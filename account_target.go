package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// verifiedAccount checks the saved account against the server and returns a
// refreshed copy. It deliberately does not persist anything: callers can use
// this for previews without changing the account context on disk.
func verifiedAccount(ctx context.Context, host string) (accountConfig, error) {
	c, err := loadAccount(host)
	if err != nil {
		return c, err
	}
	if c.SessionToken == "" || !c.SessionExpiresAt.After(time.Now()) || c.UserID == "" || c.OrganizationID == "" {
		return c, errors.New("saved Navigara account is missing a usable session or organization; run navigara login")
	}

	client := newAuthClient(c.Host)
	user, err := client.session(ctx, c.SessionToken)
	if err != nil {
		return c, err
	}
	if user.UserID != c.UserID {
		return c, errors.New("saved Navigara account no longer matches the signed-in user; run navigara login")
	}
	orgs, err := client.organizations(ctx, c.SessionToken)
	if err != nil {
		return c, err
	}
	if len(orgs) == 0 {
		return c, errors.New("no active organizations available; ask an organization administrator to invite you, then run navigara login")
	}
	var selected *organization
	for i := range orgs {
		if orgs[i].ID == c.OrganizationID {
			selected = &orgs[i]
			break
		}
	}
	if selected == nil {
		return c, errors.New("saved organization is no longer available; run navigara login")
	}
	c.Email = user.Email
	c.OrganizationName = selected.Name
	return c, nil
}

// ensureIngestion reuses a still-valid ingestion key or mints one for the
// already verified account. The session token is never used as an ingestion
// credential.
func ensureIngestion(ctx context.Context, c *accountConfig) error {
	if c == nil || c.OrganizationID == "" || c.SessionToken == "" {
		return errors.New("a verified Navigara account is required; run navigara login")
	}
	if strings.TrimSpace(c.IngestToken) != "" && c.IngestExpiresAt.After(time.Now()) {
		return nil
	}

	expiresAt := time.Now().UTC().Add(365 * 24 * time.Hour)
	var response struct {
		PlainTextKey string `json:"plainTextKey"`
		APIKey       struct {
			OrganizationID string    `json:"organizationId"`
			ExpiresAt      time.Time `json:"expiresAt"`
		} `json:"apiKey"`
	}
	client := newAuthClient(c.Host)
	path := "/v1/organizations/" + url.PathEscape(c.OrganizationID) + "/api-keys"
	err := client.request(ctx, "POST", path, c.SessionToken, map[string]any{
		"name":      "Navigara CLI",
		"role":      "roles/analytics.ingestor",
		"expiresAt": expiresAt,
	}, &response)
	if err != nil {
		if errors.Is(err, authHTTPError(403)) {
			return errors.New("you need organization admin access to create a Navigara CLI ingestion key; ask an organization admin")
		}
		return err
	}
	if response.APIKey.OrganizationID != c.OrganizationID {
		return errors.New("Navigara returned an ingestion key for the wrong organization")
	}
	if strings.TrimSpace(response.PlainTextKey) == "" || strings.IndexFunc(response.PlainTextKey, unicode.IsSpace) >= 0 {
		return errors.New("Navigara returned an invalid ingestion key")
	}
	if response.APIKey.ExpiresAt.IsZero() || !response.APIKey.ExpiresAt.After(time.Now()) {
		return errors.New("Navigara returned an expired ingestion key")
	}
	updated := *c
	updated.IngestToken = response.PlainTextKey
	updated.IngestExpiresAt = response.APIKey.ExpiresAt
	if err := writeAccount(updated, c); err != nil {
		return fmt.Errorf("save ingestion key: %w", err)
	}
	*c = updated
	return nil
}
