//go:build !windows

package main

import (
	"errors"
	"syscall"
)

var errConnRefused error = syscall.ECONNREFUSED

var errUserEnv = errors.New("user environment variables are only saved on Windows")

func getUserEnv(string) (string, bool, error) { return "", false, errUserEnv }

func setUserEnv(string, string) error { return errUserEnv }

func getMachineEnv(string) string { return "" }

func getEnvOwner(string) string { return "" }

func setEnvOwner(string, string) error { return errUserEnv }
