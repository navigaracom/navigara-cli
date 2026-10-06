package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupFileBackupAndConcurrentEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{"other":1}`), 0644)
	f, err := planFile(path, func([]byte) ([]byte, error) { return []byte(`{"other":1,"env":{}}`), nil })
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(`{"user":"changed"}`), 0600)
	if err := applySetupFiles([]*setupFile{f}, func() error { return nil }); err == nil {
		t.Fatal("overwrote concurrent edit")
	}
	os.WriteFile(path, f.before, 0644)
	if err := applySetupFiles([]*setupFile{f}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	backup, _ := os.ReadFile(f.backup)
	if string(got) != string(f.after) || string(backup) != string(f.before) {
		t.Fatal("replacement or backup changed")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("replacement is not private")
	}
	backupInfo, _ := os.Stat(f.backup)
	if backupInfo.Mode().Perm() != 0644 {
		t.Fatal("backup mode changed")
	}
	backupDir, _ := os.Stat(filepath.Dir(f.backup))
	if backupDir.Mode().Perm() != 0700 {
		t.Fatal("backup directory is not private")
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".navigara-backup-*"))
	f, err = planFile(path, func(b []byte) ([]byte, error) { return b, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles([]*setupFile{f}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	again, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".navigara-backup-*"))
	if len(again) != len(files) {
		t.Fatal("unchanged rerun created another backup")
	}
}

func TestSetupFileRejectsSymlinkAndUnexpectedCreation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("keep"), 0600)
	os.Symlink(target, path)
	transform := func([]byte) ([]byte, error) { return []byte("new"), nil }
	if _, err := planFile(path, transform); err == nil {
		t.Fatal("accepted symlink")
	}
	os.Remove(path)
	f, err := planFile(path, transform)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("user file"), 0600)
	if err := applySetupFiles([]*setupFile{f}, func() error { return nil }); err == nil {
		t.Fatal("replaced new user file")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "keep" {
		t.Fatal("changed symlink target")
	}
}

func TestSetupFileConcurrentFinalInstallPreservesEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte("before"), 0600)
	f, err := planFile(path, func([]byte) ([]byte, error) { return []byte("after"), nil })
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := f.prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp)
	os.WriteFile(path, []byte("user edit"), 0600)
	os.Chmod(path, 0644)
	if err := f.install(tmp); err == nil {
		t.Fatal("installed over concurrent edit")
	}
	got, _ := os.ReadFile(path)
	backup, _ := os.ReadFile(f.backup)
	if string(got) != "user edit" || string(backup) != "user edit" {
		t.Fatal("concurrent edit was not preserved")
	}
	info, _ := os.Stat(f.backup)
	if info.Mode().Perm() != 0644 {
		t.Fatal("backup mode changed concurrent edit")
	}
	targetInfo, _ := os.Stat(path)
	if targetInfo.Mode().Perm() != 0644 {
		t.Fatal("restore changed concurrent edit mode")
	}
	backupDir, _ := os.Stat(filepath.Dir(f.backup))
	if backupDir.Mode().Perm() != 0700 {
		t.Fatal("backup directory is not private")
	}
}

func TestSetupFileCreatesAbsentWithExclusiveInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	f, err := planFile(path, func([]byte) ([]byte, error) { return []byte("new"), nil })
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := f.prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp)
	if err := f.install(tmp); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatal("absent target was not created")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("new file is not private")
	}
}

func TestSetupFileRejectsSymlinkSwappedAfterPreparation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	target := filepath.Join(dir, "target")
	os.WriteFile(path, []byte("before"), 0600)
	os.WriteFile(target, []byte("keep"), 0600)
	f, err := planFile(path, func([]byte) ([]byte, error) { return []byte("after"), nil })
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := f.prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp)
	os.Remove(path)
	os.Symlink(target, path)
	if err := f.install(tmp); err == nil {
		t.Fatal("accepted swapped symlink")
	}
	info, err := os.Lstat(f.backup)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("did not preserve swapped symlink capture")
	}
	backupDir, _ := os.Stat(filepath.Dir(f.backup))
	if backupDir.Mode().Perm() != 0700 {
		t.Fatal("symlink backup directory is not private")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "keep" {
		t.Fatal("changed symlink target")
	}
	restored, err := os.Lstat(path)
	if err != nil || restored.Mode()&os.ModeSymlink == 0 {
		t.Fatal("did not restore swapped symlink")
	}
}

func TestSetupFileRestoresOversizedCapture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte("before"), 0644)
	f, err := planFile(path, func([]byte) ([]byte, error) { return []byte("after"), nil })
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := f.prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp)
	oversized := bytes.Repeat([]byte("x"), 4<<20+1)
	if err := os.WriteFile(path, oversized, 0644); err != nil {
		t.Fatal(err)
	}
	if err := f.install(tmp); err == nil {
		t.Fatal("accepted oversized capture")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, oversized) {
		t.Fatal("did not restore oversized capture")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0644 {
		t.Fatal("restore changed original mode")
	}
	backup, _ := os.Stat(f.backup)
	if backup.Mode().Perm() != 0644 {
		t.Fatal("backup mode changed")
	}
}

func TestSetupFilePreflightLinkFailureDoesNotCaptureOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte("before"), 0600)
	f, err := planFile(path, func([]byte) ([]byte, error) { return []byte("after"), nil })
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := f.prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	os.Remove(tmp)
	os.Mkdir(tmp, 0700)
	if err := f.install(tmp); err == nil {
		t.Fatal("accepted non-linkable staged input")
	}
	if f.backup != "" {
		t.Fatal("captured original before link preflight")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "before" {
		t.Fatal("changed original after failed link preflight")
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".navigara-backup-*"))
	if len(backups) != 0 {
		t.Fatal("left backup directory after failed link preflight")
	}
}
