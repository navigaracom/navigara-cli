package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func scanTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func putHistory(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestScanLayoutsAndSelection(t *testing.T) {
	home := filepath.Join(scanTempDir(t), "literal [home] with spaces")
	claude, codex := filepath.Join(home, ".claude"), filepath.Join(home, ".codex")
	paths := []string{
		filepath.Join(claude, "projects", "project", "a.jsonl"),
		filepath.Join(codex, "sessions", "2026", "b.jsonl"),
		filepath.Join(codex, "archived_sessions", "c.jsonl"),
	}
	putHistory(t, paths[0], fixture(t, "claude-root"))
	for _, path := range paths[1:] {
		putHistory(t, path, fixture(t, "codex-root"))
	}
	putHistory(t, filepath.Join(claude, "projects", "project", "child.jsonl"), fixture(t, "claude-child"))
	putHistory(t, filepath.Join(codex, "sessions", "child.jsonl"), fixture(t, "codex-child"))
	for _, path := range []string{"projects/direct.jsonl", "projects/project/agent-hidden.jsonl", "projects/project/subagents/deep.jsonl", "projects/project/ignored.txt"} {
		putHistory(t, filepath.Join(claude, path), "invalid excluded file")
	}
	for _, tc := range []struct {
		source, claude, codex string
		files, skipped        int
		paths                 []string
	}{
		{"all", claude, codex, 5, 2, paths},
		{"claude", claude, "missing unselected", 2, 1, paths[:1]},
		{"codex", "missing unselected", codex, 3, 1, paths[1:]},
	} {
		t.Run(tc.source, func(t *testing.T) {
			got := Scan(tc.source, tc.claude, tc.codex, nil)
			var actual []string
			for _, record := range got.Records {
				actual = append(actual, record.Path)
			}
			if len(got.Errors) != 0 || got.Files != tc.files || got.Skipped != tc.skipped || !reflect.DeepEqual(actual, tc.paths) {
				t.Fatalf("got %+v, paths %v; want %v", got, actual, tc.paths)
			}
		})
	}
}

func TestScanRootPolicies(t *testing.T) {
	home := scanTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	missing := filepath.Join(home, "missing")
	file := filepath.Join(home, "file")
	putHistory(t, file, "")
	for _, tc := range []struct {
		source, claude, codex string
		errors                int
	}{
		{"all", "", "", 0},
		{"claude", missing, "", 1},
		{"codex", "", missing, 1},
		{"claude", file, "", 1},
		{"all", home, home, 0},
		{"unknown", missing, file, 1},
	} {
		got := Scan(tc.source, tc.claude, tc.codex, nil)
		if got.Records == nil || got.Files != 0 || len(got.Records) != 0 || got.Skipped != 0 || len(got.Errors) != tc.errors {
			t.Fatalf("%+v: got %+v", tc, got)
		}
	}
	putHistory(t, filepath.Join(home, "sessions", "valid.jsonl"), fixture(t, "codex-root"))
	putHistory(t, filepath.Join(home, "archived_sessions"), "not a directory")
	putHistory(t, filepath.Join(home, "projects"), "not a directory")
	got := Scan("all", home, home, nil)
	if got.Files != 1 || len(got.Records) != 1 || len(got.Errors) != 2 {
		t.Fatalf("root diagnostics must preserve other roots: %+v", got)
	}
}

func TestScanSymlinksAndNonregular(t *testing.T) {
	home, storage := scanTempDir(t), scanTempDir(t)
	root := filepath.Join(storage, "histories")
	putHistory(t, filepath.Join(root, "valid.jsonl"), fixture(t, "codex-root"))
	if err := os.Symlink(root, filepath.Join(home, "sessions")); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"link.jsonl": filepath.Join(root, "valid.jsonl"), "linked-dir": storage} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	if err := exec.Command("mkfifo", filepath.Join(root, "pipe.jsonl")).Run(); err != nil {
		t.Logf("FIFO unavailable: %v", err)
	}
	got := Scan("codex", "", home, nil)
	if got.Files != 1 || len(got.Records) != 1 || len(got.Errors) != 0 || got.Records[0].Path != filepath.Join(root, "valid.jsonl") {
		t.Fatalf("got %+v", got)
	}
}

func TestScanFailureKeepsOtherFilesAndQuotesPaths(t *testing.T) {
	home := scanTempDir(t)
	bad := filepath.Join(home, "sessions", "bad\n\x1b.jsonl")
	putHistory(t, bad, fixture(t, "codex-root")+"{private malformed tail")
	putHistory(t, filepath.Join(home, "sessions", "good.jsonl"), fixture(t, "codex-root"))
	got := Scan("codex", "", home, nil)
	if got.Files != 2 || len(got.Records) != 1 || got.Skipped != 0 || len(got.Errors) != 1 {
		t.Fatalf("got %+v", got)
	}
	message := got.Errors[0].Error()
	if !strings.Contains(message, fmt.Sprintf("%q", bad)) || strings.ContainsAny(message, "\n\x1b") || strings.Contains(message, "private malformed tail") {
		t.Fatalf("unsafe diagnostic: %q", message)
	}
	err := pathDiagnostic(bad, &os.PathError{Op: "outer", Path: bad, Err: &os.PathError{Op: "inner", Path: bad, Err: errors.New("denied\n\x1b")}})
	if strings.Count(err.Error(), fmt.Sprintf("%q", bad)) != 1 || strings.ContainsAny(err.Error(), "\n\x1b") {
		t.Fatalf("unsafe nested diagnostic: %q", err)
	}
}

func TestScanPermissionFailures(t *testing.T) {
	home := scanTempDir(t)
	file := filepath.Join(home, "sessions", "denied.jsonl")
	putHistory(t, file, fixture(t, "codex-root"))
	dir := filepath.Join(home, "sessions", "denied-dir")
	putHistory(t, filepath.Join(dir, "hidden.jsonl"), fixture(t, "codex-root"))
	putHistory(t, filepath.Join(home, "sessions", "valid.jsonl"), fixture(t, "codex-root"))
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(file, 0600); os.Chmod(dir, 0700) })
	if f, err := os.Open(file); err == nil {
		f.Close()
		t.Skip("OS does not enforce permissions for this user")
	}
	got := Scan("codex", "", home, nil)
	if got.Files != 2 || len(got.Records) != 1 || got.Skipped != 0 || len(got.Errors) != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestReadSnapshotExcludesAppendedRecords(t *testing.T) {
	path := filepath.Join(scanTempDir(t), "history.jsonl")
	putHistory(t, path, fixture(t, "codex-root"))
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	appendFile, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendFile.WriteString("\n" + `{"timestamp":"2026-12-01T00:00:00Z"}`); err != nil {
		t.Fatal(err)
	}
	if err := appendFile.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := readSnapshot("codex", f, info.Size())
	if err != nil || got.Session.LastObservedAt == nil || got.Session.LastObservedAt.Format("2006-01-02T15:04:05Z") != "2026-01-02T00:02:00Z" {
		t.Fatalf("snapshot included append: %+v, %v", got, err)
	}
}
