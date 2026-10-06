package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type setupFile struct {
	path, backup  string
	before, after []byte
	existed       bool
	installed     os.FileInfo
}

func readSetupFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s must be a regular file, not a symlink", path)
	}
	if info.Size() > 4<<20 {
		return nil, false, fmt.Errorf("%s is too large for setup", path)
	}
	data, err := os.ReadFile(path)
	return data, true, err
}

func planFile(path string, transform func([]byte) ([]byte, error)) (*setupFile, error) {
	before, exists, err := readSetupFile(path)
	if err != nil {
		return nil, err
	}
	after, err := transform(before)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &setupFile{path: path, before: before, after: after, existed: exists}, nil
}

func (f *setupFile) check() error {
	data, exists, err := readSetupFile(f.path)
	if err != nil {
		return err
	}
	if exists != f.existed || !bytes.Equal(data, f.before) {
		return fmt.Errorf("%s changed during setup; run setup again", f.path)
	}
	// Tool homes and shell directories must already exist. Never create an
	// unexpected hierarchy or write through a replaced immediate parent symlink.
	info, err := os.Lstat(filepath.Dir(f.path))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s must be a directory, not a symlink", filepath.Dir(f.path))
	}
	return nil
}

// prepare creates the private replacement before the final target transition.
func (f *setupFile) prepare() (string, error) {
	if err := f.check(); err != nil {
		return "", err
	}
	return privateFile(filepath.Dir(f.path), ".navigara-new-*", f.after)
}

// install never replaces a target. Existing files move atomically to a private backup, leaving a
// short absent interval while the replacement is linked exclusively. Any writer that creates the
// target in that interval wins; the captured file remains in the private backup directory.
func (f *setupFile) install(tmp string) (err error) {
	installed, err := os.Lstat(tmp)
	if err != nil {
		return fmt.Errorf("inspect replacement %s: %w", tmp, err)
	}
	if !installed.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular replacement file", tmp)
	}
	captured := false
	defer func() {
		if err == nil || !captured {
			return
		}
		restored, restoreErr := restoreIfAbsent(f.backup, f.path)
		if restoreErr != nil {
			err = fmt.Errorf("%w; captured file preserved at %s; restore failed: %v", err, f.backup, restoreErr)
			return
		}
		if restored {
			err = fmt.Errorf("%w; original restored from %s", err, f.backup)
			return
		}
		err = fmt.Errorf("%w; captured file preserved at %s; newer target retained", err, f.backup)
	}()
	if f.existed {
		backupDir, err := os.MkdirTemp(filepath.Dir(f.path), ".navigara-backup-*")
		if err != nil {
			return err
		}
		if err := os.Chmod(backupDir, 0700); err != nil {
			os.RemoveAll(backupDir)
			return fmt.Errorf("secure backup directory %s: %w", backupDir, err)
		}
		probe := filepath.Join(backupDir, ".probe")
		if err := os.Link(tmp, probe); err != nil {
			os.RemoveAll(backupDir)
			return fmt.Errorf("preflight exclusive install for %s: %w", f.path, err)
		}
		if err := os.Remove(probe); err != nil {
			if cleanupErr := os.RemoveAll(backupDir); cleanupErr != nil {
				return fmt.Errorf("remove install preflight %s: %w; cleanup %s: %v", probe, err, backupDir, cleanupErr)
			}
			return fmt.Errorf("remove install preflight %s: %w", probe, err)
		}
		backup := filepath.Join(backupDir, filepath.Base(f.path))
		if err := os.Rename(f.path, backup); err != nil {
			os.RemoveAll(backupDir)
			return err
		}
		f.backup = backup
		captured = true
		info, err := os.Lstat(backup)
		if err != nil {
			return fmt.Errorf("inspect captured file %s: %w", backup, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file, not a symlink", f.path)
		}
		capturedData, exists, err := readSetupFile(backup)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%s vanished during setup", f.path)
		}
		if !bytes.Equal(capturedData, f.before) {
			return fmt.Errorf("%s changed during setup", f.path)
		}
	}
	if err := os.Link(tmp, f.path); err != nil {
		return fmt.Errorf("install %s without replacing an existing file: %w", f.path, err)
	}
	f.installed = installed
	return nil
}

// restoreIfAbsent restores the captured file only when no later writer has supplied a target.
func restoreIfAbsent(backup, path string) (bool, error) {
	info, err := os.Lstat(backup)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(backup)
		if err != nil {
			return false, err
		}
		err = os.Symlink(target, path)
	} else {
		err = os.Link(backup, path)
	}
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	return false, err
}

func privateFile(dir, pattern string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	err = os.Chmod(f.Name(), 0600)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
