//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func acquireAccountLock(dir string) (func(), error) {
	path := filepath.Join(dir, "config.lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open account config lock: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s must be a private regular file", path)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.New("another process is saving account config; try again")
		}
		return nil, fmt.Errorf("lock account config: %w", err)
	}
	return func() { _ = file.Close() }, nil
}
