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
	"runtime"
	"strings"
	"time"
)

func runSetup(args []string, input io.Reader, stdout, stderr io.Writer) int {
	return runSetupWithBrowser(context.Background(), args, input, stdout, stderr, openBrowser)
}

func runSetupWithBrowser(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer, open func(string) error) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", "", "Navigara origin (default saved host or https://app.navigara.com)")
	resume := fs.String("resume", "", "retry the exact saved setup batches in DIR")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "Usage: navigara wizard [--host ORIGIN] [--resume DIR]") }
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fs.SetOutput(stdout)
			fs.Usage()
			fs.PrintDefaults()
			return 0
		}
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "navigara: unexpected wizard argument")
		return 2
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		fmt.Fprintln(stderr, "navigara: wizard supports macOS and Linux")
		return 2
	}
	p := newSetupInput(input, stdout)
	var err error
	if *resume != "" {
		err = resumeSetup(ctx, *resume, *host, p, open)
	} else {
		err = freshSetup(ctx, *host, p, stderr, open)
	}
	if err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	return 0
}

func freshSetup(ctx context.Context, host string, p *setupInput, stderr io.Writer, open func(string) error) error {
	c, err := ensureAccount(ctx, host, p, false, open)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	opts := options{claudeHome: firstNonEmpty(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude")), codexHome: firstNonEmpty(os.Getenv("CODEX_HOME"), filepath.Join(home, ".codex"))}
	if !p.interactive() {
		writeAccountSummary(p.out, c)
		fmt.Fprintln(p.out, "\nConnect this computer to Navigara\nImport past sessions and keep new activity in sync.")
	}
	var available []string
	for _, tool := range []struct{ name, path string }{{"claude", opts.claudeHome}, {"codex", opts.codexHome}} {
		info, err := os.Stat(tool.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s home is not a directory", tool.name)
		}
		available = append(available, tool.name)
	}
	preferences, err := wizardPreferences(c, available, p)
	if err != nil {
		return err
	}
	for {
		edit, err := setupAttempt(ctx, c, opts, home, &preferences, p, stderr)
		if err != nil || !edit {
			return err
		}
		preferences, err = editWizardPreferences(c, available, preferences, p)
		if err != nil {
			return err
		}
	}
}

func setupAttempt(ctx context.Context, c accountConfig, opts options, home string, preferences *wizardSettings, p *setupInput, stderr io.Writer) (edit bool, err error) {
	opts.source, opts.email = preferences.source, preferences.email
	selected := selectedTools([]string{"claude", "codex"}, preferences.source)
	opts.url, opts.token = c.Host+"/api", firstNonEmpty(c.IngestToken, previewIngestToken)
	window := preferences.history
	var last time.Duration
	if window != "all" && window != "skip" {
		last, err = parseLast(window)
		if err != nil {
			return false, err
		}
	}
	files, selected, err := wizardPlans(&opts, home, selected, p)
	if err != nil {
		return false, err
	}
	if len(selected) == 0 {
		fmt.Fprintln(p.out, "Cancelled. Your tool settings have not changed.")
		return false, nil
	}
	opts.source = setupSource(selected)
	preferences.source = opts.source
	yes := true
	if !p.interactive() {
		yes, err = p.yes("Finish and close your running sessions so we can import complete history. Continue")
	}
	if err != nil {
		return false, err
	}
	if !yes {
		fmt.Fprintln(p.out, "Cancelled. Your tool settings have not changed.")
		return false, nil
	}
	opts.until = time.Now().UTC()
	if last > 0 {
		opts.since = opts.until.Add(-last)
	}
	state, err := os.UserConfigDir()
	if err != nil {
		return false, err
	}
	root := filepath.Join(state, "navigara", "backfills")
	if err = os.MkdirAll(root, 0700); err != nil {
		return false, err
	}
	dir, err := os.MkdirTemp(root, "setup-")
	if err != nil {
		return false, err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(dir)
		}
	}()
	opts.output = filepath.Join(dir, "payload")
	var summary bytes.Buffer
	if window != "skip" {
		fmt.Fprintln(p.out, "Preparing your session history...")
		opts.humanSummary = true
		if exportSessions(opts, &summary, stderr) != 0 {
			return false, errors.New("could not prepare backfill; no configuration changed")
		}
	} else if err = os.Mkdir(opts.output, 0700); err != nil {
		return false, err
	}
	if err = saveSetupBatches(opts.output, opts.url, c.OrganizationID); err != nil {
		return false, err
	}
	fmt.Fprintln(p.out, "\nReview your connection")
	fmt.Fprintf(p.out, "Organization: %q\nActivity belongs to: %s\n", c.OrganizationName, opts.email)
	if window == "skip" {
		fmt.Fprintln(p.out, "History: none; only new activity will be synced.")
	} else {
		fmt.Fprintf(p.out, "History: %s\n", window)
		fmt.Fprint(p.out, summary.String())
	}
	names := make([]string, len(selected))
	for i, tool := range selected {
		names[i] = setupToolName(tool)
	}
	fmt.Fprintf(p.out, "Keep new activity in sync: %s\n", strings.Join(names, ", "))
	fmt.Fprintln(p.out, "Sync usage and activity; prompt text and tool contents stay disabled. History already received live will be skipped, including partially synced sessions.")
	fmt.Fprintln(p.out, "Navigara will update your tool settings, save private backups, and store a credential for future syncing.")
	if opts.replaceCodexTelemetry {
		fmt.Fprintln(p.out, "Codex's existing telemetry will be replaced with Navigara. Your original settings will be saved in a private backup.")
	}
	action, err := wizardReview(window, p)
	if err != nil {
		return false, err
	}
	if action == wizardEdit {
		return true, nil
	}
	if action == wizardCancel {
		fmt.Fprintln(p.out, "Cancelled. No tool settings changed or session data sent.")
		return false, nil
	}
	// Keep the preview's snapshots as the concurrency boundary while replacing
	// only its token-dependent output after the user consents.
	for _, f := range files {
		if err := f.check(); err != nil {
			return false, err
		}
	}
	if err = ensureIngestion(ctx, &c); err != nil {
		return false, err
	}
	opts.token = c.IngestToken
	if err = refreshSetupPlans(files, opts, home); err != nil {
		return false, err
	}
	s := newSender(opts.url, opts.token)
	if err = applySetupFiles(files, s.checkSetup); err != nil {
		return false, fmt.Errorf("settings update failed: %w", err)
	}
	keep = true
	for _, f := range files {
		if f.backup != "" {
			fmt.Fprintln(p.out, "Previous settings saved at:", f.backup)
		}
	}
	if err = sendSetupBatches(opts.output, s); err != nil {
		return false, fmt.Errorf("%w; settings are configured; retry with navigara wizard --resume %q", err, opts.output)
	}
	fmt.Fprintf(p.out, "\nConnected. Navigara accepted %d upload batches.\nNew activity syncing is configured, not yet verified.\n", s.sent)
	fmt.Fprintln(p.out, "Open a new terminal and restart your tools. For Codex desktop/IDE, launch from that terminal so contributor identity is inherited. Check Navigara after your next session.")
	return false, nil
}

func resumeSetup(ctx context.Context, dir, host string, p *setupInput, open func(string) error) error {
	m, err := loadSetupBatches(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(p.out, "Retry %d frozen batches to %s. Tool settings will stay as configured.\n", len(m.Batches), m.URL)
	var token string
	var c accountConfig
	if m.OrganizationID == "" {
		token, err = p.token() // Legacy manifests predate saved organization context.
		if err != nil {
			return err
		}
	} else {
		var loadErr error
		c, loadErr = loadAccount(host)
		if loadErr != nil {
			return loadErr
		}
		if c.Host+"/api" != m.URL {
			return errors.New("saved backfill belongs to a different host; use --host for its original instance")
		}
		c, err = ensureAccount(ctx, host, p, false, open)
		if err != nil {
			return err
		}
		writeAccountSummary(p.out, c)
		if c.OrganizationID != m.OrganizationID {
			return errors.New("saved backfill belongs to a different organization; run navigara login and select its original organization")
		}
	}
	yes, err := p.yes("Upload these saved batches again")
	if err != nil {
		return err
	}
	if !yes {
		return nil
	}
	if m.OrganizationID != "" {
		if err = ensureIngestion(ctx, &c); err != nil {
			return err
		}
		token = c.IngestToken
	}
	s := newSender(m.URL, token)
	if err = s.validate(); err != nil {
		return err
	}
	if err = s.checkSetup(); err != nil {
		return err
	}
	if err = sendSetupBatches(dir, s); err != nil {
		return err
	}
	fmt.Fprintf(p.out, "Receiver accepted %d batches; session deduplication handles repeated data.\n", s.sent)
	return nil
}
