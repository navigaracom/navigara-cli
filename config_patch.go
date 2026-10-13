package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

const previewIngestToken = "preview-token-redacted"

// runConfigPatch updates the native Claude/Codex settings using the saved
// account. It intentionally has no history or OTLP export side effects.
func runConfigPatch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var opts options
	var apply, help bool
	fs := flag.NewFlagSet("sessions config patch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "Usage: navigara sessions config patch [options]") }
	fs.StringVar(&opts.host, "host", "", "Navigara origin")
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
	claudeFlag, codexFlag := opts.claudeHome, opts.codexHome
	if opts.claudeHome, opts.codexHome, err = toolHomes(claudeFlag, codexFlag); err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	if err := checkPatchHomes(&opts, claudeFlag, codexFlag); err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}

	c, err := verifiedAccount(ctx, opts.host)
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
	opts.url, opts.token = c.Host+"/api", firstNonEmpty(c.IngestToken, previewIngestToken)
	files, err := setupPlans(opts, home)
	if err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s config patch for %q (%s, email %s)\n", map[bool]string{true: "Applying", false: "Previewing"}[apply], c.OrganizationName, c.OrganizationID, opts.email)
	fmt.Fprintf(stdout, "Host: %s\nClaude home: %s\nCodex home: %s\n", c.Host, opts.claudeHome, opts.codexHome)
	for _, f := range files {
		fmt.Fprintln(stdout, "Update:", f.path)
	}
	if !apply {
		fmt.Fprintln(stdout, "Preview only; no files changed and no ingestion request sent.")
		return 0
	}
	if err := ensureIngestion(ctx, &c); err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	opts.token = c.IngestToken
	if err := refreshSetupPlans(files, opts, home); err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}

	s := newSender(opts.url, opts.token)
	if err := applySetupFiles(files, func() error { return s.checkSetup(ctx) }); err != nil {
		fmt.Fprintln(stderr, "navigara: settings update failed:", err)
		return 1
	}
	for _, f := range files {
		if f.backup != "" {
			fmt.Fprintln(stdout, "Backup:", f.backup)
		}
	}
	fmt.Fprintln(stdout, "Settings applied successfully.")
	return 0
}

func checkPatchHomes(opts *options, claudeFlag, codexFlag string) error {
	claudeExplicit, codexExplicit := claudeFlag != "", codexFlag != ""
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
	claudeMissing := fmt.Sprintf("Claude home %s%s was not found", opts.claudeHome, homeSource(claudeFlag, "CLAUDE_CONFIG_DIR"))
	codexMissing := fmt.Sprintf("Codex home %s%s was not found", opts.codexHome, homeSource(codexFlag, "CODEX_HOME"))
	if opts.source == "claude" && !claude {
		return errors.New(claudeMissing + "; run Claude Code once or pass --claude-home")
	}
	if opts.source == "codex" && !codex {
		return errors.New(codexMissing + "; run Codex once or pass --codex-home")
	}
	if opts.source == "all" && !claude && !codex {
		return errors.New(claudeMissing + " and " + codexMissing + "; run Claude Code or Codex once, then retry")
	}
	if opts.source == "all" {
		switch {
		case !claude:
			opts.source = "codex"
		case !codex:
			opts.source = "claude"
		}
	}
	return nil
}
