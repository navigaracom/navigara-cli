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
	"runtime"
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
		info, err := os.Stat(tool.home)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stdout, "%s: not found\n", tool.name)
			continue
		}
		if err == nil && !info.IsDir() {
			err = fmt.Errorf("%s is not a directory", tool.home)
		}
		if err != nil {
			fmt.Fprintf(stdout, "%s: cannot check its home: %v\n", tool.name, err)
			ok = false
			continue
		}
		found = true
		if at, imported := c.Imports[c.OrganizationID+"/"+tool.key]; imported && c.OrganizationID != "" {
			fmt.Fprintf(stdout, "%s: last import %s\n", tool.name, at.Local().Format(time.RFC3339))
		}
		link, err := tool.read(tool.home)
		base, isOTLP := strings.CutSuffix(link.endpoint, "/otlp")
		if err == nil && !isOTLP {
			err = fmt.Errorf("endpoint %s does not end in /otlp; run navigara wizard", link.endpoint)
		}
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
		if err := newSender(base, link.token).checkSetup(ctx); err != nil {
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
	if validEmail(email) != nil {
		return toolLink{}, errors.New("OTEL_RESOURCE_ATTRIBUTES in settings.json has no valid enduser.id; run navigara wizard")
	}
	attrs, err := resourceAttributes(env["OTEL_RESOURCE_ATTRIBUTES"], email)
	if err != nil {
		attrs = ""
	}
	expected := claudeTelemetryEnv(endpoint, token, attrs)
	for _, key := range slices.Sorted(maps.Keys(expected)) {
		if env[key] != expected[key] {
			return toolLink{}, fmt.Errorf("%s in settings.json is not what setup writes; run navigara wizard", key)
		}
	}
	for _, key := range claudeFileTelemetryOverrides {
		if _, ok := env[key]; ok {
			return toolLink{}, fmt.Errorf("%s in settings.json is not what setup writes; run navigara wizard", key)
		}
	}
	if settings.HeadersHelper != nil {
		return toolLink{}, errors.New("otelHeadersHelper in settings.json is not what setup writes; run navigara wizard")
	}
	managed := "/etc/claude-code/managed-settings.json"
	if runtime.GOOS == "darwin" {
		managed = "/Library/Application Support/ClaudeCode/managed-settings.json"
	}
	if _, err := os.Stat(managed); err == nil {
		return toolLink{}, fmt.Errorf("Claude Code is managed by %s; ask your administrator to configure telemetry", managed)
	}
	shell := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		shell[key] = value
	}
	if err := validateClaudeEnv(shell, endpoint, token); err != nil {
		return toolLink{}, err
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
	if hasOTELTOML(append(append([]byte(nil), data[:start]...), data[end:]...)) {
		return toolLink{}, errors.New("config.toml has telemetry settings outside the Navigara block; run navigara wizard")
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
	var profiles []string
	prefix, suffix := "enduser.id=\"'", "'\n"
	unquote, write := strings.NewReplacer(`'"'"'`, "'"), shellSettings
	switch shell {
	case "zsh":
		profiles = []string{filepath.Join(firstNonEmpty(os.Getenv("ZDOTDIR"), home), ".zshrc")}
	case "bash":
		login := filepath.Join(home, ".bash_profile")
		for _, name := range []string{".bash_profile", ".bash_login", ".profile"} {
			if _, err := os.Stat(filepath.Join(home, name)); err == nil {
				login = filepath.Join(home, name)
				break
			}
		}
		profiles = []string{filepath.Join(home, ".bashrc"), login}
	case "fish":
		profiles, prefix, suffix = []string{fishConfig(home)}, "'enduser.id=", "')\n"
		unquote, write = strings.NewReplacer(`\\`, `\`, `\'`, "'"), fishSettings
	default:
		return "", errors.New("no Codex email: $SHELL is not bash, zsh, or fish; use --shell")
	}
	var email string
	for _, profile := range profiles {
		data, err := os.ReadFile(profile)
		if err != nil {
			return "", fmt.Errorf("no Navigara email block in %s; run navigara wizard", profile)
		}
		start, end, err := managedRange(data)
		if err != nil || start == end {
			return "", fmt.Errorf("no Navigara email block in %s; run navigara wizard", profile)
		}
		block := string(data[start:end])
		_, value, found := strings.Cut(block, prefix)
		value, _, closed := strings.Cut(value, suffix)
		value = unquote.Replace(value)
		if expected, err := write(nil, value); !found || !closed || err != nil || string(expected) != block || email != "" && value != email {
			return "", fmt.Errorf("the Navigara email block in %s is changed; run navigara wizard", profile)
		}
		email = value
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
