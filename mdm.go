package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

type mdmUser struct {
	Name, Home, Shell string
	UID, GID          uint32
}

var (
	mdmGeteuid   = os.Geteuid
	mdmListUsers = listLocalUsers
	// mdmRunChild runs this binary as user with args and a clean env, streaming output to out and errOut.
	mdmRunChild = runAsUser
)

// exitNoEmail is the exit code of config patch when a token is set but no signed-in email exists.
const exitNoEmail = 3

const mdmChildPath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// runMDM configures telemetry and uploads history for every local user, so MDM can run one command as root.
func runMDM(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var host, source string
	var help bool
	fs := flag.NewFlagSet("mdm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "Usage: NAVIGARA_TOKEN=KEY navigara mdm [options]") }
	fs.StringVar(&host, "host", defaultHost, "Navigara origin")
	fs.StringVar(&source, "source", "all", "tool source: all, claude, codex")
	fs.BoolVar(&help, "help", false, "show mdm help")
	fs.BoolVar(&help, "h", false, "show mdm help")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if help {
		fs.SetOutput(stdout)
		fs.Usage()
		fs.PrintDefaults()
		return 0
	}
	invalid := func(message string) int {
		fmt.Fprintln(stderr, "navigara:", message)
		return 2
	}
	if fs.NArg() != 0 {
		return invalid("unexpected argument: " + fs.Arg(0))
	}
	if source != "all" && source != "claude" && source != "codex" {
		return invalid("--source must be all, claude, or codex")
	}
	if !mdmSupported || mdmGeteuid() != 0 {
		return invalid("mdm must run as root; for one user run sessions config patch --apply")
	}
	token := os.Getenv("NAVIGARA_TOKEN")
	if token == "" {
		return invalid("NAVIGARA_TOKEN is not set")
	}
	host, err := canonicalHost(host)
	if err != nil {
		return invalid(errHostFormat.Error())
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	users, err := mdmListUsers()
	if err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	users = filterMDMUsers(users, runtime.GOOS, source)
	if len(users) == 0 {
		fmt.Fprintln(stdout, "No users with Claude Code or Codex data found.")
		return 0
	}

	steps := [][]string{
		{"sessions", "config", "patch", "--host", host, "--apply", "--source", source},
		{"sessions", "upload", "--url", host + "/api", "--source", source},
	}
	ok, skipped := 0, 0
	for _, u := range users {
		env := []string{"HOME=" + u.Home, "USER=" + u.Name, "LOGNAME=" + u.Name, "SHELL=" + u.Shell, "PATH=" + mdmChildPath, "NAVIGARA_TOKEN=" + token}
		out, errOut := newPrefixWriter(stdout, u.Name+": "), newPrefixWriter(stderr, u.Name+": ")
		failed, skip := false, false
		for _, step := range steps {
			err := mdmRunChild(ctx, exe, u, step, env, out, errOut)
			if err == nil {
				continue
			}
			var coded interface{ ExitCode() int }
			if errors.As(err, &coded) && coded.ExitCode() == exitNoEmail {
				fmt.Fprintf(stdout, "%s: skipped: no signed-in Claude Code or Codex account; the next run retries\n", u.Name)
				skip = true
			} else {
				fmt.Fprintf(stdout, "%s: %s failed: %v\n", u.Name, step[1], err)
				failed = true
			}
			break
		}
		out.Flush()
		errOut.Flush()
		switch {
		case skip:
			skipped++
		case !failed:
			ok++
		}
	}
	fmt.Fprintf(stdout, "configured %d of %d users", ok, len(users))
	if skipped > 0 {
		fmt.Fprintf(stdout, ", %d skipped", skipped)
	}
	fmt.Fprintln(stdout)
	if ok+skipped != len(users) {
		return 1
	}
	return 0
}

// filterMDMUsers keeps interactive accounts that already hold data of the selected tools.
func filterMDMUsers(users []mdmUser, goos, source string) []mdmUser {
	minUID := uint32(1000)
	if goos == "darwin" {
		minUID = 500
	}
	var kept []mdmUser
	for _, u := range users {
		// GID 0 is also what a missing or malformed dscl PrimaryGroupID parses to; never hand out wheel.
		if u.UID == 0 || u.GID == 0 || u.UID < minUID || u.Name == "nobody" || u.Home == "" {
			continue
		}
		if strings.HasSuffix(u.Shell, "nologin") || strings.HasSuffix(u.Shell, "/false") {
			continue
		}
		hasClaude := source != "codex" && dirExists(filepath.Join(u.Home, ".claude"))
		hasCodex := source != "claude" && dirExists(filepath.Join(u.Home, ".codex"))
		if !hasClaude && !hasCodex {
			continue
		}
		// The tools reject a symlinked home parent when they plan settings files.
		if real, err := filepath.EvalSymlinks(u.Home); err == nil {
			u.Home = real
		}
		kept = append(kept, u)
	}
	return kept
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func listLocalUsers() ([]mdmUser, error) {
	if runtime.GOOS == "darwin" {
		return listDarwinUsers()
	}
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil, err
	}
	return parsePasswd(string(data)), nil
}

func parsePasswd(data string) []mdmUser {
	var users []mdmUser
	for _, line := range strings.Split(data, "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 {
			continue
		}
		uid, err1 := strconv.ParseUint(f[2], 10, 32)
		gid, err2 := strconv.ParseUint(f[3], 10, 32)
		if err1 != nil || err2 != nil {
			continue
		}
		users = append(users, mdmUser{Name: f[0], UID: uint32(uid), GID: uint32(gid), Home: f[5], Shell: f[6]})
	}
	return users
}

func listDarwinUsers() ([]mdmUser, error) {
	list, err := exec.Command("/usr/bin/dscl", ".", "-list", "/Users", "UniqueID").Output()
	if err != nil {
		return nil, fmt.Errorf("dscl: %w", err)
	}
	var users []mdmUser
	for _, line := range strings.Split(string(list), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		uid, err := strconv.ParseUint(f[1], 10, 32)
		if err != nil || uid < 500 {
			continue
		}
		rec, err := exec.Command("/usr/bin/dscl", ".", "-read", "/Users/"+f[0], "NFSHomeDirectory", "UserShell", "PrimaryGroupID").Output()
		if err != nil {
			continue
		}
		u := parseDsclUser(f[0], uint32(uid), string(rec))
		users = append(users, u)
	}
	return users, nil
}

// parseDsclUser reads dscl -read output; a value with spaces sits on the line after its key.
func parseDsclUser(name string, uid uint32, rec string) mdmUser {
	u := mdmUser{Name: name, UID: uid}
	lines := strings.Split(rec, "\n")
	for i, l := range lines {
		key, val, ok := strings.Cut(l, ":")
		if !ok || strings.HasPrefix(l, " ") {
			continue
		}
		val = strings.TrimSpace(val)
		if val == "" && i+1 < len(lines) {
			val = strings.TrimSpace(lines[i+1])
		}
		switch key {
		case "NFSHomeDirectory":
			u.Home = val
		case "UserShell":
			u.Shell = val
		case "PrimaryGroupID":
			gid, _ := strconv.ParseUint(val, 10, 32)
			u.GID = uint32(gid)
		}
	}
	return u
}

// parseGroupIDs reads `id -G` output into group ids with the primary group first.
func parseGroupIDs(out string, primary uint32) ([]uint32, error) {
	groups := []uint32{primary}
	for _, f := range strings.Fields(out) {
		g, err := strconv.ParseUint(f, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("unexpected id -G output %q", strings.TrimSpace(out))
		}
		if uint32(g) != primary && !slices.Contains(groups, uint32(g)) {
			groups = append(groups, uint32(g))
		}
	}
	return groups, nil
}

// capGroups trims the list to what setgroups accepts; darwin allows 16.
// ponytail: extra groups are dropped; dynamic initgroups-style membership needs cgo.
func capGroups(groups []uint32, goos string) []uint32 {
	if goos == "darwin" && len(groups) > 16 {
		return groups[:16]
	}
	return groups
}

type prefixWriter struct {
	mu     sync.Mutex
	w      io.Writer
	prefix string
	buf    bytes.Buffer
}

func newPrefixWriter(w io.Writer, prefix string) *prefixWriter {
	return &prefixWriter{w: w, prefix: prefix}
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf.Write(b)
	for {
		line, err := p.buf.ReadString('\n')
		if err != nil {
			p.buf.WriteString(line)
			break
		}
		_, _ = fmt.Fprint(p.w, p.prefix+line)
	}
	return len(b), nil
}

// Flush writes a final line that has no newline.
func (p *prefixWriter) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buf.Len() > 0 {
		fmt.Fprintln(p.w, p.prefix+p.buf.String())
		p.buf.Reset()
	}
}
