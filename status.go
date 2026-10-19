package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// toolLink is the live sync setting that setup wrote for one tool.
type toolLink struct {
	endpoint, token, email string
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var shell string
	fs := flag.NewFlagSet("sessions status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: navigara sessions status [--shell bash|zsh|fish]\n\nShows the saved login and the live sync settings of Claude Code and Codex.\nExit code 0 means that each tool found on this computer sends to Navigara and Navigara accepts its key.")
	}
	fs.StringVar(&shell, "shell", "", "shell profile for the Codex identity: bash, zsh, fish (default $SHELL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "navigara: unexpected argument:", fs.Arg(0))
		return 2
	}
	c, err := loadAccount("")
	if err != nil {
		fmt.Fprintln(stdout, "Login: cannot read:", err)
	} else if c.SessionToken == "" {
		fmt.Fprintln(stdout, "Login: not signed in")
	} else {
		fmt.Fprintf(stdout, "Login: %s as %s, organization %s\n", c.Host, firstNonEmpty(c.Email, "unknown"), firstNonEmpty(c.OrganizationName, c.OrganizationID, "unknown"))
	}
	shell = firstNonEmpty(shell, filepath.Base(os.Getenv("SHELL")))
	claudeHome, codexHome, homeErr := toolHomes("", "")
	ok, found := true, false
	for _, tool := range []struct {
		name, key, home string
		read            func(string) (toolLink, error)
	}{
		{"Claude Code", "claude", claudeHome, claudeLink},
		{"Codex", "codex", codexHome, func(home string) (toolLink, error) { return codexLink(home, shell) }},
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
		if at, imported := c.Imports[c.OrganizationID+"/"+tool.key]; imported && c.OrganizationID != "" {
			fmt.Fprintf(stdout, "%s: last import %s\n", tool.name, at.Local().Format(time.RFC3339))
		}
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
		Env           map[string]string `json:"env"`
		HeadersHelper json.RawMessage   `json:"otelHeadersHelper"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return toolLink{}, errors.New("settings.json is not valid JSON")
	}
	env := settings.Env
	endpoint := env["OTEL_EXPORTER_OTLP_ENDPOINT"]
	token, hasToken := strings.CutPrefix(env["OTEL_EXPORTER_OTLP_HEADERS"], "Authorization=Bearer ")
	email := enduserID(env["OTEL_RESOURCE_ATTRIBUTES"])
	if env["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" || endpoint == "" || !hasToken {
		return toolLink{}, errors.New("telemetry is not set up; run navigara wizard")
	}
	attrs, err := resourceAttributes(env["OTEL_RESOURCE_ATTRIBUTES"], email)
	if err != nil || email == "" {
		attrs = ""
	}
	expected := claudeTelemetryEnv(endpoint, token, attrs)
	for _, key := range slices.Sorted(maps.Keys(expected)) {
		if env[key] != expected[key] {
			return toolLink{}, fmt.Errorf("%s in settings.json is not what setup writes; run navigara wizard", key)
		}
	}
	if settings.HeadersHelper != nil {
		return toolLink{}, errors.New("otelHeadersHelper in settings.json is not what setup writes; run navigara wizard")
	}
	shell := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		shell[key] = value
	}
	for _, env := range []map[string]string{env, shell} {
		if err := validateClaudeEnv(env, endpoint, token); err != nil {
			return toolLink{}, err
		}
	}
	return toolLink{endpoint: endpoint, token: token, email: email}, nil
}

func codexLink(home, shell string) (toolLink, error) {
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
	var block struct {
		OTEL struct {
			Metrics struct {
				OTLP struct {
					Endpoint string            `toml:"endpoint"`
					Headers  map[string]string `toml:"headers"`
				} `toml:"otlp-http"`
			} `toml:"metrics_exporter"`
		} `toml:"otel"`
	}
	if start == end || toml.Unmarshal(data[start:end], &block) != nil {
		return toolLink{}, errors.New("no Navigara block in config.toml; run navigara wizard")
	}
	endpoint := strings.TrimSuffix(block.OTEL.Metrics.OTLP.Endpoint, "/v1/metrics")
	token := strings.TrimPrefix(block.OTEL.Metrics.OTLP.Headers["Authorization"], "Bearer ")
	got, want := strings.Split(string(data[start:end]), "\n"), strings.Split(codexBlock(endpoint, token), "\n")
	table := ""
	for i, line := range want {
		key, _, _ := strings.Cut(line, " =")
		if strings.HasPrefix(line, "[") {
			table, key = line, ""
		}
		if i >= len(got) || got[i] != line {
			return toolLink{}, fmt.Errorf("%s in config.toml is not what setup writes; run navigara wizard", strings.TrimSpace(table+" "+key))
		}
	}
	if len(got) != len(want) {
		return toolLink{}, errors.New("the Navigara block in config.toml is not what setup writes; run navigara wizard")
	}
	email, err := codexEmail(shell)
	if err != nil {
		return toolLink{}, err
	}
	return toolLink{endpoint: endpoint, token: token, email: email}, nil
}

// codexEmail reads the email that setup exports to Codex from the profile of the user's shell.
// The block must be exactly what setup writes for that email.
func codexEmail(shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	profile, prefix, suffix := filepath.Join(home, ".bashrc"), "enduser.id=\"'", "'\n"
	unquote, write := strings.NewReplacer(`'"'"'`, "'"), shellSettings
	switch shell {
	case "zsh":
		profile = filepath.Join(firstNonEmpty(os.Getenv("ZDOTDIR"), home), ".zshrc")
	case "bash":
	case "fish":
		profile, prefix, suffix = fishConfig(home), "'enduser.id=", "')\n"
		unquote, write = strings.NewReplacer(`\\`, `\`, `\'`, "'"), fishSettings
	default:
		return "", errors.New("no Codex email: $SHELL is not bash, zsh, or fish; use --shell")
	}
	missing := fmt.Errorf("no Navigara email block in %s; run navigara wizard", profile)
	data, err := os.ReadFile(profile)
	if err != nil {
		return "", missing
	}
	start, end, err := managedRange(data)
	if err != nil || start == end {
		return "", missing
	}
	block := string(data[start:end])
	_, value, found := strings.Cut(block, prefix)
	value, _, closed := strings.Cut(value, suffix)
	email := unquote.Replace(value)
	if expected, err := write(nil, email); !found || !closed || err != nil || string(expected) != block {
		return "", fmt.Errorf("the Navigara email block in %s is changed; run navigara wizard", profile)
	}
	return email, nil
}

func enduserID(attrs string) string {
	for _, attr := range strings.Split(attrs, ",") {
		if key, value, ok := strings.Cut(attr, "="); ok && strings.TrimSpace(key) == "enduser.id" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
