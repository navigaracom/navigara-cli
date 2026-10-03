package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

func observeRepository(b []byte, source, kind, timestamp string, line int) (*RepositoryObservation, error) {
	var context RepositoryObservation
	if source == "claude_code" {
		var row struct {
			Branch *string `json:"gitBranch"`
		}
		if json.Unmarshal(b, &row) != nil {
			return nil, fmt.Errorf("invalid repository context")
		}
		if row.Branch == nil {
			return nil, nil
		}
		context.Branch = *row.Branch
	} else {
		if kind != "session_meta" {
			return nil, nil
		}
		var row struct {
			Payload struct {
				Git json.RawMessage `json:"git"`
			} `json:"payload"`
		}
		if json.Unmarshal(b, &row) != nil {
			return nil, fmt.Errorf("invalid repository context")
		}
		git := row.Payload.Git
		if len(git) == 0 || bytes.Equal(bytes.TrimSpace(git), []byte("null")) {
			return nil, nil
		}
		var fields struct {
			Branch string `json:"branch"`
			Commit string `json:"commit_hash"`
			URL    string `json:"repository_url"`
		}
		if json.Unmarshal(git, &fields) != nil {
			return nil, fmt.Errorf("invalid repository context")
		}
		context.Branch, context.CommitHash, context.RepositoryURL = fields.Branch, fields.Commit, repositoryKey(fields.URL)
	}
	context.RecordLine = line
	if timestamp != "" {
		t, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, fmt.Errorf("invalid timestamp")
		}
		t = t.UTC()
		context.ObservedAt = &t
	}
	return &context, nil
}

// repositoryKey provides a credential-free host/path key; provider aliases are backend concerns.
func repositoryKey(value string) string {
	if strings.ContainsAny(value, "\\\r\n\t") {
		return ""
	}
	if !strings.Contains(value, "://") {
		host, path, ok := strings.Cut(value, ":")
		first, _, _ := strings.Cut(path, "/")
		first, err := url.PathUnescape(first)
		if !ok || err != nil || len(host) <= 1 || strings.ContainsAny(host, "/?#") || strings.EqualFold(host, "file") || strings.Contains(first, "@") {
			return ""
		}
		value = "ssh://" + host + "/" + strings.TrimLeft(path, "/")
	}
	u, err := url.Parse(value)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ssh", "git":
	default:
		return ""
	}
	host := strings.ToLower(u.Hostname())
	query, err := url.QueryUnescape(u.RawQuery)
	if err != nil {
		return ""
	}
	// Check every non-userinfo component before discarding query and fragment.
	for _, component := range []string{u.Path, query, u.Fragment} {
		for _, r := range component {
			if r == '\\' || r == '@' || r == '%' || unicode.IsControl(r) {
				return ""
			}
		}
	}
	path := strings.TrimSuffix(strings.ToLower(strings.Trim(u.EscapedPath(), "/")), ".git")
	if host == "" || path == "" || strings.HasSuffix(path, "/") || strings.ContainsAny(host, "/\\@% ") {
		return ""
	}
	return host + "/" + path
}
