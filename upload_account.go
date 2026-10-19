package main

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// prepareUploadAccount resolves the destination and the email of an upload. A dry run resolves
// the same email as a send when it can, but it never fails on login and creates no key.
func prepareUploadAccount(ctx context.Context, opts *options, stderr io.Writer) error {
	// A dry run ignores $NAVIGARA_TOKEN: only --url or --token choose a destination there.
	if opts.url != "" || opts.tokenExplicit || (opts.send && opts.token != "") {
		if opts.url == "" || opts.token == "" {
			return errors.New("explicit upload requires both --url and --token (or NAVIGARA_TOKEN); omit both to use navigara login")
		}
		if opts.host != "" {
			return errors.New("use either --host with saved login, or --url and --token")
		}
		return nil
	}
	if !opts.send {
		if opts.email != "" || opts.allowAnonymous {
			return nil
		}
		if c, err := loadAccount(opts.host); err != nil || c.SessionToken == "" {
			return nil
		}
		c, err := verifiedAccount(ctx, opts.host)
		if err != nil {
			fmt.Fprintf(stderr, "Saved login could not be checked (%v). Using the email from Claude Code or Codex.\n", err)
			return nil
		}
		opts.email = c.Email
		return nil
	}
	c, err := verifiedAccount(ctx, opts.host)
	if err != nil {
		return err
	}
	if err = ensureIngestion(ctx, &c); err != nil {
		return err
	}
	opts.url, opts.token, opts.organizationID = c.Host+"/api", c.IngestToken, c.OrganizationID
	if opts.email == "" && !opts.allowAnonymous {
		opts.email = c.Email
	}
	return nil
}
