package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

type authClient struct {
	host string
	http *http.Client
}

type authHTTPError int

func (e authHTTPError) Error() string {
	if e == 401 {
		return "Navigara session is no longer valid (HTTP 401); run navigara login"
	}
	if e == 403 {
		return "Navigara denied access (HTTP 403); ask your organization administrator"
	}
	return fmt.Sprintf("Navigara authentication API returned HTTP %d", e)
}

func newAuthClient(host string) *authClient {
	return &authClient{host: host, http: &http.Client{Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *authClient) request(ctx context.Context, method, path, token string, body, result any) error {
	if _, err := canonicalHost(c.host); err != nil {
		return err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return errors.New("cannot encode Navigara request")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(data)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.host+"/api"+path, reader)
	if err != nil {
		return errors.New("invalid Navigara request")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return fmt.Errorf("cannot reach Navigara; check the host, network and TLS settings: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return authHTTPError(resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 || json.Unmarshal(data, result) != nil {
		return errors.New("invalid Navigara response")
	}
	return nil
}

type accountUser struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

func (c *authClient) session(ctx context.Context, token string) (accountUser, error) {
	var user accountUser
	err := c.request(ctx, "GET", "/v1/auth/session", token, nil, &user)
	if err == nil && (user.UserID == "" || validEmail(user.Email) != nil) {
		err = errors.New("Navigara returned an invalid account")
	}
	return user, err
}
