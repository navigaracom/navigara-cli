package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/navigaracom/navigara-cli/internal/session"
	"github.com/navigaracom/navigara-cli/internal/telemetry"
)

// exportSessions rebuilds each selected session's telemetry, writes the OTLP bodies and,
// with --send, posts them to the Navigara OTLP endpoint.
func exportSessions(opts options, stdout, stderr io.Writer) int {
	claudeHome, codexHome := toolHome(opts.claudeHome, ".claude"), toolHome(opts.codexHome, ".codex")
	ident := telemetry.ReadIdentity(claudeHome, codexHome)
	if opts.email != "" {
		ident = ident.WithEmail(opts.email)
	}
	// Each tool needs its own signed-in email: one tool's account says nothing about the other's.
	claudeOK := ident.Email != "" || opts.allowAnonymous
	codexOK := ident.CodexEmail != "" || opts.allowAnonymous
	wantClaude := opts.source == "all" || opts.source == "claude"
	wantCodex := opts.source == "all" || opts.source == "codex"
	if (!wantClaude || !claudeOK) && (!wantCodex || !codexOK) {
		fmt.Fprintln(stderr, "navigara: no signed-in email found; pass --email or --allow-anonymous")
		return 2
	}
	if wantClaude && !claudeOK {
		fmt.Fprintln(stderr, "navigara: skipping Claude Code: no signed-in email; pass --email or --allow-anonymous")
		wantClaude = false
	}
	if wantCodex && !codexOK {
		fmt.Fprintln(stderr, "navigara: skipping Codex: no signed-in email; pass --email or --allow-anonymous")
		wantCodex = false
	}

	pinned := map[string]bool{}
	for _, id := range opts.sessions {
		pinned[id] = true
	}
	want := func(id string) bool { return len(pinned) == 0 || pinned[id] }

	repos := repositoryResolver()
	var sessions []*telemetry.Session
	failed := 0
	incomplete := false
	keep := func(s *telemetry.Session, err error, id string) {
		if err != nil {
			failed++
			fmt.Fprintf(stderr, "navigara: %s: %v\n", id, err)
			return
		}
		s.Within(opts.since, opts.until)
		if len(s.Events) > 0 || len(s.Points) > 0 {
			sessions = append(sessions, s)
		}
	}
	if wantClaude {
		scan := session.Scan("claude", opts.claudeHome, opts.codexHome)
		incomplete = len(scan.Errors) > 0
		for _, err := range scan.Errors {
			fmt.Fprintln(stderr, "navigara:", err)
		}
		seen := map[string]bool{}
		for _, r := range scan.Records {
			id := r.Session.SessionID
			if seen[id] || !want(id) {
				continue
			}
			seen[id] = true
			s, err := telemetry.BuildClaude(telemetry.ClaudeInput{Path: r.Path, Subagents: telemetry.ClaudeSubagents(r.Path), Until: opts.until}, ident, repos)
			keep(s, err, id)
		}
	}
	if wantCodex {
		seen := map[string]bool{}
		paths, err := telemetry.CodexRollouts(codexHome)
		if err != nil {
			fmt.Fprintln(stderr, "navigara:", err)
			incomplete = true
		} else {
			for _, path := range paths {
				id, err := telemetry.CodexSessionID(path)
				if err != nil {
					fmt.Fprintln(stderr, "navigara:", err)
					incomplete = true
					continue
				}
				if seen[id] || !want(id) {
					continue
				}
				seen[id] = true
				s, err := telemetry.BuildCodex(path, ident, opts.until)
				keep(s, err, id)
			}
		}
	}
	if incomplete {
		return 1
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Source != sessions[j].Source {
			return sessions[i].Source < sessions[j].Source
		}
		return sessions[i].SessionID < sessions[j].SessionID
	})

	bodies, err := telemetry.Encode(sessions)
	if err != nil {
		fmt.Fprintln(stderr, "navigara: encode:", err)
		return 1
	}

	if err := os.Mkdir(opts.output, 0700); err != nil {
		fmt.Fprintf(stderr, "navigara: output %q: %v\n", opts.output, err)
		return 1
	}
	counts := map[string]int{}
	for _, b := range bodies {
		kind := strings.TrimPrefix(b.Path, "/v1/")
		counts[kind]++
		path := filepath.Join(opts.output, fmt.Sprintf("%s-%06d.json", kind, counts[kind]))
		if err := writeRequest(path, b.Data); err != nil {
			fmt.Fprintf(stderr, "navigara: output %q: %v\n", path, err)
			return 1
		}
	}

	events, points := 0, 0
	for _, s := range sessions {
		events += len(s.Events)
		points += len(s.Points)
	}
	fmt.Fprintf(stdout, "sessions=%d failed=%d events=%d points=%d bodies=%d identity=%s\n",
		len(sessions), failed, events, points, len(bodies), firstNonEmpty(ident.Email, ident.CodexEmail, "anonymous"))
	for _, s := range sessions {
		note := ""
		if s.Estimated {
			note = " cost=estimated"
		}
		for reason, n := range s.Missing {
			note += fmt.Sprintf(" missing[%s]=%d", reason, n)
		}
		fmt.Fprintf(stdout, "%s %s events=%d points=%d%s\n", s.Source, s.SessionID, len(s.Events), len(s.Points), note)
	}

	if opts.send {
		if err := send(opts, bodies, stdout); err != nil {
			fmt.Fprintln(stderr, "navigara: send:", err)
			return 1
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func toolHome(flag, dir string) string {
	if flag != "" {
		return flag
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return dir
	}
	return filepath.Join(home, dir)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// repositoryResolver reads a working directory's origin remote, as Claude Code reports it.
func repositoryResolver() func(cwd string) string {
	cache := map[string]string{}
	return func(cwd string) string {
		if cwd == "" {
			return ""
		}
		if url, ok := cache[cwd]; ok {
			return url
		}
		url := ""
		out, err := exec.Command("git", "-C", cwd, "config", "--get", "remote.origin.url").Output()
		if err == nil {
			if key := session.RepositoryKey(strings.TrimSpace(string(out))); key != "" {
				url = "https://" + key
			}
		}
		cache[cwd] = url
		return url
	}
}

// send posts every body, retrying throttling and server errors with backoff.
func send(opts options, bodies []telemetry.Body, stdout io.Writer) error {
	if opts.url == "" || opts.token == "" {
		return errors.New("--send needs --url and --token (or NAVIGARA_TOKEN)")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	base := strings.TrimSuffix(opts.url, "/") + "/otlp"
	for i, b := range bodies {
		var lastErr error
		for attempt := 0; attempt < 5; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(1<<attempt) * time.Second)
			}
			req, err := http.NewRequest(http.MethodPost, base+b.Path, bytes.NewReader(b.Data))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+opts.token)
			resp, err := client.Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			switch {
			case resp.StatusCode == http.StatusOK:
				lastErr = nil
			case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
				return fmt.Errorf("%s: HTTP %d; the API key needs the analytics ingestor role", b.Path, resp.StatusCode)
			case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
				lastErr = fmt.Errorf("%s: HTTP %d", b.Path, resp.StatusCode)
				continue
			default:
				return fmt.Errorf("%s: HTTP %d", b.Path, resp.StatusCode)
			}
			break
		}
		if lastErr != nil {
			return fmt.Errorf("body %d of %d: %w", i+1, len(bodies), lastErr)
		}
	}
	fmt.Fprintf(stdout, "sent=%d\n", len(bodies))
	return nil
}

func writeRequest(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
