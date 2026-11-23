package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

func runSetup(ctx context.Context, args []string, input io.Reader, stdout, stderr io.Writer) int {
	return runSetupWithBrowser(ctx, args, input, stdout, stderr, openBrowser)
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
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		fmt.Fprintln(stderr, "navigara: wizard supports macOS, Linux, and Windows")
		return 2
	}
	p := newSetupInput(input, stdout)
	p.command = "navigara wizard"
	var err error
	if *resume != "" {
		err = resumeSetup(ctx, *resume, *host, p, stderr, open)
	} else {
		err = freshSetup(ctx, *host, p, stderr, open)
	}
	if err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		if errors.Is(err, context.Canceled) {
			return exitInterrupted
		}
		return 1
	}
	return 0
}

func freshSetup(ctx context.Context, host string, p *setupInput, stderr io.Writer, open func(string) error) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var opts options
	if opts.claudeHome, opts.codexHome, err = toolHomes("", ""); err != nil {
		return err
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
	if len(available) == 0 {
		return fmt.Errorf("Claude Code and Codex were not found on this computer (looked in %s and %s). Install one, run it once, then run navigara wizard again.",
			homePath(opts.claudeHome, home), homePath(opts.codexHome, home))
	}
	c, err := ensureAccount(ctx, host, p, false, open)
	if err != nil {
		return err
	}
	if !p.interactive() {
		writeAccountSummary(p.out, c)
		fmt.Fprintln(p.out, "\nConnect this computer to Navigara\nImport past sessions and keep new activity in sync.")
	}
	preferences, err := wizardPreferences(c, available, p)
	if err != nil {
		return err
	}
	return setupAttempt(ctx, c, opts, home, preferences, p, stderr, open)
}

func homePath(path, home string) string {
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return filepath.Join("~", rest)
	}
	return path
}

func setupAttempt(ctx context.Context, c accountConfig, opts options, home string, preferences wizardSettings, p *setupInput, stderr io.Writer, open func(string) error) error {
	opts.source, opts.email = preferences.source, preferences.email
	selected := selectedTools([]string{"claude", "codex"}, preferences.source)
	opts.url, opts.token = c.Host+"/api", firstNonEmpty(c.IngestToken, previewIngestToken)
	window := preferences.history
	var last time.Duration
	if window == "since" {
		opts.sourceSince = map[string]time.Time{}
		for _, tool := range selected {
			opts.sourceSince[tool] = preferences.imports[tool]
		}
	} else if window != "all" && window != "skip" {
		var err error
		last, err = parseLast(window)
		if err != nil {
			return err
		}
	}
	files, selected, err := wizardPlans(&opts, home, selected, p)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		p.cancelled()
		return nil
	}
	opts.source = setupSource(selected)
	yes := true
	if !p.interactive() {
		yes, err = p.yes("Finish and close your running sessions so we can import complete history. Continue")
	}
	if err != nil {
		return err
	}
	if !yes {
		p.cancelled()
		return nil
	}
	if err = ensureIngestion(ctx, &c); err != nil {
		return err
	}
	opts.token = c.IngestToken
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
	var history *setupHistoryProgress
	var historyOut, historyErr bytes.Buffer
	if window != "skip" {
		opts.humanSummary = true
		opts.skippedTools = map[string]bool{}
		history = newSetupHistoryProgress(p)
		history.discovering()
		historyCtx, stopHistory := interruptible(ctx)
		code := exportSessionsWithProgress(historyCtx, opts, &historyOut, &historyErr, history.processed)
		interrupted := historyCtx.Err() != nil
		stopHistory()
		if interrupted {
			history.stop()
			return fmt.Errorf("interrupted; no configuration changed: %w", context.Canceled)
		}
		if code != 0 {
			history.stop()
			fmt.Fprint(p.out, historyOut.String())
			fmt.Fprint(stderr, historyErr.String())
			return errors.New("could not read session history; no configuration changed")
		}
	} else if err = os.Mkdir(opts.output, 0700); err != nil {
		return err
	}
	if history != nil {
		history.finalizing()
	}
	var imported []string
	if window != "skip" {
		for _, tool := range selected {
			// A file skipped now may be readable later; keep its history in the next import.
			if !opts.skippedTools[tool] {
				imported = append(imported, tool)
			}
		}
	}
	err = saveSetupManifest(opts.output, setupManifest{URL: opts.url, OrganizationID: c.OrganizationID, ImportedUntil: opts.until, ImportedTools: imported})
	if history != nil {
		history.stop()
	}
	if err != nil {
		if history != nil {
			fmt.Fprint(stderr, historyErr.String())
		}
		return err
	}
	if history != nil {
		p.status("History prepared", historyOut.String())
		fmt.Fprint(stderr, historyErr.String())
	}
	var s *sender
	err = p.busy("Connecting your tools...", func() error {
		// Keep the original snapshots as the concurrency boundary while replacing
		// only token-dependent output after the receiver capability check.
		for _, f := range files {
			if err := f.check(); err != nil {
				return err
			}
		}
		if err := refreshSetupPlans(files, opts, home); err != nil {
			return err
		}
		s = newSender(opts.url, opts.token)
		if err := applySetupFiles(files, func() error { return s.checkSetup(ctx) }); err != nil {
			return fmt.Errorf("settings update failed: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	keep = true
	backups := make([]string, 0, len(files))
	for _, f := range files {
		if f.backup != "" {
			backups = append(backups, f.backup)
		}
	}
	if len(backups) > 0 && p.interactive() {
		body := "Your previous settings are saved here:\n"
		for _, backup := range backups {
			body += "  " + wizardPathValue(backup) + "\n"
		}
		p.status("Settings backup", body)
		fmt.Fprintln(p.out)
	} else {
		for _, backup := range backups {
			fmt.Fprintln(p.out, "Previous settings saved at:", backup)
		}
	}
	progress := newSetupProgress(p)
	progress.stderr = stderr
	s.retried = progress.retry
	uploadCtx, stopUpload := interruptible(ctx)
	err = sendSetupBatches(uploadCtx, opts.output, s, progress.upload, false)
	stopUpload()
	if err != nil {
		progress.stop()
		return fmt.Errorf("%w; settings are configured; retry with navigara wizard --resume %q", err, opts.output)
	}
	saveImports(c, opts.until, imported, stderr)
	sessions := -1
	if window != "skip" {
		fmt.Sscanf(historyOut.String(), "%d sessions", &sessions)
	}
	url := welcomeURL(c.Host)
	p.completed(sessions, selected, url)
	if slices.Contains(selected, "codex") {
		if receipt := s.codexSummary(); receipt != "" {
			p.status("Codex usage receipt", receipt)
		}
	}
	continueSetup(p, url, p.confirmOpen(), open)
	return nil
}

// saveImports never moves a checkpoint back, so resuming an older payload keeps a newer import.
func saveImports(c accountConfig, until time.Time, tools []string, stderr io.Writer) {
	if len(tools) == 0 {
		return
	}
	updated := c
	updated.Imports = maps.Clone(c.Imports)
	if updated.Imports == nil {
		updated.Imports = map[string]time.Time{}
	}
	for _, tool := range tools {
		key := c.OrganizationID + "/" + tool
		if !updated.Imports[key].After(until) {
			updated.Imports[key] = until
		}
	}
	if err := writeAccount(updated, &c); err != nil {
		fmt.Fprintln(stderr, "navigara: could not save the import time:", err)
	}
}

func resumeSetup(ctx context.Context, dir, host string, p *setupInput, stderr io.Writer, open func(string) error) error {
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
	if err = s.checkSetup(ctx); err != nil {
		return err
	}
	if p.interactive() {
		fmt.Fprintln(p.out)
	}
	progress := newSetupProgress(p)
	s.retried = progress.retry
	uploadCtx, stopUpload := interruptible(ctx)
	err = sendSetupBatches(uploadCtx, dir, s, progress.upload, false)
	stopUpload()
	if err != nil {
		progress.stop()
		return err
	}
	body := fmt.Sprintf("Navigara received %d upload batches.\n", s.sent) + s.codexSummary()
	url := ""
	if m.OrganizationID != "" {
		url = welcomeURL(c.Host)
	}
	if url != "" {
		body += "Check: open " + url + " after your next session.\n"
	}
	p.status("History upload received", body)
	if m.OrganizationID != "" {
		saveImports(c, m.ImportedUntil, m.ImportedTools, stderr)
	}
	continueSetup(p, url, p.confirmOpen(), open)
	return nil
}
