//go:build !darwin && !linux

package main

import "errors"

func acquireAccountLock(string) (func(), error) {
	return nil, errors.New("account config locking is unsupported on this platform")
}
