package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
)

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
		if !strings.Contains(view, "Connect this computer to Navigara") || !strings.Contains(view, `"dev@example.com"`) || !strings.Contains(view, `"Acme"`) || !strings.Contains(view, `"https://app.example"`) || strings.Contains(view, "secret") || !strings.Contains(view, "All available history") || !strings.Contains(view, "Continue") {
			t.Fatalf("default account or history is invisible: %s", view)
		}
		narrow := newWizardForm(account, available, &settings, &selected, &history).WithWidth(40)
		narrow.Init()
		narrow.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
		narrowView := ansi.Strip(narrow.View())
		expected := []string{"dev@example.com", "All available history", "Continue"}
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
			t.Fatal("focused Continue did not handle Enter")
		} else if _, cmd = form.Update(cmd()); cmd != nil {
			if _, next := form.Update(cmd()); next != nil {
				t.Fatal("Continue did not complete the form")
			}
		}
		if form.State != huh.StateCompleted {
			t.Fatalf("Enter did not complete defaults form: %v", form.State)
		}
		if !form.GetBool("continue") {
			t.Fatal("Enter did not retain the default Continue action")
		}
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
			skip, apply := action != "cancel", action == "connect"
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
			posts := 0
			srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts++
				if !apply {
					t.Error("ingestion request without consent")
				}
				data, _ := io.ReadAll(r.Body)
				if bytes.Contains(data, []byte(`"codex"`)) {
					t.Error("uploaded history for skipped Codex")
				}
				w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
				io.WriteString(w, `{}`)
			}))
			defer srv.Close()
			answer := "n"
			if skip {
				answer = "y"
			}
			consent := "n"
			if apply {
				consent = "y"
			}
			var out, errs bytes.Buffer
			code := runSetup(nil, strings.NewReader("y\ny\n\nall\n"+answer+"\ny\n"+consent+"\n"), &out, &errs)
			if code != 0 {
				t.Fatalf("wizard failed rather than offering recovery: %d %s", code, &errs)
			}
			if skip && (!strings.Contains(out.String(), "2 sessions") || !strings.Contains(out.String(), "Keep new activity in sync: Claude Code")) {
				t.Fatalf("preview did not reflect the remaining tool: %s", &out)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, existing) {
				t.Fatalf("conflicting settings changed: %q %v", got, err)
			}
			data, err := os.ReadFile(filepath.Join(claude, "settings.json"))
			if apply {
				if err != nil || !bytes.Contains(data, []byte("test-secret")) || posts < 2 {
					t.Fatal("remaining Claude tool was not configured and imported")
				}
			} else if !os.IsNotExist(err) || posts != 0 {
				t.Fatal("cancel/decline changed settings or contacted ingestion")
			}
		})
	}
}
