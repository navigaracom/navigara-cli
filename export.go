package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	wantClaude := opts.source == "all" || opts.source == "claude"
	wantCodex := opts.source == "all" || opts.source == "codex"
	claudeHome, codexHome, err := toolHomes(opts.claudeHome, opts.codexHome)
	if (wantClaude && claudeHome == "") || (wantCodex && codexHome == "") {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
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
	// A missing $CLAUDE_CONFIG_DIR or $CODEX_HOME is reported before anything else: history the
	// user expects there is not read, and no email can fix that.
	for _, tool := range []struct {
		name, home, flag, env string
		want                  *bool
	}{
		{"Claude Code", claudeHome, opts.claudeHome, "CLAUDE_CONFIG_DIR", &wantClaude},
		{"Codex", codexHome, opts.codexHome, "CODEX_HOME", &wantCodex},
	} {
		source := homeSource(tool.flag, tool.env)
		if !*tool.want || source == "" {
			continue
		}
		if _, err := os.Stat(tool.home); errors.Is(err, fs.ErrNotExist) {
			*tool.want = false
			fmt.Fprintf(stderr, "navigara: skipping %s: home %q%s does not exist\n", tool.name, tool.home, source)
		}
	}
	ident := telemetry.ReadIdentity(claudeHome, codexHome)
	if opts.email != "" {
		ident = ident.WithEmail(opts.email)
	}
	// Each tool needs its own signed-in email: one tool's account says nothing about the other's.
	claudeOK := ident.Email != "" || opts.allowAnonymous
	codexOK := ident.CodexEmail != "" || opts.allowAnonymous
	if (wantClaude || wantCodex) && (!wantClaude || !claudeOK) && (!wantCodex || !codexOK) {
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
	var reposMu, commitsMu sync.Mutex
	lookupRepo := func(cwd string) string {
		reposMu.Lock()
		defer reposMu.Unlock()
		return repos(cwd)
	}
	lookupCommits := func(repo string, from, to time.Time) []telemetry.Commit {
		commitsMu.Lock()
		defer commitsMu.Unlock()
		return commits.between(repo, from, to)
	}
	type job struct {
		source, id string
		build      func() (*telemetry.Session, error)
	}
	var jobs []job
	incomplete := false
	// A missing default home has no history to read.
	if _, err := os.Stat(claudeHome); wantClaude && opts.claudeHome == "" && errors.Is(err, fs.ErrNotExist) {
		wantClaude = false
	}
	if wantClaude {
		scan := session.Scan("claude", claudeHome, codexHome, func(path string) bool {
			return stale(opts.since, append([]string{path}, telemetry.ClaudeSubagents(path)...)...)
		})
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
					Until: opts.until, Commits: lookupCommits}, ident, lookupRepo)
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
				if stale(opts.since, path) {
					continue
				}
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
	// Prepare at most two sessions at a time, then encode, write and send their
	// bodies serially in sorted session order.
	counts := map[string]int{}
	exported, failed, events, points, bodies := 0, 0, 0, 0, 0
	compressor := gzip.NewWriter(io.Discard)
	defer compressor.Close()
	estimated, partial := 0, 0
	var lines []string
	batcher := telemetry.NewBatcher(func(b telemetry.Body) error {
		var compressed bytes.Buffer
		compressor.Reset(&compressed)
		if _, err := compressor.Write(b.Data); err != nil {
			return fmt.Errorf("encode: %w", err)
		}
		if err := compressor.Close(); err != nil {
			return fmt.Errorf("encode: %w", err)
		}
		data := compressed.Bytes()
		kind := strings.TrimPrefix(b.Path, "/v1/")
		counts[kind]++
		bodies++
		path := filepath.Join(opts.output, fmt.Sprintf("%s-%06d.json.gz", kind, counts[kind]))
		if err := writeRequest(path, data); err != nil {
			return fmt.Errorf("output %q: %w", path, err)
		}
		if sender != nil {
			if err := sender.post(telemetry.Body{Path: b.Path, Data: data}); err != nil {
				return fmt.Errorf("send: %w (re-run with a new --output to resume: the receiver drops records it already has)", err)
			}
		}
		return nil
	})
	for index := 0; index < len(jobs); index += 2 {
		pairSize := 2
		if remaining := len(jobs) - index; remaining < pairSize {
			pairSize = remaining
		}
		prepared := make([]struct {
			s   *telemetry.Session
			err error
		}, pairSize)
		var wg sync.WaitGroup
		wg.Add(pairSize)
		for pairIndex := 0; pairIndex < pairSize; pairIndex++ {
			go func(pairIndex int) {
				defer wg.Done()
				prepared[pairIndex].s, prepared[pairIndex].err = jobs[index+pairIndex].build()
			}(pairIndex)
		}
		wg.Wait()
		for pairIndex := 0; pairIndex < pairSize; pairIndex++ {
			j := jobs[index+pairIndex]
			s, err := prepared[pairIndex].s, prepared[pairIndex].err
			if err != nil {
				failed++
				fmt.Fprintf(stderr, "navigara: %s: %v\n", j.id, err)
				report(index + pairIndex + 1)
				continue
			}
			s.Within(opts.since, opts.until)
			if len(s.Events) == 0 && len(s.Points) == 0 {
				report(index + pairIndex + 1)
				continue
			}
			err = telemetry.Encode(s, batcher.Add)
			if err != nil {
				fmt.Fprintf(stderr, "navigara: %s: %v\n", j.id, err)
				return 1
			}
			exported++
			events += len(s.Events)
			points += len(s.Points)
			if s.Estimated {
				estimated++
			}
			if len(s.Missing) > 0 {
				partial++
			}
			report(index + pairIndex + 1)
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
	}
	if err := batcher.Flush(); err != nil {
		fmt.Fprintf(stderr, "navigara: %v\n", err)
		return 1
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

// staleMargin covers coarse file times (1 s on HFS+) and the points a builder stamps just after
// the last record, such as cost-state residue at the session end plus 1 ms.
const staleMargin = time.Minute

// stale reports whether every file ends well before since, so none can hold a record in the
// window. Skipping them keeps a short --last from reading the whole history. A file counts only
// when both its write time and its newest tail record are old: a network file system can stamp
// write times by a clock that differs from the tool's.
func stale(since time.Time, paths ...string) bool {
	if since.IsZero() {
		return false
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.ModTime().Add(staleMargin).Before(since) {
			return false
		}
		if last := lastRecordTime(path); last.IsZero() || !last.Add(staleMargin).Before(since) {
			return false
		}
	}
	return true
}

// lastRecordTime is the newest record time in the last 64 KiB of a JSONL history, counting a
// Claude Code cost-state row's process start. It is zero when that tail holds no complete
// timestamped record, for example inside one very long record.
func lastRecordTime(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return time.Time{}
	}
	start := max(info.Size()-64<<10, 0)
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return time.Time{}
	}
	lines := bytes.Split(buf, []byte("\n"))
	if start > 0 {
		lines = lines[1:]
	}
	var last time.Time
	for _, line := range lines {
		var row struct {
			Timestamp string `json:"timestamp"`
			StartTime int64  `json:"startTime"`
		}
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, row.Timestamp); err == nil && t.After(last) {
			last = t
		}
		if t := time.UnixMilli(row.StartTime); row.StartTime > 0 && t.After(last) {
			last = t
		}
	}
	return last
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
