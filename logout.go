package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
)

func runLogout(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", "", "Navigara instance origin (default saved host)")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "Usage: navigara logout [--host ORIGIN]"); fs.PrintDefaults() }
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fs.SetOutput(stdout)
			fs.Usage()
			return 0
		}
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "navigara: unexpected logout argument")
		return 2
	}

	saved, err := loadAccount("")
	if err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	if *host != "" {
		wanted, err := canonicalHost(*host)
		if err != nil {
			fmt.Fprintln(stderr, "navigara:", err)
			return 2
		}
		if saved.Host != wanted {
			fmt.Fprintf(stdout, "No saved Navigara account for %s.\n", wanted)
			return 0
		}
	}

	clear := accountConfig{Version: 1, Host: saved.Host}
	if saved == clear {
		fmt.Fprintln(stdout, "Already signed out of Navigara on this computer.")
		return 0
	}

	var revokeErr error
	if saved.SessionToken != "" {
		revokeErr = newAuthClient(saved.Host).request(ctx, "POST", "/v1/auth/sign-out", saved.SessionToken, struct{}{}, &struct{}{})
	}
	if errors.Is(revokeErr, authHTTPError(401)) {
		revokeErr = nil
	}
	if err := writeAccount(clear, &saved); err != nil {
		fmt.Fprintln(stderr, "navigara: could not clear saved account:", err)
		return 1
	}
	fmt.Fprintln(stdout, "Signed out of Navigara on this computer.\nClaude Code and Codex streaming settings remain active.")
	if revokeErr != nil {
		fmt.Fprintln(stderr, "navigara: local credentials cleared, but could not revoke the server session:", revokeErr)
		return 1
	}
	return 0
}
