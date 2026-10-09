package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
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
	return exportSessionsWithProgress(opts, stdout, stderr, nil)
}

// exportSessionsWithProgress reports discovered and inspected sessions without
// affecting export order or output.
func exportSessionsWithProgress(opts options, stdout, stderr io.Writer, progress func(done, total int)) int {
	var sender *sender
	if opts.send {
		if err := prepareUploadAccount(context.Background(), &opts); err != nil {
			fmt.Fprintln(stderr, "navigara:", err)
			return 1
		}
		sender = newSender(opts.url, opts.token)
		if err := sender.validate(); err != nil {
			fmt.Fprintln(stderr, "navigara:", err)
			return 2
		}
		if err := sender.checkSetup(); err != nil {
			fmt.Fprintln(stderr, "navigara:", err)
			return 1
		}
	}
	claudeHome, codexHome := toolHome(opts.claudeHome, ".claude"), toolHome(opts.codexHome, ".codex")
	wantClaude := opts.source == "all" || opts.source == "claude"
	wantCodex := opts.source == "all" || opts.source == "codex"
	for _, tool := range []struct {
		name, home string
		selected   bool
		explicit   bool
	}{
		{"Claude Code", claudeHome, wantClaude, opts.claudeHome != ""},
		{"Codex", codexHome, wantCodex, opts.codexHome != ""},
	} {
		if !tool.selected || !tool.explicit {
			continue
		}
		info, err := os.Stat(tool.home)
		if err != nil {
			fmt.Fprintf(stderr, "navigara: %s home %q: %v\n", tool.name, tool.home, err)
			return 1
		}
		if !info.IsDir() {
			fmt.Fprintf(stderr, "navigara: %s home %q: not a directory\n", tool.name, tool.home)
			return 1
		}
	}
	ident := telemetry.ReadIdentity(claudeHome, codexHome)
	if opts.email != "" {
		ident = ident.WithEmail(opts.email)
	}
	// Each tool needs its own signed-in email: one tool's account says nothing about the other's.
	claudeOK := ident.Email != "" || opts.allowAnonymous
	codexOK := ident.CodexEmail != "" || opts.allowAnonymous
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
	incomplete := false
	if wantClaude {
		scan := session.Scan("claude", opts.claudeHome, opts.codexHome)
		incomplete = len(scan.Errors) > 0
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
				jobs = append(jobs, job{"codex", id, func() (*telemetry.Session, error) {
					return telemetry.BuildCodex(path, ident, opts.until)
				}})
			}
		}
	}
	if incomplete {
		return 1
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].source != jobs[j].source {
			return jobs[i].source < jobs[j].source
		}
		return jobs[i].id < jobs[j].id
	})
	report := func(done int) {
		if progress != nil {
			progress(done, len(jobs))
		}
	}
	report(0)

	if err := os.Mkdir(opts.output, 0700); err != nil {
		fmt.Fprintf(stderr, "navigara: output %q: %v\n", opts.output, err)
		return 1
	}
	// One session at a time: build, write and send it before the next, so memory stays flat.
	counts := map[string]int{}
	exported, failed, events, points, bodies := 0, 0, 0, 0, 0
	estimated, partial := 0, 0
	var lines []string
	for index, j := range jobs {
		s, err := j.build()
		if err != nil {
			failed++
			fmt.Fprintf(stderr, "navigara: %s: %v\n", j.id, err)
			report(index + 1)
			continue
		}
		s.Within(opts.since, opts.until)
		if len(s.Events) == 0 && len(s.Points) == 0 {
			report(index + 1)
			continue
		}
		encoded, err := telemetry.Encode([]*telemetry.Session{s})
		if err != nil {
			fmt.Fprintf(stderr, "navigara: encode %s: %v\n", j.id, err)
			return 1
		}
		for _, b := range encoded {
			data, err := gzipBody(b.Data)
			if err != nil {
				fmt.Fprintf(stderr, "navigara: encode %s: %v\n", j.id, err)
				return 1
			}
			kind := strings.TrimPrefix(b.Path, "/v1/")
			counts[kind]++
			path := filepath.Join(opts.output, fmt.Sprintf("%s-%06d.json.gz", kind, counts[kind]))
			if err := writeRequest(path, data); err != nil {
				fmt.Fprintf(stderr, "navigara: output %q: %v\n", path, err)
				return 1
			}
			if sender != nil {
				if err := sender.post(telemetry.Body{Path: b.Path, Data: data}); err != nil {
					fmt.Fprintf(stderr, "navigara: send: %v (re-run with a new --output to resume: the receiver drops records it already has)\n", err)
					return 1
				}
			}
		}
		exported++
		events += len(s.Events)
		points += len(s.Points)
		bodies += len(encoded)
		if s.Estimated {
			estimated++
		}
		if len(s.Missing) > 0 {
			partial++
		}
		report(index + 1)
		if opts.humanSummary {
			continue
		}
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
	if opts.humanSummary {
		fmt.Fprintf(stdout, "%d sessions ready to import (%d activity events, %d usage measurements).\n", exported, events, points)
		if estimated > 0 || partial > 0 {
			fmt.Fprintf(stdout, "%d sessions have estimated costs; %d have fields unavailable in local history.\n", estimated, partial)
		}
	} else {
		fmt.Fprintf(stdout, "sessions=%d failed=%d events=%d points=%d bodies=%d identity=%s\n",
			exported, failed, events, points, bodies, firstNonEmpty(ident.Email, ident.CodexEmail, "anonymous"))
		for _, line := range lines {
			fmt.Fprintln(stdout, line)
		}
	}
	if sender != nil {
		fmt.Fprintf(stdout, "sent=%d\n", sender.sent)
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

// gzipBody compresses one OTLP body: the repeated attributes shrink it about 40 times.
func gzipBody(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
	at time.Time
	telemetry.Commit
}

// commitIndex lists the signed-in user's commits per repository, once, from local refs; a
// repository's worktrees share them, so a commit made in another worktree is found too. The reflog
// keeps commits of deleted branches; Reachable tells an amended commit from its replacement.
type commitIndex struct {
	email  string
	byRepo map[string][]commit
}

func (c *commitIndex) between(cwd string, from, to time.Time) []telemetry.Commit {
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
		reachable := map[string]bool{}
		if out, err := exec.Command("git", "-C", cwd, "rev-list", "--all").Output(); err == nil {
			for _, sha := range strings.Fields(string(out)) {
				reachable[sha] = true
			}
		}
		out, err := exec.Command("git", "-C", cwd, "log", "--all", "--reflog", "--format=%H%x1f%ct%x1f%ae%x1f%s").Output()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				fields := strings.Split(line, "\x1f")
				if len(fields) != 4 || !strings.EqualFold(fields[2], c.email) {
					continue
				}
				if secs, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					list = append(list, commit{at: time.Unix(secs, 0).UTC(),
						Commit: telemetry.Commit{SHA: fields[0], Subject: fields[3], Reachable: reachable[fields[0]]}})
				}
			}
		}
		c.byRepo[key] = list
	}
	var found []telemetry.Commit
	seen := map[string]bool{}
	for _, cm := range list {
		if !cm.at.Before(from.Truncate(time.Second)) && !cm.at.After(to) && !seen[cm.SHA] {
			seen[cm.SHA] = true
			found = append(found, cm.Commit)
		}
	}
	return found
}
