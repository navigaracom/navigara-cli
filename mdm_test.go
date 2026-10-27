package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func mdmHome(t *testing.T, dir string) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestFilterMDMUsers(t *testing.T) {
	claude, codex, empty := mdmHome(t, ".claude"), mdmHome(t, ".codex"), mdmHome(t, ".config")
	users := []mdmUser{
		{"alice", claude, "/bin/zsh", 501, 20},
		{"bob", codex, "/bin/bash", 1001, 1001},
		{"root", claude, "/bin/zsh", 0, 0},
		{"nobody", claude, "/bin/zsh", 4000, 4000},
		{"svc", claude, "/usr/sbin/nologin", 1002, 1002},
		{"ftp", claude, "/usr/bin/false", 1003, 1003},
		{"carol", empty, "/bin/zsh", 1004, 1004},
		{"daemon", claude, "/bin/zsh", 1, 1},
		{"eve", claude, "/bin/zsh", 1005, 0},
	}
	names := func(us []mdmUser) string {
		var n []string
		for _, u := range us {
			n = append(n, u.Name)
		}
		return strings.Join(n, ",")
	}
	if got := names(filterMDMUsers(users, "darwin", "all")); got != "alice,bob" {
		t.Errorf("darwin: %s", got)
	}
	if got := names(filterMDMUsers(users, "linux", "all")); got != "bob" {
		t.Errorf("linux: %s", got)
	}
}

func TestParsePasswd(t *testing.T) {
	got := parsePasswd("alice:x:1000:1000:Alice:/home/alice:/bin/bash\nbroken\nbad:x:z:1:::\n")
	if len(got) != 1 || got[0] != (mdmUser{"alice", "/home/alice", "/bin/bash", 1000, 1000}) {
		t.Errorf("got %+v", got)
	}
}

func stubMDM(t *testing.T, euid int, users []mdmUser, runner func(u mdmUser, args, env []string) error) {
	t.Helper()
	if euid == 0 && !mdmSupported {
		t.Skip("mdm runs only on macOS and Linux")
	}
	oldE, oldL, oldR := mdmGeteuid, mdmListUsers, mdmRunChild
	t.Cleanup(func() { mdmGeteuid, mdmListUsers, mdmRunChild = oldE, oldL, oldR })
	mdmGeteuid = func() int { return euid }
	mdmListUsers = func() ([]mdmUser, error) { return users, nil }
	mdmRunChild = func(_ context.Context, _ string, u mdmUser, args, env []string, out, errOut io.Writer) error {
		return runner(u, args, env)
	}
}

func TestRunMDMRefusals(t *testing.T) {
	stubMDM(t, 501, nil, nil)
	t.Setenv("NAVIGARA_TOKEN", "secret")
	var stderr bytes.Buffer
	if code := runMDM(context.Background(), nil, io.Discard, &stderr); code != 2 || !strings.Contains(stderr.String(), "must run as root") {
		t.Errorf("non-root: code %d, %q", code, stderr.String())
	}
	stubMDM(t, 0, nil, nil)
	t.Setenv("NAVIGARA_TOKEN", "")
	if code := runMDM(context.Background(), nil, io.Discard, io.Discard); code != 2 {
		t.Errorf("missing token: code %d", code)
	}
}

func TestRunMDMOneFailureKeepsGoing(t *testing.T) {
	a, b, c := mdmHome(t, ".claude"), mdmHome(t, ".claude"), mdmHome(t, ".codex")
	users := []mdmUser{{"a", a, "/bin/zsh", 1001, 1001}, {"b", b, "/bin/zsh", 1002, 1002}, {"c", c, "/bin/zsh", 1003, 1003}}
	t.Setenv("NAVIGARA_TOKEN", "secret")
	ran := map[string][]string{}
	stubMDM(t, 0, users, func(u mdmUser, args, env []string) error {
		if strings.Contains(strings.Join(args, " "), "secret") {
			t.Errorf("token in argv: %v", args)
		}
		ran[u.Name] = append(ran[u.Name], args[1])
		if u.Name == "b" && args[1] == "config" {
			return errors.New("boom")
		}
		return nil
	})
	var stdout bytes.Buffer
	code := runMDM(context.Background(), []string{"--host", "https://x.example.com"}, &stdout, io.Discard)
	aSteps := strings.Join(ran["a"], ",")
	if code != 1 || !strings.Contains(stdout.String(), "configured 2 of 3 users") {
		t.Errorf("code %d, out %q", code, stdout.String())
	}
	if aSteps != "config,upload" || strings.Join(ran["b"], ",") != "config" || strings.Join(ran["c"], ",") != "config,upload" {
		t.Errorf("steps: %v", ran)
	}
}

func TestRunMDMStepArgs(t *testing.T) {
	home, _ := filepath.EvalSymlinks(mdmHome(t, ".claude"))
	t.Setenv("NAVIGARA_TOKEN", "secret")
	var calls [][]string
	var env []string
	stubMDM(t, 0, []mdmUser{{"a", home, "/bin/zsh", 1001, 1001}}, func(u mdmUser, args, e []string) error {
		calls, env = append(calls, args), e
		return nil
	})
	if code := runMDM(context.Background(), []string{"--host", "https://x.example.com"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("code %d", code)
	}
	if got := strings.Join(calls[1], " "); got != "sessions upload --url https://x.example.com/api --source all" {
		t.Errorf("upload: %s", got)
	}
	if !strings.Contains(strings.Join(env, "\n"), "NAVIGARA_TOKEN=secret") || !strings.Contains(strings.Join(env, "\n"), "HOME="+home) {
		t.Errorf("env: %v", env)
	}
}

func TestPrefixWriter(t *testing.T) {
	var out bytes.Buffer
	w := newPrefixWriter(&out, "u: ")
	_, _ = w.Write([]byte("one\ntw"))
	_, _ = w.Write([]byte("o\nthree"))
	w.Flush()
	if out.String() != "u: one\nu: two\nu: three\n" {
		t.Errorf("got %q", out.String())
	}
}

func TestFilterMDMUsersSourceAndSymlink(t *testing.T) {
	claude, codex := mdmHome(t, ".claude"), mdmHome(t, ".codex")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(claude, link); err != nil {
		t.Fatal(err)
	}
	users := []mdmUser{{"a", link, "/bin/zsh", 1001, 1}, {"b", codex, "/bin/zsh", 1002, 1}}
	got := filterMDMUsers(users, "linux", "codex")
	if len(got) != 1 || got[0].Name != "b" {
		t.Errorf("codex: %+v", got)
	}
	got = filterMDMUsers(users, "linux", "claude")
	want, _ := filepath.EvalSymlinks(claude)
	if len(got) != 1 || got[0].Name != "a" || got[0].Home != want {
		t.Errorf("claude: %+v want home %s", got, want)
	}
}

func TestParseDsclUser(t *testing.T) {
	rec := "NFSHomeDirectory:\n /Users/Jane Doe\nPrimaryGroupID: 20\nUserShell: /bin/zsh\n"
	if got := parseDsclUser("jane", 501, rec); got != (mdmUser{"jane", "/Users/Jane Doe", "/bin/zsh", 501, 20}) {
		t.Errorf("got %+v", got)
	}
}

type codedErr int

func (c codedErr) Error() string { return "exit" }
func (c codedErr) ExitCode() int { return int(c) }

func TestRunMDMNoEmailIsSkipNotFailure(t *testing.T) {
	a, b := mdmHome(t, ".claude"), mdmHome(t, ".claude")
	users := []mdmUser{{"a", a, "/bin/zsh", 1001, 1001}, {"b", b, "/bin/zsh", 1002, 1002}}
	t.Setenv("NAVIGARA_TOKEN", "secret")
	var ranA []string
	stubMDM(t, 0, users, func(u mdmUser, args, env []string) error {
		if u.Name == "a" {
			ranA = append(ranA, args[1])
			return codedErr(exitNoEmail)
		}
		return nil
	})
	var stdout bytes.Buffer
	code := runMDM(context.Background(), nil, &stdout, io.Discard)
	if code != 0 || !strings.Contains(stdout.String(), "configured 1 of 2 users, 1 skipped") || !strings.Contains(stdout.String(), "a: skipped: no signed-in") {
		t.Errorf("code %d, out %q", code, stdout.String())
	}
	if len(ranA) != 1 {
		t.Errorf("upload must not run after skip: %v", ranA)
	}
}

func TestParseGroupIDs(t *testing.T) {
	got, err := parseGroupIDs("12 20 61 12 80\n", 20)
	if err != nil || !slices.Equal(got, []uint32{20, 12, 61, 80}) {
		t.Errorf("got %v, %v", got, err)
	}
	if _, err := parseGroupIDs("id: no such user", 20); err == nil {
		t.Error("want error for non-numeric output")
	}
}

func TestCapGroups(t *testing.T) {
	var many []uint32
	for i := uint32(0); i < 20; i++ {
		many = append(many, i)
	}
	if got := capGroups(many, "darwin"); len(got) != 16 || got[0] != 0 || got[15] != 15 {
		t.Errorf("darwin: %v", got)
	}
	if got := capGroups(many, "linux"); len(got) != 20 {
		t.Errorf("linux: %v", got)
	}
}
