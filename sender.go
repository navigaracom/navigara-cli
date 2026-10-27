package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/navigaracom/navigara-cli/internal/telemetry"
)

type sender struct {
	client        *http.Client
	base, token   string
	sent          int
	sentMu        sync.Mutex
	codexReceipts map[[32]byte]int
	// retried reports each failed attempt that will be retried. It can be called concurrently.
	retried func(string)
	// explicitKey marks a key from --token or NAVIGARA_TOKEN, which login does not replace.
	explicitKey bool
}

// maxOTLPBody is the receiver's limit for a decoded request body.
const maxOTLPBody = 16 << 20

const maxAttempts = 5

var errIngestionAuthRejected = errors.New("Navigara rejected the key")

func newSender(base, token string) *sender {
	return &sender{client: &http.Client{Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		base: otlpBase(base), token: token, retried: printRetry}
}

// printRetry clears a terminal line first, so an active spinner does not share the line.
func printRetry(line string) {
	if term.IsTerminal(os.Stderr.Fd()) {
		line = "\r\033[2K" + line
	}
	fmt.Fprintln(os.Stderr, line)
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
func (s *sender) checkSetup(ctx context.Context) error {
	return s.send(ctx, telemetry.Body{Path: "/v1/metrics", Data: []byte(`{}`)}, true)
}

// post sends one gzip-compressed OTLP body.
func (s *sender) post(ctx context.Context, b telemetry.Body) error { return s.send(ctx, b, false) }

func (s *sender) send(ctx context.Context, b telemetry.Body, probe bool) error {
	if err := s.validate(); err != nil {
		return err
	}
	if b.Path != "/v1/metrics" && b.Path != "/v1/logs" {
		return errors.New("unsupported OTLP signal")
	}
	var lastErr error
	reason := ""
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<attempt) * time.Second
			s.retried(fmt.Sprintf("Upload failed (%s), retrying in %d s (attempt %d of %d)…", reason, delay/time.Second, attempt, maxAttempts))
			backoff := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				backoff.Stop()
				return cancelled(ctx, lastErr)
			case <-backoff.C:
			}
		}
		if ctx.Err() != nil {
			return cancelled(ctx, lastErr)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+b.Path, bytes.NewReader(b.Data))
		if err != nil {
			return errors.New("invalid OTLP request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Navigara-Backfill-Receipt", "v1")
		if !probe {
			req.Header.Set("Content-Encoding", "gzip")
		}
		req.Header.Set("Authorization", "Bearer "+s.token)
		resp, err := s.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return cancelled(ctx, lastErr)
			}
			if probe && errors.Is(err, errConnRefused) {
				return fmt.Errorf("Cannot reach %s. Check the address and your network.", req.URL.Host)
			}
			lastErr = fmt.Errorf("OTLP connection failed; check network and TLS settings: %w", err)
			reason = "connection error"
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
		resp.Body.Close()
		if readErr != nil && ctx.Err() != nil {
			return cancelled(ctx, lastErr)
		}
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
			skipped := -1 // No acknowledgment means the omission count is unavailable.
			if b.Path == "/v1/metrics" && resp.Header.Get("X-Navigara-Backfill-Receipt") != "" {
				if resp.Header.Get("X-Navigara-Backfill-Receipt") != "v1" {
					return errors.New("unsupported backfill receipt version")
				}
				count := resp.Header.Get("X-Navigara-Backfill-Skipped-Codex-Datapoints")
				var err error
				skipped, err = strconv.Atoi(count)
				if err != nil || skipped < 0 || strings.Trim(count, "0123456789") != "" {
					return errors.New("invalid Codex omission count in backfill receipt")
				}
				if skipped > 0 && resp.Header.Get("X-Navigara-Backfill-Codex-Skip-Reason") != "overlap-unverified-v1" {
					return errors.New("unsupported Codex omission reason in backfill receipt")
				}
			}
			if probe {
				if resp.Header.Get("X-Navigara-Backfill-Policy") != "session-live-wins-v1" {
					return errors.New("Your Navigara server is too old for this CLI. Ask your admin to update Navigara.")
				}
			} else {
				s.sentMu.Lock()
				s.sent++
				if b.Path == "/v1/metrics" {
					if s.codexReceipts == nil {
						s.codexReceipts = make(map[[32]byte]int)
					}
					s.codexReceipts[sha256.Sum256(b.Data)] = skipped
				}
				s.sentMu.Unlock()
			}
			return nil
		case resp.StatusCode == http.StatusUnauthorized && s.explicitKey:
			return fmt.Errorf("%w (expired or revoked). Create a new Analytics Ingestor key, or remove --url/--token/NAVIGARA_TOKEN to use your saved login.", errIngestionAuthRejected)
		case resp.StatusCode == http.StatusUnauthorized:
			return fmt.Errorf("%w (expired or revoked). Run navigara login, or create a new Analytics Ingestor key.", errIngestionAuthRejected)
		case resp.StatusCode == http.StatusForbidden:
			return fmt.Errorf("%w: it does not have the Analytics Ingestor role. Create a new Analytics Ingestor key.", errIngestionAuthRejected)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s: HTTP %d", b.Path, resp.StatusCode)
			reason = fmt.Sprintf("HTTP %d", resp.StatusCode)
		default:
			return fmt.Errorf("%s: HTTP %d", b.Path, resp.StatusCode)
		}
	}
	return lastErr
}

// cancelled reports why ctx ended, with the last failed attempt when there was one. The cause
// set by a parallel upload, such as another batch's failure, is reported in place of Canceled.
func cancelled(ctx context.Context, last error) error {
	cause := context.Cause(ctx)
	if last == nil {
		return cause
	}
	return fmt.Errorf("%w; last attempt: %w", cause, last)
}

// Repeated frozen batches replace their receipt; resume never adds a prior run's totals.
func (s *sender) codexSummary() string {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	total := 0
	for _, count := range s.codexReceipts {
		if count < 0 || count > int(^uint(0)>>1)-total {
			return "Navigara did not report how many Codex usage records were skipped.\n"
		}
		total += count
	}
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("%d Codex usage records were skipped because they may overlap live data.\n", total)
}
