package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	oauthClientID = "navigara-cli"
	oauthScope    = "navigara"
	maxOAuthBody  = 1024 * 1024
	maxOAuthToken = 8192
)

func (c *authClient) oauthToken(ctx context.Context, code, redirectURI, verifier string, requestStart time.Time) (loginSession, error) {
	if _, err := canonicalHost(c.host); err != nil {
		return loginSession{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {oauthClientID},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+"/api/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return loginSession{}, errors.New("invalid OAuth token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return loginSession{}, errors.New("cannot reach Navigara to finish sign-in")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody+1))
	if err != nil || len(body) > maxOAuthBody {
		return loginSession{}, errors.New("invalid OAuth token response")
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return loginSession{}, oauthTokenError(body)
	}
	if resp.StatusCode != http.StatusOK {
		return loginSession{}, errors.New("Navigara could not finish sign-in; run navigara login again")
	}
	var token struct {
		AccessToken string      `json:"access_token"`
		TokenType   string      `json:"token_type"`
		ExpiresIn   json.Number `json:"expires_in"`
		Scope       string      `json:"scope"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if decoder.Decode(&token) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(token.AccessToken) == 0 || len(token.AccessToken) > maxOAuthToken || !strings.EqualFold(token.TokenType, "Bearer") || (token.Scope != "" && token.Scope != oauthScope) {
		return loginSession{}, errors.New("invalid OAuth token response")
	}
	seconds, err := token.ExpiresIn.Int64()
	if err != nil || seconds <= 0 || seconds > math.MaxInt64/int64(time.Second) {
		return loginSession{}, errors.New("invalid OAuth token response")
	}
	expiresAt := requestStart.Add(time.Duration(seconds) * time.Second)
	if !expiresAt.After(requestStart) {
		return loginSession{}, errors.New("invalid OAuth token response")
	}
	return loginSession{SessionToken: token.AccessToken, ExpiresAt: expiresAt}, nil
}

func oauthTokenError(body []byte) error {
	var response struct {
		Code string `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil {
		return errors.New("Navigara rejected sign-in; run navigara login again")
	}
	switch response.Code {
	case "invalid_grant":
		return errors.New("sign-in code expired or was already used; run navigara login again")
	case "invalid_client", "unauthorized_client":
		return errors.New("Navigara rejected this CLI sign-in; contact your administrator")
	case "invalid_request", "unsupported_grant_type":
		return errors.New("Navigara could not finish sign-in; run navigara login again")
	default:
		return errors.New("Navigara rejected sign-in; run navigara login again")
	}
}
