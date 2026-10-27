package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
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
		if managed := claudeManagedBy(); managed != "" {
			return nil, fmt.Errorf("Claude Code is managed by %s; ask your administrator to configure telemetry", managed)
		}
		if err := add(filepath.Join(opts.claudeHome, "settings.json"), func(b []byte) ([]byte, error) { return claudeSettings(b, endpoint, opts.token, opts.email) }); err != nil {
			return nil, err
		}
		f := files[len(files)-1]
		f.notes, f.conflict = settingsChanges(f.before, f.after, json.Unmarshal), claudeTelemetryConflict(f.before, endpoint, opts.token)
	}
	if opts.source == "all" || opts.source == "codex" {
		if err := add(filepath.Join(opts.codexHome, "config.toml"), func(b []byte) ([]byte, error) {
			return codexSettings(b, endpoint, opts.token)
		}); err != nil {
			return nil, err
		}
		codex := files[len(files)-1]
		codex.notes, codex.conflict = settingsChanges(codex.before, codex.after, toml.Unmarshal), codexTelemetryConflict(codex.before, endpoint)
		var profiles []string
		profile := shellSettings
		shell := firstNonEmpty(opts.shell, filepath.Base(os.Getenv("SHELL")))
		if runtime.GOOS == "windows" {
			shell = "windows"
		}
		switch shell {
		case "windows":
			machine := getMachineEnv("OTEL_RESOURCE_ATTRIBUTES")
			f, err := planEnv("OTEL_RESOURCE_ATTRIBUTES", func(v string) (string, error) {
				if v == "" {
					// Start from the machine value, which the new user value hides.
					v = machine
				}
				return resourceAttributes(v, opts.email)
			})
			if err != nil {
				return nil, err
			}
			f.owner = opts.email
			if prev := enduserID(string(f.before)); prev != "" && prev != getEnvOwner(f.env) {
				f.notes = append(f.notes, "Replaces your enduser.id="+prev+"; config remove restores it")
			}
			if !f.existed && machine != "" {
				f.notes = append(f.notes, "Copies the machine OTEL_RESOURCE_ATTRIBUTES; later machine changes apply after config remove")
				if id := enduserID(machine); id != "" {
					f.notes = append(f.notes, "Hides the machine enduser.id="+id)
				}
			}
			if opts.shell != "" {
				f.warning = "--shell is ignored on Windows: the Codex email goes to the user environment variable OTEL_RESOURCE_ATTRIBUTES."
			}
			files = append(files, f)
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

// claudeManagedSettings is the path where an administrator manages Claude Code.
func claudeManagedSettings() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode/managed-settings.json"
	case "windows":
		// ProgramW6432 is the 64-bit folder also under a 32-bit parent, such as Intune's default host.
		return filepath.Join(firstNonEmpty(os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles"), `C:\Program Files`), "ClaudeCode", "managed-settings.json")
	}
	return "/etc/claude-code/managed-settings.json"
}

// claudeManagedBy names the managed settings that override Claude Code's user settings, or is
// "" when there are none: the managed file, its managed-settings.d drop-ins, or a registry policy.
func claudeManagedBy() string {
	managed := claudeManagedSettings()
	if _, err := os.Stat(managed); err == nil {
		return managed
	}
	if dropIns, _ := filepath.Glob(filepath.Join(filepath.Dir(managed), "managed-settings.d", "*.json")); len(dropIns) > 0 {
		return dropIns[0]
	}
	return claudePolicy()
}

func fishConfig(home string) string {
	return filepath.Join(firstNonEmpty(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config")), "fish", "config.fish")
}

// removePlans plans the removal of every Navigara block and key. It plans only
// files that exist. known lists the Navigara origins that prove an older
// Claude layout without the marker.
func removePlans(opts options, home string, known []string) ([]*setupFile, error) {
	var files []*setupFile
	// A file without Navigara settings is only reported. It may be a symlink,
	// for example a dotfile manager's profile, which setup never writes.
	add := func(path string, transform func([]byte) ([]byte, error)) error {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var data []byte
		if info.Mode().IsRegular() && info.Size() <= 4<<20 {
			if data, err = os.ReadFile(path); err != nil {
				return err
			}
		}
		if out, err := transform(data); data != nil && err == nil && bytes.Equal(out, data) {
			files = append(files, &setupFile{path: path, before: data, after: data, existed: true})
			return nil
		}
		f, err := planFile(path, transform)
		if err == nil {
			files = append(files, f)
		}
		return err
	}
	shell := removeManagedBlock
	var codexWarning string
	targets := []struct {
		path      string
		transform func([]byte) ([]byte, error)
	}{
		{filepath.Join(opts.claudeHome, "settings.json"), func(b []byte) ([]byte, error) { return removeClaudeSettings(b, known) }},
		{filepath.Join(opts.codexHome, "config.toml"), func(b []byte) (out []byte, err error) {
			out, codexWarning, err = removeCodexSettings(b, known)
			return out, err
		}},
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
		before := len(files)
		if err := add(target.path, target.transform); err != nil {
			return nil, err
		}
		if i == 1 && len(files) > before {
			files[before].warning = codexWarning
		}
		if i == 0 && len(files) == 1 {
			if !bytes.Equal(files[0].before, files[0].after) {
				files[0].notes = claudeKeptKeys(files[0].before, files[0].after)
			}
			if origin := claudeUnknownOrigin(files[0].before, known); origin != "" {
				files[0].warning = "Claude Code sends telemetry to " + origin + ", which is not a known Navigara address; nothing removed. Use --host " + origin + " if it is your Navigara instance."
			}
		}
	}
	if runtime.GOOS == "windows" && opts.source != "claude" {
		f, err := planEnv("OTEL_RESOURCE_ATTRIBUTES", func(v string) (string, error) {
			// Remove only the email that setup wrote; a changed one belongs to the user.
			if id := enduserID(v); id == "" || id != getEnvOwner("OTEL_RESOURCE_ATTRIBUTES") {
				return v, nil
			}
			out, err := resourceAttributes(v, "")
			if err != nil {
				return v, nil // a value the CLI cannot parse stays as the user left it
			}
			// Setup created the variable from this copy of the machine value. Unless the user
			// added to it, deleting it shows the current machine value again.
			if copied := getEnvOwner("OTEL_RESOURCE_ATTRIBUTES" + createdSuffix); copied != "" && out == copied {
				return "", nil
			}
			return resourceAttributes(out, getEnvOwner("OTEL_RESOURCE_ATTRIBUTES"+previousSuffix))
		})
		if err != nil {
			return nil, err
		}
		switch {
		case !f.unchanged():
			files = append(files, f)
		case getEnvOwner(f.env) != "":
			// The email is gone or changed; only the records of the old setup are left.
			f.dropRecords = true
			f.notes = append(f.notes, `Clears the Navigara records in HKCU\Software\Navigara`)
			files = append(files, f)
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
		f.after, f.notes, f.conflict, f.warning = refreshed[i].after, refreshed[i].notes, refreshed[i].conflict, refreshed[i].warning
	}
	return nil
}
