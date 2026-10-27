package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func acquireAccountLock(dir string) (func(), error) {
	path := filepath.Join(dir, "config.lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("open account config lock: %w", err)
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%s must be a regular file", path)
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errors.New("another process is saving account config; try again")
		}
		return nil, fmt.Errorf("lock account config: %w", err)
	}
	return func() { _ = file.Close() }, nil
}
