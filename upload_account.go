package main

import (
	"context"
	"errors"
)

// prepareUploadAccount resolves the destination and the email of an upload. A dry run resolves
// the same email as a send, but it needs no login and creates no key.
func prepareUploadAccount(ctx context.Context, opts *options) error {
	if opts.url != "" || opts.token != "" || opts.tokenExplicit {
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
	}
	c, err := verifiedAccount(ctx, opts.host)
	if err != nil {
		return err
	}
	if opts.send {
		if err = ensureIngestion(ctx, &c); err != nil {
			return err
		}
		opts.url, opts.token, opts.organizationID = c.Host+"/api", c.IngestToken, c.OrganizationID
	}
	if opts.email == "" && !opts.allowAnonymous {
		opts.email = c.Email
	}
	return nil
}
