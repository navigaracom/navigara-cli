//go:build !darwin && !linux

package main

import (
	"context"
	"errors"
	"io"
)

const mdmSupported = false

func runAsUser(context.Context, string, mdmUser, []string, []string, io.Writer, io.Writer) error {
	return errors.New("sessions mdm is unsupported on this platform")
}
