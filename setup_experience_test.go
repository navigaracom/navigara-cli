package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestWizardFormHeaderFitsFortyColumns(t *testing.T) {
	account := accountConfig{
		Email:            "dev@example.com",
		OrganizationName: "Example Developer Organization",
		Host:             "https://dev.navigara.com",
	}
	settings := wizardSettings{source: "all", email: account.Email, history: "all"}
	selected, history := []string{"claude", "codex"}, settings.history
	form := newWizardForm(account, []string{"claude", "codex"}, &settings, &selected, &history).WithWidth(40)
	form.Init()
	form.Update(tea.WindowSizeMsg{Width: 40, Height: 24})

	view := ansi.Strip(form.View())
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 40 {
			t.Fatalf("wizard line is %d columns wide: %q", width, line)
		}
	}
	compactView := strings.Join(strings.Fields(view), "")
	for _, value := range []string{
		account.Email,
		account.OrganizationName,
		account.Host,
		"All available history",
		"Last 30 days",
		"Last 7 days",
		"Last 24 hours",
		"Do not import history",
		"Custom duration",
		"Import past sessions",
		"[x] Claude Code",
		"[x] Codex",
		"Connect",
		"Enter to connect; Shift+Tab to edit.",
	} {
		if !strings.Contains(compactView, strings.Join(strings.Fields(value), "")) {
			t.Fatalf("narrow wizard hides %q: %s", value, view)
		}
	}
	injected := account
	injected.OrganizationName = "Acme\nForged prompt\x1b[2J"
	form = newWizardForm(injected, []string{"claude", "codex"}, &settings, &selected, &history).WithWidth(72)
	form.Init()
	form.Update(tea.WindowSizeMsg{Width: 72, Height: 24})
	injectedView := ansi.Strip(form.View())
	if strings.Contains(injectedView, "\x1b") || strings.Contains(injectedView, "\nForged prompt") || !strings.Contains(injectedView, `Acme\nForged prompt\x1b[2J`) {
		t.Fatalf("organization value altered the form: %q", injectedView)
	}
}

func TestWizardFormShowsDetectedToolsAndAccountDefaults(t *testing.T) {
	account := accountConfig{Email: "dev@example.com", OrganizationName: "Acme", Host: "https://app.example", IngestToken: "secret"}
	for _, available := range [][]string{{"claude", "codex"}, {"claude"}, {"codex"}} {
		settings := wizardSettings{source: setupSource(available), email: "dev@example.com", history: "all"}
		selected, history := selectedTools(available, settings.source), settings.history
		form := newWizardForm(account, available, &settings, &selected, &history).WithWidth(72)
		form.Init()
		form.Update(tea.WindowSizeMsg{Width: 72, Height: 24})
		view := ansi.Strip(form.View())
		for _, source := range available {
			if !strings.Contains(view, "[x] "+setupToolName(source)) {
				t.Fatalf("detected tool is invisible: %s", view)
			}
		}
		if !strings.Contains(view, "Connect this computer to Navigara") || !strings.Contains(view, "dev@example.com") || !strings.Contains(view, "Acme") || !strings.Contains(view, "https://app.example") || strings.Contains(view, "secret") || !strings.Contains(view, "All available history") || !strings.Contains(view, "Import past sessions") || !strings.Contains(view, "Connect") {
			t.Fatalf("default account or history is invisible: %s", view)
		}
		if strings.Contains(strings.Join(available, ","), "claude") != strings.Contains(view, "Claude sends prompt text and tool arguments.") {
			t.Fatalf("Claude disclosure did not match available tools: %s", view)
		}
		if strings.Contains(view, "> ") || !strings.Contains(view, "● All available history") {
			t.Fatalf("inactive fields look focused or lose their selection: %s", view)
		}
		for _, label := range []string{"Last 30 days", "Last 7 days", "Last 24 hours", "Do not import history", "Custom duration"} {
			if !strings.Contains(view, label) {
				t.Fatalf("history choice is hidden: %s", view)
			}
		}
		narrow := newWizardForm(account, available, &settings, &selected, &history).WithWidth(40)
		narrow.Init()
		narrow.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
		narrowView := ansi.Strip(narrow.View())
		expected := []string{"dev@example.com", "All available history", "Import past sessions", "Connect"}
		for _, tool := range available {
			expected = append(expected, "[x] "+setupToolName(tool))
		}
		for _, label := range expected {
			if !strings.Contains(narrowView, label) {
				t.Fatalf("narrow wizard hides %s: %s", label, narrowView)
			}
		}
		for name, key := range map[string]tea.Key{
			"q":      {Code: 'q', Text: "q"},
			"up":     {Code: tea.KeyUp},
			"space":  {Code: tea.KeySpace},
			"escape": {Code: tea.KeyEscape},
		} {
			t.Run(name, func(t *testing.T) {
				settings := wizardSettings{source: setupSource(available), email: "dev@example.com", history: "all"}
				selected, history := selectedTools(available, settings.source), settings.history
				form := newWizardForm(account, available, &settings, &selected, &history)
				dispatchWizardCommand(form, form.Init())
				dispatchWizardMessage(form, tea.KeyPressMsg(key))
				if form.State != huh.StateNormal {
					t.Fatalf("%s submitted the form: %v", name, form.State)
				}
			})
		}
		if _, cmd := form.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); cmd == nil {
			t.Fatal("focused Connect did not handle Enter")
		} else if _, cmd = form.Update(cmd()); cmd != nil {
			if _, next := form.Update(cmd()); next != nil {
				t.Fatal("Connect did not complete the form")
			}
		}
		if form.State != huh.StateCompleted {
			t.Fatalf("Enter did not complete defaults form: %v", form.State)
		}
		if !form.GetBool("continue") {
			t.Fatal("Enter did not retain the default Connect action")
		}
	}
}

func TestWizardFormKeepsSavedSkipHistory(t *testing.T) {
	account := accountConfig{Email: "dev@example.com"}
	settings := wizardSettings{source: "claude", email: account.Email, history: "skip"}
	selected, history := []string{"claude"}, settings.history
	form := newWizardForm(account, []string{"claude"}, &settings, &selected, &history).WithWidth(72)
	form.Init()
	form.Update(tea.WindowSizeMsg{Width: 72, Height: 24})
	view := ansi.Strip(form.View())
	if !strings.Contains(view, "● Do not import history") {
		t.Fatalf("saved skip history is not selected: %q", view)
	}
	if _, cmd := form.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})); cmd == nil {
		t.Fatal("focused Connect did not handle Enter")
	} else {
		dispatchWizardCommand(form, cmd)
	}
	if form.State != huh.StateCompleted || history != "skip" {
		t.Fatalf("saved skip was not retained: state=%v history=%q", form.State, history)
	}
}

func dispatchWizardMessage(form *huh.Form, message tea.Msg) {
	_, command := form.Update(message)
	dispatchWizardCommand(form, command)
}

func dispatchWizardCommand(form *huh.Form, command tea.Cmd) {
	if command == nil {
		return
	}
	switch message := command().(type) {
	case tea.BatchMsg:
		for _, command := range message {
			dispatchWizardCommand(form, command)
		}
	default:
		dispatchWizardMessage(form, message)
	}
}

func TestWizardEnterKeepsAccountToolsAndAllHistory(t *testing.T) {
	var out bytes.Buffer
	p := newSetupInput(strings.NewReader("\n\n\n\n"), &out)
	settings, err := wizardPreferences(accountConfig{Email: "dev@example.com"}, []string{"claude", "codex"}, p)
	if err != nil || settings.source != "all" || settings.email != "dev@example.com" || settings.history != "all" {
		t.Fatalf("default preferences = %#v, %v", settings, err)
	}
}

func TestWizardEditKeepsPreviousAnswersOnBlankPlainInput(t *testing.T) {
	var out bytes.Buffer
	p := newSetupInput(strings.NewReader("\n\n\n\n"), &out)
	settings, err := editWizardPreferences(accountConfig{Email: "dev@example.com"}, []string{"claude", "codex"}, wizardSettings{
		source: "claude", email: "dev@example.com", history: "12h",
	}, p)
	if err != nil {
		t.Fatal(err)
	}
	if settings.source != "claude" || settings.email != "dev@example.com" || settings.history != "12h" {
		t.Fatalf("blank edit lost saved values: %#v", settings)
	}
	if !strings.Contains(out.String(), "Connect Claude Code (sends prompt text and tool arguments)") {
		t.Fatalf("plain Claude question lacks disclosure: %q", out.String())
	}
}

func TestOrganizationNamesCannotAlterTerminalChoices(t *testing.T) {
	var out bytes.Buffer
	p := newSetupInput(strings.NewReader("2\n"), &out)
	orgs := []organization{{ID: "one", Name: "One"}, {ID: "two", Name: "Two\nForged prompt\x1b[2J"}}
	selected, err := chooseOrganization(orgs, "", true, p)
	if err != nil || selected.ID != "two" {
		t.Fatalf("organization selection = %v, %v", selected, err)
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "\nForged prompt") {
		t.Fatalf("organization name altered terminal choices: %q", out.String())
	}
}

func TestWizardCanSkipConflictingToolWithoutChangingSettings(t *testing.T) {
	for _, action := range []string{"cancel", "skip", "connect"} {
		t.Run(action, func(t *testing.T) {
			apply := action == "connect"
			claude, codex := fixtureHomes(t)
			home := filepath.Dir(claude)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("CLAUDE_CONFIG_DIR", claude)
			t.Setenv("CODEX_HOME", codex)
			t.Setenv("SHELL", "/bin/zsh")
			t.Setenv("ZDOTDIR", home)
			path := filepath.Join(codex, "config.toml")
			existing := []byte("[otel]\nexporter = \"console\"\n")
			if err := os.WriteFile(path, existing, 0600); err != nil {
				t.Fatal(err)
			}
			fixture, err := os.ReadFile("internal/session/testdata/codex-root.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(codex, "sessions"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(codex, "sessions", "rollout-existing.jsonl"), fixture, 0600); err != nil {
				t.Fatal(err)
			}
			var posts atomic.Int32 // Uploads run concurrently.
			srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts.Add(1)
				if !apply {
					t.Error("ingestion request without consent")
				}
				data := mustBody(r)
				if bytes.Contains(data, []byte(`"codex"`)) {
					t.Error("uploaded history for skipped Codex")
				}
				w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
				io.WriteString(w, `{}`)
			}))
			defer srv.Close()
			skip, closeSessions := "n", "n"
			if action != "cancel" {
				skip = "y"
			}
			if action == "connect" {
				closeSessions = "y"
			}
			input := "y\ny\n\nall\n" + skip + "\n"
			if action != "cancel" {
				input += closeSessions + "\n"
			}
			var out, errs bytes.Buffer
			code := runSetup(context.Background(), nil, strings.NewReader(input), &out, &errs)
			if code != 0 {
				t.Fatalf("wizard failed rather than offering recovery: %d %s", code, &errs)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, existing) {
				t.Fatalf("conflicting settings changed: %q %v", got, err)
			}
			data, err := os.ReadFile(filepath.Join(claude, "settings.json"))
			if apply {
				if err != nil || !bytes.Contains(data, []byte("test-secret")) || posts.Load() < 2 {
					t.Fatal("remaining Claude tool was not configured and imported")
				}
			} else if !os.IsNotExist(err) || posts.Load() != 0 {
				t.Fatal("cancel/decline changed settings or contacted ingestion")
			}
		})
	}
}

func TestWizardReplacesExistingCodexTelemetryAfterContinue(t *testing.T) {
	for action, connect := range map[string]bool{"cancel": false, "connect": true} {
		t.Run(action, func(t *testing.T) {
			claude, codex := fixtureHomes(t)
			home := filepath.Dir(claude)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("CLAUDE_CONFIG_DIR", claude)
			t.Setenv("CODEX_HOME", codex)
			t.Setenv("SHELL", "/bin/zsh")
			t.Setenv("ZDOTDIR", home)
			original, err := os.ReadFile("testdata/codex-existing-telemetry.toml")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(codex, "config.toml")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			var out, errs bytes.Buffer
			requests := 0
			srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if !connect || !strings.Contains(out.String(), "Connecting your tools...") {
					t.Error("ingestion request before confirmation")
				}
				w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
				io.WriteString(w, `{}`)
			}))
			defer srv.Close()
			final := "n"
			if connect {
				final = "y"
			}
			input := "n\ny\n\nskip\ny\n" + final + "\n"
			if code := runSetup(context.Background(), nil, strings.NewReader(input), &out, &errs); code != 0 {
				t.Fatalf("wizard = %d: %s", code, &errs)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			backups, err := filepath.Glob(filepath.Join(codex, ".navigara-backup-*", "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if !connect {
				if requests != 0 || len(backups) != 0 || !bytes.Equal(got, original) {
					t.Fatal("cancel changed settings, created a backup, or contacted ingestion")
				}
				return
			}
			if requests == 0 || len(backups) != 1 || !bytes.Contains(got, []byte("test-secret")) || bytes.Contains(got, []byte("fixture-old")) {
				t.Fatal("Connect did not refresh the credential and back up the original")
			}
			backup, err := os.ReadFile(backups[0])
			if err != nil || !bytes.Equal(backup, original) {
				t.Fatal("backup did not retain the original settings")
			}
			info, err := os.Stat(filepath.Dir(backups[0]))
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatal("backup directory is not private")
			}
		})
	}
}
