//go:build !darwin && !linux && !windows

package main

import "errors"

func acquireAccountLock(string) (func(), error) {
	return nil, errors.New("account config locking is unsupported on this platform")
}
