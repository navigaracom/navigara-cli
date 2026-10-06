package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func transactionFile(t *testing.T, path string, after []byte) *setupFile {
	t.Helper()
	f, err := planFile(path, func([]byte) ([]byte, error) { return after, nil })
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestApplySetupFilesPreflightDoesNotChangeTargets(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dir, "created")
	files := []*setupFile{transactionFile(t, existing, []byte("after")), transactionFile(t, created, []byte("new"))}
	if err := applySetupFiles(files, func() error { return errors.New("receiver rejected") }); err == nil {
		t.Fatal("preflight failure succeeded")
	}
	if got, _ := os.ReadFile(existing); !bytes.Equal(got, []byte("before")) {
		t.Fatalf("existing changed to %q", got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("new target exists: %v", err)
	}
}

func TestApplySetupFilesRollsBackFirstTarget(t *testing.T) {
	for _, existed := range []bool{true, false} {
		t.Run(map[bool]string{true: "existing", false: "new"}[existed], func(t *testing.T) {
			dir := t.TempDir()
			first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
			if existed {
				if err := os.WriteFile(first, []byte("before"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(first, 0640); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(second, []byte("second-before"), 0600); err != nil {
				t.Fatal(err)
			}
			firstFile, secondFile := transactionFile(t, first, []byte("after")), transactionFile(t, second, []byte("second-after"))
			err := applySetupFiles([]*setupFile{firstFile, secondFile}, func() error {
				return os.WriteFile(second, []byte("concurrent"), 0600)
			})
			if err == nil {
				t.Fatal("concurrent second target accepted")
			}
			if existed {
				info, statErr := os.Stat(first)
				got, readErr := os.ReadFile(first)
				if statErr != nil || readErr != nil || !bytes.Equal(got, []byte("before")) || info.Mode().Perm() != 0640 {
					t.Fatalf("original not restored: %q, %v, %v", got, info, errors.Join(statErr, readErr))
				}
				backup, backupErr := os.ReadFile(firstFile.backup)
				if firstFile.backup == "" || backupErr != nil || !bytes.Equal(backup, []byte("before")) {
					t.Fatalf("original backup missing: %q, %v", backup, backupErr)
				}
			} else if _, statErr := os.Stat(first); !os.IsNotExist(statErr) {
				t.Fatalf("new first target remained: %v", statErr)
			}
		})
	}
}

func TestApplySetupFilesLeavesUnchangedPrivateTarget(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	if err := os.WriteFile(first, []byte("same"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	firstFile, secondFile := transactionFile(t, first, []byte("same")), transactionFile(t, second, []byte("after"))
	if err := applySetupFiles([]*setupFile{firstFile, secondFile}, func() error { return os.WriteFile(second, []byte("concurrent"), 0600) }); err == nil {
		t.Fatal("concurrent second target accepted")
	}
	if got, _ := os.ReadFile(first); !bytes.Equal(got, []byte("same")) || firstFile.backup != "" {
		t.Fatalf("unchanged private target was touched: %q, backup %q", got, firstFile.backup)
	}
}

func TestApplySetupFilesLeavesUnchangedPrivateTargetInReadOnlyDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("same"), 0600); err != nil {
		t.Fatal(err)
	}
	f := transactionFile(t, path, []byte("same"))
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	if err := applySetupFiles([]*setupFile{f}, func() error { return nil }); err != nil {
		t.Fatalf("unchanged private target required a temporary file: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".navigara-new-*")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary replacements = %v, err = %v", matches, err)
	}
}

func TestApplySetupFilesTightensUnchangedTargetMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("same"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles([]*setupFile{transactionFile(t, path, []byte("same"))}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("target mode = %v, want 0600", info.Mode())
	}
}

func TestApplySetupFilesRejectsModeChangeForSkippedTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("same"), 0600); err != nil {
		t.Fatal(err)
	}
	f := transactionFile(t, path, []byte("same"))
	if err := applySetupFiles([]*setupFile{f}, func() error { return os.Chmod(path, 0644) }); err == nil {
		t.Fatal("accepted concurrent mode change")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 || f.backup != "" {
		t.Fatalf("mode change was not preserved: mode=%v backup=%q", info.Mode(), f.backup)
	}
}

func TestRollbackSetupFilePreservesSameContentReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	f := transactionFile(t, path, []byte("after"))
	tmp, err := f.prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp)
	if err := f.install(tmp); err != nil {
		t.Fatal(err)
	}
	replacement, err := privateFile(dir, "replacement-*", []byte("after"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || os.SameFile(f.installed, info) {
		t.Fatalf("replacement did not get a distinct identity: %v", err)
	}
	if err := f.rollback(); err == nil {
		t.Fatal("same-content replacement rollback succeeded")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, []byte("after")) {
		t.Fatalf("same-content replacement was not retained: %q", got)
	}
}
