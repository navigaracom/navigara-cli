package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const previewIngestToken = "preview-token-redacted"

// runConfigPatch updates the native Claude/Codex settings using the saved
// account. It intentionally has no history or OTLP export side effects.
func runConfigPatch(args []string, stdout, stderr io.Writer) int {
	var opts options
	var apply, help bool
	fs := flag.NewFlagSet("sessions config patch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "Usage: navigara sessions config patch [options]") }
	fs.StringVar(&opts.url, "host", "", "Navigara origin")
	fs.StringVar(&opts.source, "source", "all", "tool source: all, claude, codex")
	fs.StringVar(&opts.claudeHome, "claude-home", "", "Claude home (default $CLAUDE_CONFIG_DIR or ~/.claude)")
	fs.StringVar(&opts.codexHome, "codex-home", "", "Codex home (default $CODEX_HOME or ~/.codex)")
	fs.StringVar(&opts.email, "email", "", "contributor email (default signed-in account)")
	fs.BoolVar(&apply, "apply", false, "apply the prepared settings")
	fs.BoolVar(&help, "help", false, "show patch help")
	fs.BoolVar(&help, "h", false, "show patch help")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if help {
		fs.SetOutput(stdout)
		fs.Usage()
		fs.PrintDefaults()
		return 0
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "navigara: unexpected argument: "+fs.Arg(0))
		return 2
	}
	if opts.source != "all" && opts.source != "claude" && opts.source != "codex" {
		fmt.Fprintln(stderr, "navigara: --source must be all, claude, or codex")
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	claudeExplicit, codexExplicit := false, false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "claude-home" {
			claudeExplicit = true
		}
		if f.Name == "codex-home" {
			codexExplicit = true
		}
	})
	if claudeExplicit && opts.claudeHome == "" {
		fmt.Fprintln(stderr, "navigara: --claude-home must not be empty")
		return 2
	}
	if codexExplicit && opts.codexHome == "" {
		fmt.Fprintln(stderr, "navigara: --codex-home must not be empty")
		return 2
	}
	opts.claudeHome = firstNonEmpty(opts.claudeHome, os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude"))
	opts.codexHome = firstNonEmpty(opts.codexHome, os.Getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
	if err := checkPatchHomes(&opts, claudeExplicit, codexExplicit); err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}

	c, err := verifiedAccount(context.Background(), opts.url)
	if err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	if opts.email == "" {
		opts.email = c.Email
	}
	if err := validEmail(opts.email); err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	if !apply {
		opts.url = c.Host + "/api"
		opts.token = previewIngestToken
		if c.IngestToken != "" {
			opts.token = c.IngestToken
		}
	} else {
		if err := ensureIngestion(context.Background(), &c); err != nil {
			fmt.Fprintln(stderr, "navigara:", err)
			return 1
		}
		opts.url, opts.token = c.Host+"/api", c.IngestToken
	}
	files, err := setupPlans(opts, home)
	if err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s config patch for %s (%s, email %s)\n", map[bool]string{true: "Applying", false: "Previewing"}[apply], c.OrganizationName, c.OrganizationID, opts.email)
	fmt.Fprintf(stdout, "Host: %s\nClaude home: %s\nCodex home: %s\n", c.Host, opts.claudeHome, opts.codexHome)
	for _, f := range files {
		fmt.Fprintln(stdout, "Update:", f.path)
	}
	if !apply {
		fmt.Fprintln(stdout, "Preview only; no files changed and no ingestion request sent.")
		return 0
	}

	s := newSender(opts.url, opts.token)
	prepared := make([]string, len(files))
	for i, f := range files {
		prepared[i], err = f.prepare()
		if err != nil {
			cleanupPrepared(prepared)
			fmt.Fprintln(stderr, "navigara:", err)
			return 1
		}
	}
	if err = s.checkSetup(); err != nil {
		cleanupPrepared(prepared)
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	installed := make([]*setupFile, 0, len(files))
	for i, f := range files {
		if err = f.check(); err != nil {
			cleanupPrepared(prepared)
			fmt.Fprintln(stderr, "navigara:", err)
			return 1
		}
		if f.existed && bytes.Equal(f.before, f.after) && patchFilePrivate(f.path) {
			_ = os.Remove(prepared[i])
			installed = append(installed, f)
			continue
		}
		if err = f.install(prepared[i]); err != nil {
			rollbackErr := rollbackPatch(installed)
			cleanupPrepared(prepared)
			if rollbackErr != nil {
				err = fmt.Errorf("%w; rollback failed: %v", err, rollbackErr)
			}
			fmt.Fprintln(stderr, "navigara: settings update failed:", err)
			return 1
		}
		installed = append(installed, f)
		if f.backup != "" {
			fmt.Fprintln(stdout, "Backup:", f.backup)
		}
	}
	cleanupPrepared(prepared)
	fmt.Fprintln(stdout, "Settings applied successfully.")
	return 0
}

func checkPatchHomes(opts *options, claudeExplicit, codexExplicit bool) error {
	check := func(name, path string, explicit bool) (bool, error) {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			if explicit {
				return false, fmt.Errorf("%s home %s does not exist", name, path)
			}
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() {
			return false, fmt.Errorf("%s home is not a directory", name)
		}
		return true, nil
	}
	claude, codex := false, false
	var err error
	if opts.source == "all" || opts.source == "claude" {
		claude, err = check("Claude", opts.claudeHome, claudeExplicit)
		if err != nil {
			return err
		}
	}
	if opts.source == "all" || opts.source == "codex" {
		codex, err = check("Codex", opts.codexHome, codexExplicit)
		if err != nil {
			return err
		}
	}
	if opts.source == "claude" && !claude {
		return errors.New("Claude home was not found; run Claude Code once or pass --claude-home")
	}
	if opts.source == "codex" && !codex {
		return errors.New("Codex home was not found; run Codex once or pass --codex-home")
	}
	if opts.source == "all" && !claude && !codex {
		return errors.New("no tool homes found; run Claude Code or Codex once, then retry")
	}
	if opts.source == "all" {
		if !claude {
			opts.source = "codex"
		}
		if !codex && claude {
			opts.source = "claude"
		}
	}
	return nil
}

func cleanupPrepared(paths []string) {
	for _, path := range paths {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

func patchFilePrivate(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600
}

func rollbackPatch(files []*setupFile) error {
	var rollbackErr error
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		data, exists, err := readSetupFile(f.path)
		if err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("inspect %s: %w", f.path, err))
			continue
		}
		if !exists || !bytes.Equal(data, f.after) {
			continue
		}
		if err := os.Remove(f.path); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove %s: %w", f.path, err))
			continue
		}
		if f.backup != "" {
			if restored, err := restoreIfAbsent(f.backup, f.path); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore %s: %w", f.path, err))
			} else if !restored {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore %s: concurrent replacement retained", f.path))
			}
		}
	}
	return rollbackErr
}
