package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The records of a user variable that setup changes: the enduser.id that setup wrote (no suffix),
// the user's own one that it replaced, and the machine value that setup copied when it created
// the variable.
const (
	previousSuffix = "_PREVIOUS"
	createdSuffix  = "_CREATED"
)

// Prepare every replacement before checking the receiver or changing any target.
func applySetupFiles(files []*setupFile, preflight func() error) (err error) {
	prepared := make([]string, len(files))
	var installed []*setupFile
	defer func() {
		// Roll back first: on Windows, os.SameFile reads the file ID of a prepared path.
		if err != nil {
			err = errors.Join(err, rollbackSetupFiles(installed))
		}
		for _, path := range prepared {
			if path != "" {
				_ = os.Remove(path)
			}
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
		if f.env != "" {
			continue
		}
		if prepared[i] == "" {
			info, e := os.Lstat(f.path)
			if e != nil {
				return e
			}
			if privateMode(info, 0600) {
				continue
			}
			return fmt.Errorf("%s changed during setup; run setup again", f.path)
		}
		if err = f.install(prepared[i]); err != nil {
			return err
		}
		installed = append(installed, f)
	}
	for _, f := range files {
		if f.env != "" {
			if err = applyEnv(f); err != nil {
				return err
			}
		}
	}
	collectBackups(installed)
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
		changed := errors.Join(statErr, readErr, errors.New("file changed during update"))
		restored, restoreErr := restoreIfAbsent(captured, f.path)
		if restored {
			_ = os.RemoveAll(dir)
			return fmt.Errorf("rollback %s: concurrent change kept in place (original backup %s): %w", f.path, f.backup, changed)
		}
		return fmt.Errorf("rollback %s: concurrent change retained (capture %s, original backup %s): %w", f.path, captured, f.backup, errors.Join(changed, restoreErr))
	}
	if f.existed {
		restored, restoreErr := restoreIfAbsent(f.backup, f.path)
		if restoreErr != nil || !restored {
			return fmt.Errorf("rollback %s: original preserved at %s; replacement preserved at %s: %w", f.path, f.backup, captured, errors.Join(restoreErr, errors.New("could not restore original without replacing a newer file")))
		}
	}
	return os.RemoveAll(dir)
}

// applyEnv saves the user variable of f and its records. Setup records the email before the value
// and remove drops the records after it, so remove and status never treat an email without a
// record as ours. On failure it restores the value and the records.
func applyEnv(f *setupFile) (err error) {
	names := []string{f.env, f.env + previousSuffix, f.env + createdSuffix}
	saved := make([]string, len(names))
	for i, name := range names {
		saved[i] = getEnvOwner(name)
	}
	valueSet := false
	defer func() {
		if err == nil {
			return
		}
		if valueSet {
			err = errors.Join(err, setUserEnv(f.env, string(f.before)))
		}
		for i, name := range names {
			err = errors.Join(err, setEnvOwner(name, saved[i]))
		}
	}()
	if f.owner != "" {
		// When the value is not ours yet, its enduser.id, also the same email, belongs to the user.
		previous := saved[1]
		if id := enduserID(string(f.before)); id == "" || id != saved[0] {
			previous = id
		}
		if err = setEnvOwner(names[1], previous); err != nil {
			return err
		}
		// A variable that the user owns now was not created by setup, whatever an old record says.
		created := saved[2]
		if !f.existed {
			// Keep the copy of the machine value, so remove can tell it from user additions.
			created, _ = resourceAttributes(getMachineEnv(f.env), "")
		} else if enduserID(string(f.before)) != saved[0] {
			created = ""
		}
		if err = setEnvOwner(names[2], created); err != nil {
			return err
		}
		if err = setEnvOwner(names[0], f.owner); err != nil {
			return err
		}
	}
	if !f.unchanged() {
		if err = setUserEnv(f.env, string(f.after)); err != nil {
			return err
		}
		valueSet = true
	}
	if f.owner == "" {
		for _, name := range names {
			if err = setEnvOwner(name, ""); err != nil {
				return err
			}
		}
	}
	return nil
}
