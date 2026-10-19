package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func setupPlans(opts options, home string) ([]*setupFile, error) {
	var files []*setupFile
	add := func(path string, transform func([]byte) ([]byte, error)) error {
		f, err := planFile(path, transform)
		if err == nil {
			files = append(files, f)
		}
		return err
	}
	endpoint := otlpBase(opts.url)
	if opts.source == "all" || opts.source == "claude" {
		env := map[string]string{}
		for _, entry := range os.Environ() {
			key, value, _ := strings.Cut(entry, "=")
			env[key] = value
		}
		if err := validateClaudeEnv(env, endpoint, opts.token); err != nil {
			return nil, err
		}
		managed := "/etc/claude-code/managed-settings.json"
		if runtime.GOOS == "darwin" {
			managed = "/Library/Application Support/ClaudeCode/managed-settings.json"
		}
		if _, err := os.Stat(managed); err == nil {
			return nil, fmt.Errorf("Claude Code is managed by %s; ask your administrator to configure telemetry", managed)
		}
		if err := add(filepath.Join(opts.claudeHome, "settings.json"), func(b []byte) ([]byte, error) { return claudeSettings(b, endpoint, opts.token, opts.email) }); err != nil {
			return nil, err
		}
		f := files[len(files)-1]
		f.notes, f.conflict = claudeTelemetryChanges(f.before, endpoint, opts.token)
	}
	if opts.source == "all" || opts.source == "codex" {
		if err := add(filepath.Join(opts.codexHome, "config.toml"), func(b []byte) ([]byte, error) {
			return codexSettings(b, endpoint, opts.token)
		}); err != nil {
			return nil, err
		}
		codex := files[len(files)-1]
		codex.notes, codex.conflict = codexTelemetryChanges(codex.before, endpoint)
		var profiles []string
		profile := shellSettings
		switch firstNonEmpty(opts.shell, filepath.Base(os.Getenv("SHELL"))) {
		case "zsh":
			profiles = []string{filepath.Join(firstNonEmpty(os.Getenv("ZDOTDIR"), home), ".zshrc")}
		case "bash":
			profiles = []string{filepath.Join(home, ".bashrc")}
			login := filepath.Join(home, ".bash_profile")
			for _, name := range []string{".bash_profile", ".bash_login", ".profile"} {
				if _, err := os.Stat(filepath.Join(home, name)); err == nil {
					login = filepath.Join(home, name)
					break
				}
			}
			profiles = append(profiles, login)
		case "fish":
			if _, err := os.Stat(filepath.Dir(fishConfig(home))); err != nil {
				codex.warning = filepath.Dir(fishConfig(home)) + " does not exist. Codex sessions do not show your email until you add this line to config.fish:\n  set -gx OTEL_RESOURCE_ATTRIBUTES " + fishQuote("enduser.id="+opts.email)
				break
			}
			profiles = []string{fishConfig(home)}
			profile = fishSettings
		default:
			codex.warning = "Could not find a bash, zsh, or fish shell. Codex sessions do not show your email until you add this line to your shell profile:\n  export OTEL_RESOURCE_ATTRIBUTES=enduser.id=" + shellQuote(opts.email) + "\nOr re-run with --shell bash, --shell zsh, or --shell fish."
		}
		for _, path := range profiles {
			if err := add(path, func(b []byte) ([]byte, error) { return profile(b, opts.email) }); err != nil {
				return nil, err
			}
		}
	}
	for _, f := range files {
		if err := f.check(); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func planConflict(files []*setupFile) error {
	for _, f := range files {
		if f.conflict != nil {
			return f.conflict
		}
	}
	return nil
}

func fishConfig(home string) string {
	return filepath.Join(firstNonEmpty(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config")), "fish", "config.fish")
}

// removePlans plans the removal of every Navigara block and key. It plans only
// files that exist.
func removePlans(opts options, home string) ([]*setupFile, error) {
	var files []*setupFile
	add := func(path string, transform func([]byte) ([]byte, error)) error {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return nil
		}
		f, err := planFile(path, transform)
		if err == nil {
			files = append(files, f)
		}
		return err
	}
	shell := func(b []byte) ([]byte, error) { return removeManagedBlock(b, managedRange) }
	targets := []struct {
		path      string
		transform func([]byte) ([]byte, error)
	}{
		{filepath.Join(opts.claudeHome, "settings.json"), removeClaudeSettings},
		{filepath.Join(opts.codexHome, "config.toml"), func(b []byte) ([]byte, error) { return removeManagedBlock(b, tomlManagedRange) }},
		{filepath.Join(firstNonEmpty(os.Getenv("ZDOTDIR"), home), ".zshrc"), shell},
		{filepath.Join(home, ".bashrc"), shell},
		{filepath.Join(home, ".bash_profile"), shell},
		{filepath.Join(home, ".bash_login"), shell},
		{filepath.Join(home, ".profile"), shell},
		{fishConfig(home), shell},
	}
	for i, target := range targets {
		if (i == 0 && opts.source == "codex") || (i > 0 && opts.source == "claude") {
			continue
		}
		if err := add(target.path, target.transform); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// Refresh token-dependent output without replacing the original file snapshots.
func refreshSetupPlans(files []*setupFile, opts options, home string) error {
	refreshed, err := setupPlans(opts, home)
	if err != nil {
		return err
	}
	if len(refreshed) != len(files) {
		return fmt.Errorf("settings plans changed; run the command again")
	}
	for i, f := range files {
		if refreshed[i].path != f.path || refreshed[i].existed != f.existed || !bytes.Equal(refreshed[i].before, f.before) {
			return fmt.Errorf("settings plans changed; run the command again")
		}
		f.after = refreshed[i].after
	}
	return nil
}
