package main

import (
	"context"
	"errors"
)

func prepareUploadAccount(ctx context.Context, opts *options) error {
	if !opts.send {
		return nil
	}
	if opts.url != "" || opts.token != "" {
		if opts.url == "" || opts.token == "" {
			return errors.New("explicit upload requires both --url and --token (or NAVIGARA_TOKEN); omit both to use navigara login")
		}
		if opts.host != "" {
			return errors.New("use either --host with saved login, or --url and --token")
		}
		return nil
	}
	c, err := verifiedAccount(ctx, opts.host)
	if err != nil {
		return err
	}
	if err = ensureIngestion(ctx, &c); err != nil {
		return err
	}
	opts.url, opts.token = c.Host+"/api", c.IngestToken
	if opts.email == "" && !opts.allowAnonymous {
		opts.email = c.Email
	}
	return nil
}
