package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Prepare every replacement before checking the receiver or changing any target.
func applySetupFiles(files []*setupFile, preflight func() error) (err error) {
	prepared := make([]string, len(files))
	var installed []*setupFile
	defer func() {
		for _, path := range prepared {
			if path != "" {
				_ = os.Remove(path)
			}
		}
		if err != nil {
			err = errors.Join(err, rollbackSetupFiles(installed))
		}
	}()
	for i, f := range files {
		prepared[i], err = f.prepare()
		if err != nil {
			return err
		}
	}
	if err = preflight(); err != nil {
		return err
	}
	for i, f := range files {
		if err = f.check(); err != nil {
			return err
		}
		if f.existed && bytes.Equal(f.before, f.after) {
			if info, e := os.Lstat(f.path); e == nil && info.Mode().Perm() == 0600 {
				continue
			}
		}
		if err = f.install(prepared[i]); err != nil {
			return err
		}
		installed = append(installed, f)
	}
	return nil
}

func rollbackSetupFiles(files []*setupFile) error {
	var result error
	for i := len(files) - 1; i >= 0; i-- {
		result = errors.Join(result, files[i].rollback())
	}
	return result
}

// Capture before inspecting: a concurrent replacement is never deleted based
// on a prior read. Restoration only fills an absent path, just like install.
func (f *setupFile) rollback() error {
	dir, err := os.MkdirTemp(filepath.Dir(f.path), ".navigara-rollback-*")
	if err != nil {
		return fmt.Errorf("rollback %s: %w; original backup: %s", f.path, err, f.backup)
	}
	captured := filepath.Join(dir, filepath.Base(f.path))
	if err = os.Rename(f.path, captured); err != nil {
		_ = os.Remove(dir)
		return fmt.Errorf("rollback %s: %w; original backup: %s", f.path, err, f.backup)
	}
	capturedInfo, statErr := os.Lstat(captured)
	data, _, readErr := readSetupFile(captured)
	if statErr != nil || f.installed == nil || !os.SameFile(f.installed, capturedInfo) || f.installed.Mode() != capturedInfo.Mode() || readErr != nil || !bytes.Equal(data, f.after) {
		restored, restoreErr := restoreIfAbsent(captured, f.path)
		if restored {
			_ = os.RemoveAll(dir)
		}
		return fmt.Errorf("rollback %s: concurrent change retained (capture %s, original backup %s): %w", f.path, captured, f.backup, errors.Join(statErr, readErr, restoreErr, errors.New("file changed during update")))
	}
	if f.existed {
		restored, restoreErr := restoreIfAbsent(f.backup, f.path)
		if restoreErr != nil || !restored {
			return fmt.Errorf("rollback %s: original preserved at %s; replacement preserved at %s: %w", f.path, f.backup, captured, errors.Join(restoreErr, errors.New("could not restore original without replacing a newer file")))
		}
	}
	return os.RemoveAll(dir)
}
