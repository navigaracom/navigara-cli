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
	}
	if opts.source == "all" || opts.source == "codex" {
		if err := add(filepath.Join(opts.codexHome, "config.toml"), func(b []byte) ([]byte, error) { return codexSettings(b, endpoint, opts.token) }); err != nil {
			return nil, err
		}
		var profiles []string
		switch filepath.Base(os.Getenv("SHELL")) {
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
		default:
			return nil, fmt.Errorf("Codex identity setup supports bash and zsh; unsupported SHELL")
		}
		for _, path := range profiles {
			if err := add(path, func(b []byte) ([]byte, error) { return shellSettings(b, opts.email) }); err != nil {
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
