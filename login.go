package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

type organization struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (c *authClient) organizations(ctx context.Context, token string) ([]organization, error) {
	var organizations []organization
	seen := map[string]bool{}
	page := ""
	for {
		var response struct {
			Organizations []organization `json:"organizations"`
			NextPageToken string         `json:"nextPageToken"`
		}
		path := "/v1/auth/organizations?" + url.Values{"page_size": {"100"}, "page_token": {page}}.Encode()
		if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
			return nil, err
		}
		for _, org := range response.Organizations {
			if org.ID != "" && org.Status == "active" {
				organizations = append(organizations, org)
			}
		}
		page = response.NextPageToken
		if page == "" {
			return organizations, nil
		}
		if seen[page] || len(seen) >= 100 {
			return nil, errors.New("invalid organization pagination response")
		}
		seen[page] = true
	}
}

func chooseOrganization(orgs []organization, previous string, force bool, p *setupInput) (organization, error) {
	if len(orgs) == 0 {
		return organization{}, errors.New("no active organizations available; ask an administrator to invite you, then run navigara login again")
	}
	if len(orgs) == 1 {
		return orgs[0], nil
	}
	if !force {
		for _, org := range orgs {
			if org.ID == previous {
				return org, nil
			}
		}
	}
	fmt.Fprintln(p.out, "Choose an organization:")
	for i, org := range orgs {
		fmt.Fprintf(p.out, "  %d. %q\n", i+1, org.Name)
	}
	answer, err := p.ask("Organization number", "")
	if err != nil {
		return organization{}, err
	}
	n, err := strconv.Atoi(answer)
	if err != nil || n < 1 || n > len(orgs) {
		return organization{}, errors.New("enter one of the listed organization numbers")
	}
	return orgs[n-1], nil
}

func ensureAccount(ctx context.Context, host string, p *setupInput, forceSelection bool, open func(string) error) (accountConfig, error) {
	c, err := loadAccount(host)
	if err != nil {
		return c, err
	}
	client := newAuthClient(c.Host)
	var user struct {
		UserID string `json:"userId"`
		Email  string `json:"email"`
	}
	login := c.SessionToken == "" || !c.SessionExpiresAt.After(time.Now())
	if !login {
		err = client.request(ctx, "GET", "/v1/auth/session", c.SessionToken, nil, &user)
		if errors.Is(err, authHTTPError(401)) {
			login = true
		} else if err != nil {
			return c, err
		}
	}
	if login {
		session, err := client.browserLogin(ctx, p.out, open)
		if err != nil {
			return c, err
		}
		c.SessionToken, c.SessionExpiresAt = session.SessionToken, session.ExpiresAt
		if err = client.request(ctx, "GET", "/v1/auth/session", c.SessionToken, nil, &user); err != nil {
			return c, err
		}
	}
	if user.UserID == "" || validEmail(user.Email) != nil {
		return c, errors.New("Navigara returned an invalid account")
	}
	accountChanged := login || c.UserID != user.UserID || c.Email != user.Email
	if c.UserID != user.UserID {
		c.OrganizationID, c.OrganizationName, c.IngestToken = "", "", ""
		c.IngestExpiresAt = time.Time{}
	}
	c.UserID, c.Email = user.UserID, user.Email
	if accountChanged {
		if err = saveAccount(c); err != nil {
			return c, err
		}
	}
	orgs, err := client.organizations(ctx, c.SessionToken)
	if err != nil {
		return c, err
	}
	org, err := chooseOrganization(orgs, c.OrganizationID, forceSelection, p)
	if err != nil {
		return c, err
	}
	if org.ID != c.OrganizationID {
		c.IngestToken = ""
		c.IngestExpiresAt = time.Time{}
	}
	c.OrganizationID, c.OrganizationName = org.ID, org.Name
	if err = saveAccount(c); err != nil {
		return c, err
	}
	fmt.Fprintf(p.out, "Account: %s\nHost: %s\nOrganization: %q (%s)\n", c.Email, c.Host, c.OrganizationName, c.OrganizationID)
	return c, nil
}

func runLogin(args []string, input io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", "", "Navigara instance origin (default saved host or https://app.navigara.com)")
	fs.Usage = func() { fmt.Fprintln(fs.Output(), "Usage: navigara login [--host ORIGIN]"); fs.PrintDefaults() }
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
		fmt.Fprintln(stderr, "navigara: unexpected login argument")
		return 2
	}
	p := &setupInput{r: bufio.NewReader(input), out: stdout}
	if _, err := ensureAccount(context.Background(), *host, p, true, openBrowser); err != nil {
		fmt.Fprintln(stderr, "navigara:", err)
		return 1
	}
	return 0
}
