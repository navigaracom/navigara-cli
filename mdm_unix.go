//go:build darwin || linux

package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"syscall"
)

const mdmSupported = true

var mdmIDGroups = func(name string) (string, error) {
	out, err := exec.Command("/usr/bin/id", "-G", name).Output()
	return string(out), err
}

func runAsUser(ctx context.Context, exe string, u mdmUser, args, env []string, stdout, stderr io.Writer) error {
	out, err := mdmIDGroups(u.Name)
	if err != nil {
		return fmt.Errorf("look up groups: %w", err)
	}
	groups, err := parseGroupIDs(out, u.GID)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = env, u.Home, stdout, stderr
	// Setsid drops the operator's controlling terminal, so user code cannot open /dev/tty.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Credential: &syscall.Credential{Uid: u.UID, Gid: u.GID, Groups: capGroups(groups, runtime.GOOS)}}
	return cmd.Run()
}
