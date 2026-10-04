package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
	commits := &commitIndex{email: ident.Email, byRepo: map[string][]commit{}}
	type job struct {
		source, id string
		build      func() (*telemetry.Session, error)
	}
	var jobs []job
	if wantClaude {
		scan := session.Scan("claude", opts.claudeHome, opts.codexHome)
		for _, err := range scan.Errors {
			fmt.Fprintln(stderr, "navigara:", err)
		}
		seen := map[string]bool{}
		for _, r := range scan.Records {
			id, path := r.Session.SessionID, r.Path
			if seen[id] || !want(id) {
				continue
			}
			seen[id] = true
			jobs = append(jobs, job{"claude_code", id, func() (*telemetry.Session, error) {
				return telemetry.BuildClaude(telemetry.ClaudeInput{Path: path, Subagents: telemetry.ClaudeSubagents(path),
					Until: opts.until, Commits: commits.between}, ident, repos)
			}})
		}
	}
	if wantCodex {
		seen := map[string]bool{}
		for _, path := range telemetry.CodexRollouts(codexHome) {
			id := telemetry.CodexSessionID(path)
			if id == "" || seen[id] || !want(id) {
				continue
			}
			seen[id] = true
			jobs = append(jobs, job{"codex", id, func() (*telemetry.Session, error) {
				return telemetry.BuildCodex(path, ident, opts.until)
			}})
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].source != jobs[j].source {
			return jobs[i].source < jobs[j].source
		}
		return jobs[i].id < jobs[j].id
	})

	if err := os.Mkdir(opts.output, 0700); err != nil {
		fmt.Fprintf(stderr, "navigara: output %q: %v\n", opts.output, err)
		return 1
	}
	var sender *sender
	if opts.send {
		if opts.url == "" || opts.token == "" {
			fmt.Fprintln(stderr, "navigara: --send needs --url and --token (or NAVIGARA_TOKEN)")
			return 2
		}
		sender = newSender(opts.url, opts.token)
	}
	// One session at a time: build, write and send it before the next, so memory stays flat.
	counts := map[string]int{}
	exported, failed, events, points, bodies := 0, 0, 0, 0, 0
	var lines []string
	for _, j := range jobs {
		s, err := j.build()
		if err != nil {
			failed++
			fmt.Fprintf(stderr, "navigara: %s: %v\n", j.id, err)
			continue
		}
		s.Within(opts.since, opts.until)
		if len(s.Events) == 0 && len(s.Points) == 0 {
			continue
		}
		encoded, err := telemetry.Encode([]*telemetry.Session{s})
		if err != nil {
			fmt.Fprintf(stderr, "navigara: encode %s: %v\n", j.id, err)
			return 1
		}
		for _, b := range encoded {
			kind := strings.TrimPrefix(b.Path, "/v1/")
			counts[kind]++
			path := filepath.Join(opts.output, fmt.Sprintf("%s-%06d.json", kind, counts[kind]))
			if err := writeRequest(path, b.Data); err != nil {
				fmt.Fprintf(stderr, "navigara: output %q: %v\n", path, err)
				return 1
			}
			if sender != nil {
				if err := sender.post(b); err != nil {
					fmt.Fprintf(stderr, "navigara: send: %v (re-run to resume: the receiver drops records it has)\n", err)
					return 1
				}
			}
		}
		exported++
		events += len(s.Events)
		points += len(s.Points)
		bodies += len(encoded)
		note := ""
		if s.Estimated {
			note = " cost=estimated"
		}
		reasons := make([]string, 0, len(s.Missing))
		for reason := range s.Missing {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		for _, reason := range reasons {
			note += fmt.Sprintf(" missing[%s]=%d", reason, s.Missing[reason])
		}
		lines = append(lines, fmt.Sprintf("%s %s events=%d points=%d%s", s.Source, s.SessionID, len(s.Events), len(s.Points), note))
	}
	fmt.Fprintf(stdout, "sessions=%d failed=%d events=%d points=%d bodies=%d identity=%s\n",
		exported, failed, events, points, bodies, firstNonEmpty(ident.Email, ident.CodexEmail, "anonymous"))
	for _, line := range lines {
		fmt.Fprintln(stdout, line)
	}
	if sender != nil {
		fmt.Fprintf(stdout, "sent=%d\n", sender.sent)
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

type sender struct {
	client *http.Client
	base   string
	token  string
	sent   int
}

func newSender(url, token string) *sender {
	return &sender{client: &http.Client{Timeout: 60 * time.Second}, base: strings.TrimSuffix(url, "/") + "/otlp", token: token}
}

// post sends one body, retrying throttling and server errors with backoff.
func (s *sender) post(b telemetry.Body) error {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<attempt) * time.Second)
		}
		req, err := http.NewRequest(http.MethodPost, s.base+b.Path, bytes.NewReader(b.Data))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+s.token)
		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK:
			s.sent++
			return nil
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return fmt.Errorf("%s: HTTP %d; the API key needs the analytics ingestor role", b.Path, resp.StatusCode)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s: HTTP %d", b.Path, resp.StatusCode)
		default:
			return fmt.Errorf("%s: HTTP %d", b.Path, resp.StatusCode)
		}
	}
	return lastErr
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

type commit struct {
	at  time.Time
	sha string
}

// commitIndex lists the signed-in user's commits per repository, once, from local refs; a
// repository's worktrees share them, so a commit made in another worktree is found too.
type commitIndex struct {
	email  string
	byRepo map[string][]commit
}

func (c *commitIndex) between(cwd string, from, to time.Time) []string {
	if cwd == "" || c.email == "" {
		return nil
	}
	root, err := exec.Command("git", "-C", cwd, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return nil
	}
	key := strings.TrimSpace(string(root))
	if !filepath.IsAbs(key) {
		key = filepath.Join(cwd, key)
	}
	list, ok := c.byRepo[key]
	if !ok {
		out, err := exec.Command("git", "-C", cwd, "log", "--all", "--reflog",
			"--author="+c.email, "--format=%H %ct").Output()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				sha, ts, ok := strings.Cut(line, " ")
				if secs, err := strconv.ParseInt(ts, 10, 64); ok && err == nil {
					list = append(list, commit{at: time.Unix(secs, 0).UTC(), sha: sha})
				}
			}
		}
		c.byRepo[key] = list
	}
	var shas []string
	seen := map[string]bool{}
	for _, cm := range list {
		if !cm.at.Before(from.Truncate(time.Second)) && !cm.at.After(to) && !seen[cm.sha] {
			seen[cm.sha] = true
			shas = append(shas, cm.sha)
		}
	}
	return shas
}
