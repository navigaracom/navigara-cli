package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// toolLink is the live sync setting that setup wrote for one tool.
type toolLink struct {
	endpoint, token, email string
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, "Usage: navigara sessions status\n\nShows the saved login and the live sync settings of Claude Code and Codex.\nExit code 0 means that each tool found on this computer sends to Navigara and Navigara accepts its key.")
		return 0
	}
	if len(args) != 0 {
		fmt.Fprintln(stderr, "navigara: unexpected argument:", args[0])
		return 2
	}
	if c, err := loadAccount(""); err != nil {
		fmt.Fprintln(stdout, "Login: cannot read:", err)
	} else if c.SessionToken == "" {
		fmt.Fprintln(stdout, "Login: not signed in")
	} else {
		fmt.Fprintf(stdout, "Login: %s as %s, organization %s\n", c.Host, firstNonEmpty(c.Email, "unknown"), firstNonEmpty(c.OrganizationName, c.OrganizationID, "unknown"))
	}
	claudeHome, codexHome, homeErr := toolHomes("", "")
	ok, found := true, false
	for _, tool := range []struct {
		name, home string
		read       func(string) (toolLink, error)
	}{
		{"Claude Code", claudeHome, claudeLink},
		{"Codex", codexHome, codexLink},
	} {
		if tool.home == "" {
			fmt.Fprintf(stdout, "%s: cannot find its home: %v\n", tool.name, homeErr)
			ok = false
			continue
		}
		if info, err := os.Stat(tool.home); err != nil || !info.IsDir() {
			fmt.Fprintf(stdout, "%s: not found\n", tool.name)
			continue
		}
		found = true
		link, err := tool.read(tool.home)
		if err != nil {
			fmt.Fprintf(stdout, "%s: not connected: %v\n", tool.name, err)
			ok = false
			continue
		}
		host := link.endpoint
		if u, err := url.Parse(link.endpoint); err == nil && u.Host != "" {
			host = u.Host
		}
		fmt.Fprintf(stdout, "%s: connected to %s as %s\n", tool.name, host, firstNonEmpty(link.email, "unknown email"))
		if err := newSender(strings.TrimSuffix(link.endpoint, "/otlp"), link.token).checkSetup(ctx); err != nil {
			fmt.Fprintf(stdout, "  Navigara does not accept the Analytics Ingestor key: %v\n", err)
			ok = false
		} else {
			fmt.Fprintln(stdout, "  Navigara accepts the Analytics Ingestor key")
		}
	}
	if !found {
		fmt.Fprintln(stdout, "No Claude Code or Codex home found on this computer.")
	}
	if !ok {
		return 1
	}
	return 0
}

func claudeLink(home string) (toolLink, error) {
	data, err := os.ReadFile(filepath.Join(home, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return toolLink{}, errors.New("no settings.json")
	}
	if err != nil {
		return toolLink{}, err
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return toolLink{}, errors.New("settings.json is not valid JSON")
	}
	env := settings.Env
	token, hasToken := strings.CutPrefix(env["OTEL_EXPORTER_OTLP_HEADERS"], "Authorization=Bearer ")
	if env["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" || env["OTEL_METRICS_EXPORTER"] != "otlp" || env["OTEL_LOGS_EXPORTER"] != "otlp" || env["OTEL_EXPORTER_OTLP_ENDPOINT"] == "" || !hasToken {
		return toolLink{}, errors.New("telemetry is not set up; run navigara wizard")
	}
	if err := validateClaudeEnv(env, env["OTEL_EXPORTER_OTLP_ENDPOINT"], token); err != nil {
		return toolLink{}, err
	}
	return toolLink{endpoint: env["OTEL_EXPORTER_OTLP_ENDPOINT"], token: token, email: enduserID(env["OTEL_RESOURCE_ATTRIBUTES"])}, nil
}

func codexLink(home string) (toolLink, error) {
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return toolLink{}, errors.New("no config.toml")
	}
	if err != nil {
		return toolLink{}, err
	}
	start, end, err := tomlManagedRange(data)
	if err != nil {
		return toolLink{}, err
	}
	type exporter struct {
		OTLP struct {
			Endpoint string            `toml:"endpoint"`
			Headers  map[string]string `toml:"headers"`
		} `toml:"otlp-http"`
	}
	var block struct {
		OTEL struct {
			Metrics exporter `toml:"metrics_exporter"`
			Logs    exporter `toml:"exporter"`
		} `toml:"otel"`
	}
	if start == end || toml.Unmarshal(data[start:end], &block) != nil {
		return toolLink{}, errors.New("no Navigara block in config.toml; run navigara wizard")
	}
	endpoint, hasEndpoint := strings.CutSuffix(block.OTEL.Metrics.OTLP.Endpoint, "/v1/metrics")
	token, hasToken := strings.CutPrefix(block.OTEL.Metrics.OTLP.Headers["Authorization"], "Bearer ")
	logs := block.OTEL.Logs.OTLP
	if !hasEndpoint || !hasToken || logs.Endpoint != endpoint+"/v1/logs" || logs.Headers["Authorization"] != "Bearer "+token {
		return toolLink{}, errors.New("the Navigara block in config.toml is not complete; run navigara wizard")
	}
	return toolLink{endpoint: endpoint, token: token, email: shellEmail()}, nil
}

// shellEmail reads the email that setup exports to Codex from the profile of the user's shell.
func shellEmail() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	profile := filepath.Join(home, ".bashrc")
	if filepath.Base(os.Getenv("SHELL")) == "zsh" {
		profile = filepath.Join(firstNonEmpty(os.Getenv("ZDOTDIR"), home), ".zshrc")
	}
	data, err := os.ReadFile(profile)
	if err != nil {
		return ""
	}
	start, end, err := managedRange(data)
	if err != nil {
		return ""
	}
	_, value, found := strings.Cut(string(data[start:end]), "enduser.id=\"'")
	value, _, closed := strings.Cut(value, "'\n")
	if !found || !closed {
		return ""
	}
	return strings.ReplaceAll(value, `'"'"'`, "'")
}

func enduserID(attrs string) string {
	for _, attr := range strings.Split(attrs, ",") {
		if key, value, ok := strings.Cut(attr, "="); ok && strings.TrimSpace(key) == "enduser.id" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
