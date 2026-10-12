package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/navigaracom/navigara-cli/internal/telemetry"
)

type sender struct {
	client      *http.Client
	base, token string
	sent        int
}

// maxOTLPBody is the receiver's limit for a decoded request body.
const maxOTLPBody = 16 << 20

var errIngestionAuthRejected = errors.New("the API key needs the analytics ingestor role")

func newSender(base, token string) *sender {
	return &sender{client: &http.Client{Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		base: otlpBase(base), token: token}
}

func otlpBase(base string) string { return strings.TrimRight(base, "/") + "/otlp" }

func (s *sender) validate() error {
	u, err := url.Parse(s.base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("URL must be an HTTPS Navigara API base without credentials, query or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback()) {
		return errors.New("URL requires HTTPS (HTTP is allowed only for a loopback IP)")
	}
	if s.token == "" || strings.ContainsAny(s.token, " \t\r\n") {
		return errors.New("a nonempty analytics ingestor token is required")
	}
	return nil
}

// An empty export verifies authorization and the deployed session policy, without
// manufacturing a session or usage. A plain 200 from an older server is insufficient.
func (s *sender) checkSetup() error {
	return s.send(telemetry.Body{Path: "/v1/metrics", Data: []byte(`{}`)}, true)
}

// post sends one gzip-compressed OTLP body.
func (s *sender) post(b telemetry.Body) error { return s.send(b, false) }

func (s *sender) send(b telemetry.Body, probe bool) error {
	if err := s.validate(); err != nil {
		return err
	}
	if b.Path != "/v1/metrics" && b.Path != "/v1/logs" {
		return errors.New("unsupported OTLP signal")
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<attempt) * time.Second)
		}
		req, err := http.NewRequest(http.MethodPost, s.base+b.Path, bytes.NewReader(b.Data))
		if err != nil {
			return errors.New("invalid OTLP request")
		}
		req.Header.Set("Content-Type", "application/json")
		if !probe {
			req.Header.Set("Content-Encoding", "gzip")
		}
		req.Header.Set("Authorization", "Bearer "+s.token)
		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = errors.New("OTLP connection failed; check network and TLS settings")
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK:
			if readErr != nil || len(data) > 65536 {
				return errors.New("invalid OTLP response")
			}
			var result struct {
				Partial json.RawMessage `json:"partialSuccess"`
			}
			if len(bytes.TrimSpace(data)) != 0 && json.Unmarshal(data, &result) != nil {
				return errors.New("invalid OTLP JSON response")
			}
			// An exporter cannot promise success when the receiver reports any rejection
			// or warning. Preserve the exact local bodies so the user can retry them.
			if len(result.Partial) != 0 && string(result.Partial) != "{}" && string(result.Partial) != "null" {
				return errors.New("receiver reported partial success; inspect server diagnostics before retrying")
			}
			if probe {
				if resp.Header.Get("X-Navigara-Backfill-Policy") != "session-live-wins-v1" {
					return errors.New("server must support session backfill deduplication before setup can continue")
				}
			} else {
				s.sent++
			}
			return nil
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return fmt.Errorf("%s: HTTP %d; %w", b.Path, resp.StatusCode, errIngestionAuthRejected)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s: HTTP %d", b.Path, resp.StatusCode)
		default:
			return fmt.Errorf("%s: HTTP %d", b.Path, resp.StatusCode)
		}
	}
	return lastErr
}
