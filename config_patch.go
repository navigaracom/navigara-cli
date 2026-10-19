package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

const previewIngestToken = "preview-token-redacted"

// runConfigPatch updates native Claude/Codex settings using the saved account
// or a supplied ingestion token. It has no history export side effects.
func runConfigPatch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var opts options
	var apply, replace, help bool
	fs := flag.NewFlagSet("sessions config patch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "Usage: navigara sessions config patch [options]") }
	fs.StringVar(&opts.host, "host", "", "Navigara origin")
	fs.StringVar(&opts.token, "token", "", "analytics ingestor token (or $NAVIGARA_TOKEN)")
	fs.StringVar(&opts.source, "source", "all", "tool source: all, claude, codex")
	fs.StringVar(&opts.claudeHome, "claude-home", "", "Claude home (default $CLAUDE_CONFIG_DIR or ~/.claude)")
	fs.StringVar(&opts.codexHome, "codex-home", "", "Codex home (default $CODEX_HOME or ~/.codex)")
	fs.StringVar(&opts.email, "email", "", "contributor email (required with a token; default signed-in account)")
	fs.StringVar(&opts.shell, "shell", "", "shell profile for the Codex identity: bash, zsh, fish (default $SHELL)")
	fs.BoolVar(&apply, "apply", false, "apply the prepared settings")
	fs.BoolVar(&replace, "replace", false, "replace existing Claude Code or Codex telemetry settings")
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
	tokenFlag := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "token" {
			tokenFlag = true
		}
	})
	envToken, envTokenSet := os.LookupEnv("NAVIGARA_TOKEN")
	directToken := tokenFlag || envTokenSet
	if directToken {
		if !tokenFlag {
			opts.token = envToken
		}
		if opts.email == "" {
			message := "--email is required with --token."
			if !tokenFlag {
				message = "NAVIGARA_TOKEN is set; --email is required with a token."
			}
			fmt.Fprintln(stderr, "navigara:", message)
			return 1
		}
		var err error
		if opts.host == "" {
			opts.host = defaultHost
			if saved, err := loadAccount(""); err == nil && saved.UserID != "" {
				opts.host = saved.Host
				fmt.Fprintf(stdout, "Using the host of your saved login: %s\n", opts.host)
			}
		} else if opts.host, err = canonicalHost(opts.host); err != nil {
			fmt.Fprintln(stderr, "navigara:", errHostFormat)
			return 1
		}
		if err := validEmail(opts.email); err != nil {
			fmt.Fprintln(stderr, "navigara:", err)
			return 1
		}
	}
	if opts.source != "all" && opts.source != "claude" && opts.source != "codex" {
		fmt.Fprintln(stderr, "navigara: --source must be all, claude, or codex")
		return 2
	}
	if opts.shell != "" && opts.shell != "bash" && opts.shell != "zsh" && opts.shell != "fish" {
		fmt.Fprintln(stderr, "navigara: --shell must be bash, zsh, or fish")
		return 2
	}
	var s *sender
	if directToken {
		opts.url = opts.host + "/api"
		s = newSender(opts.url, opts.token)
		s.explicitKey = true
		if err := s.validate(); err != nil {
			fmt.Fprintln(stderr, "navigara:", err)
			return 1
		}
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

	var c accountConfig
	if !directToken {
		c, err = signedInAccount(ctx, opts.host, "navigara sessions config patch", stderr)
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
	}
	files, err := setupPlans(opts, home)
	if err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	if directToken {
		fmt.Fprintf(stdout, "%s config patch (email %s)\n", map[bool]string{true: "Applying", false: "Previewing"}[apply], opts.email)
		fmt.Fprintf(stdout, "Host: %s\n", opts.host)
	} else {
		fmt.Fprintf(stdout, "%s config patch for %q (%s, email %s)\n", map[bool]string{true: "Applying", false: "Previewing"}[apply], c.OrganizationName, c.OrganizationID, opts.email)
		fmt.Fprintf(stdout, "Host: %s\n", c.Host)
	}
	fmt.Fprintf(stdout, "Claude home: %s\nCodex home: %s\n", opts.claudeHome, opts.codexHome)
	conflict := planConflict(files)
	if !apply {
		printPlan(stdout, files)
		if conflict != nil && !replace {
			fmt.Fprintln(stdout, conflict)
		}
		fmt.Fprintln(stdout, "Preview only; no tool settings changed and no ingestion request sent.")
		return 0
	}
	if conflict != nil && !replace {
		fmt.Fprintln(stderr, "navigara:", conflict)
		return 1
	}
	if !directToken {
		if err := ensureIngestion(ctx, &c); err != nil {
			fmt.Fprintln(stderr, "navigara:", err)
			return 1
		}
		opts.token = c.IngestToken
		if err := refreshSetupPlans(files, opts, home); err != nil {
			fmt.Fprintln(stderr, "navigara:", err)
			return 1
		}
		s = newSender(opts.url, opts.token)
	}
	if !printPlan(stdout, files) {
		return 0
	}
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

// printPlan prints one line per file and reports whether any file changes.
func printPlan(w io.Writer, files []*setupFile) bool {
	changed := false
	for _, f := range files {
		if f.unchanged() {
			fmt.Fprintln(w, "Unchanged:", f.path)
			continue
		}
		changed = true
		fmt.Fprintln(w, "Update:", f.path)
		for _, note := range f.notes {
			fmt.Fprintln(w, "  "+note)
		}
	}
	for _, f := range files {
		if f.warning != "" {
			fmt.Fprintln(w, "Warning:", f.warning)
		}
	}
	if !changed {
		fmt.Fprintln(w, "No changes needed.")
	}
	return changed
}

// runConfigRemove removes the settings that config patch and the wizard wrote.
func runConfigRemove(args []string, stdout, stderr io.Writer) int {
	var opts options
	var apply, help bool
	fs := flag.NewFlagSet("sessions config remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "Usage: navigara sessions config remove [options]") }
	fs.StringVar(&opts.source, "source", "all", "tool source: all, claude, codex")
	fs.StringVar(&opts.claudeHome, "claude-home", "", "Claude home (default $CLAUDE_CONFIG_DIR or ~/.claude)")
	fs.StringVar(&opts.codexHome, "codex-home", "", "Codex home (default $CODEX_HOME or ~/.codex)")
	fs.BoolVar(&apply, "apply", false, "remove the settings")
	fs.BoolVar(&help, "help", false, "show remove help")
	fs.BoolVar(&help, "h", false, "show remove help")
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
	if err == nil {
		opts.claudeHome, opts.codexHome, err = toolHomes(opts.claudeHome, opts.codexHome)
	}
	var files []*setupFile
	if err == nil {
		files, err = removePlans(opts, home)
	}
	if err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	var changed []*setupFile
	for _, f := range files {
		if bytes.Equal(f.before, f.after) {
			fmt.Fprintln(stdout, "Unchanged:", f.path)
			continue
		}
		fmt.Fprintln(stdout, "Update:", f.path)
		changed = append(changed, f)
	}
	if len(changed) == 0 {
		fmt.Fprintln(stdout, "No changes needed.")
		return 0
	}
	if !apply {
		fmt.Fprintln(stdout, "Preview only; no files changed. Run again with --apply to remove the settings.")
		return 0
	}
	if err := applySetupFiles(changed, func() error { return nil }); err != nil {
		fmt.Fprintln(stderr, "navigara: settings update failed:", err)
		return 1
	}
	for _, f := range changed {
		if f.backup != "" {
			fmt.Fprintln(stdout, "Backup:", f.backup)
		}
	}
	fmt.Fprintln(stdout, "Navigara settings removed. Restart Claude Code, Codex, and your shell.")
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
