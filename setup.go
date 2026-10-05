package main

import (
	"bufio"
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
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "Usage: navigara setup [--host ORIGIN] [--resume DIR]") }
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
		fmt.Fprintln(stderr, "navigara: unexpected setup argument")
		return 2
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		fmt.Fprintln(stderr, "navigara: setup supports macOS and Linux")
		return 2
	}
	p := &setupInput{r: bufio.NewReader(input), out: stdout}
	if f, ok := input.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			p.terminal = f
		}
	}
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
	fmt.Fprintln(p.out, "Set up this computer for Navigara backfill and native telemetry.")
	var selected []string
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
		yes, err := p.yes("Configure " + tool.name + " at " + tool.path)
		if err != nil {
			return err
		}
		if yes {
			selected = append(selected, tool.name)
		}
	}
	if len(selected) == 0 {
		return errors.New("no tools selected; run Claude Code or Codex once, then rerun setup")
	}
	opts.source = selected[0]
	if len(selected) == 2 {
		opts.source = "all"
	}
	opts.url, opts.token = c.Host+"/api", firstNonEmpty(c.IngestToken, previewIngestToken)
	opts.email, err = p.ask("Contributor email for both backfill and streaming", c.Email)
	if err != nil {
		return err
	}
	if err = validEmail(opts.email); err != nil {
		return err
	}
	window, err := p.ask("Import history: duration (7d, 24h), all, or skip", "30d")
	if err != nil {
		return err
	}
	var last time.Duration
	if window != "all" && window != "skip" {
		last, err = parseLast(window)
		if err != nil {
			return err
		}
	}
	files, err := setupPlans(opts, home)
	if err != nil {
		return err
	}
	yes, err := p.yes("Finish and close running Claude Code/Codex sessions before continuing. Ready")
	if err != nil {
		return err
	}
	if !yes {
		fmt.Fprintln(p.out, "Setup cancelled.")
		return nil
	}
	opts.until = time.Now().UTC()
	if last > 0 {
		opts.since = opts.until.Add(-last)
	}
	state, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	root := filepath.Join(state, "navigara", "backfills")
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(root, "setup-")
	if err != nil {
		return err
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
		if exportSessions(opts, &summary, stderr) != 0 {
			return errors.New("could not prepare backfill; no configuration changed")
		}
	} else if err = os.Mkdir(opts.output, 0700); err != nil {
		return err
	}
	if err = saveSetupBatches(opts.output, opts.url, c.OrganizationID); err != nil {
		return err
	}
	fmt.Fprintln(p.out, "\nBackfill preview:")
	if window == "skip" {
		fmt.Fprintln(p.out, "Historical import skipped.")
	} else {
		fmt.Fprint(p.out, summary.String())
	}
	fmt.Fprintf(p.out, "\nNative telemetry: %s\nEndpoint: %s\nContributor: %s\nAuthorization: Bearer <hidden>\n", strings.Join(selected, ", "), otlpBase(opts.url), opts.email)
	fmt.Fprintln(p.out, "Enable metrics and logs; prompts and tool contents stay disabled. Existing live session data takes precedence over backfill, including partially live sessions.")
	for _, f := range files {
		fmt.Fprintln(p.out, "Update:", f.path)
	}
	fmt.Fprintln(p.out, "Private backups will be saved beside changed files. The token is stored in tool settings for future exports.")
	yes, err = p.yes("Apply these settings and upload the preview")
	if err != nil {
		return err
	}
	if !yes {
		fmt.Fprintln(p.out, "Setup cancelled; no tool settings changed or telemetry sent.")
		return nil
	}
	if err = ensureIngestion(ctx, &c); err != nil {
		return err
	}
	opts.token = c.IngestToken
	files, err = setupPlans(opts, home)
	if err != nil {
		return err
	}
	s := newSender(opts.url, opts.token)
	if err = applySetupFiles(files, s.checkSetup); err != nil {
		return fmt.Errorf("settings update failed: %w", err)
	}
	keep = true
	fmt.Fprintf(p.out, "Frozen backfill: %s\n", opts.output)
	for _, f := range files {
		if f.backup != "" {
			fmt.Fprintln(p.out, "Backup:", f.backup)
		}
	}
	if err = sendSetupBatches(opts.output, s); err != nil {
		return fmt.Errorf("%w; settings are configured; retry with navigara setup --resume %q", err, opts.output)
	}
	fmt.Fprintf(p.out, "Receiver accepted %d batches. Streaming configured, not yet verified.\n", s.sent)
	fmt.Fprintln(p.out, "Open a new terminal and restart your tools. For Codex desktop/IDE, launch from that terminal so contributor identity is inherited. Check Navigara after your next session.")
	return nil
}

func resumeSetup(ctx context.Context, dir, host string, p *setupInput, open func(string) error) error {
	m, err := loadSetupBatches(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(p.out, "Retry %d frozen batches to %s. Tool settings will stay as configured.\n", len(m.Batches), m.URL)
	var token string
	if m.OrganizationID == "" {
		token, err = p.token() // Legacy manifests predate saved organization context.
	} else {
		c, loadErr := loadAccount(host)
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
		if c.OrganizationID != m.OrganizationID {
			return errors.New("saved backfill belongs to a different organization; run navigara login and select its original organization")
		}
		if err = ensureIngestion(ctx, &c); err != nil {
			return err
		}
		token = c.IngestToken
	}
	if err != nil {
		return err
	}
	s := newSender(m.URL, token)
	if err = s.validate(); err != nil {
		return err
	}
	yes, err := p.yes("Upload these saved batches again")
	if err != nil {
		return err
	}
	if !yes {
		return nil
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
