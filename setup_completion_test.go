package main

import (
	"bytes"
	"errors"
	"testing"
)

func TestFinishScreenGolden(t *testing.T) {
	for _, test := range []struct {
		name     string
		sessions int
		tools    []string
		want     string
	}{
		{"both tools", 12, []string{"claude", "codex"}, "\nYou're connected to Navigara\nImported 12 past sessions.\nNew sessions sync automatically after you restart Claude Code / Codex in a new terminal.\nCheck: open https://app.example/welcome after your next session.\n"},
		{"history skipped", -1, []string{"codex"}, "\nYou're connected to Navigara\nNew sessions sync automatically after you restart Codex in a new terminal.\nCheck: open https://app.example/welcome after your next session.\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			(&setupInput{out: &out}).completed(test.sessions, test.tools, welcomeURL("https://app.example"))
			if out.String() != test.want {
				t.Fatalf("finish screen:\n%q\nwant:\n%q", out.String(), test.want)
			}
		})
	}
}

func TestContinueSetupOpensWelcomeOnlyAfterYes(t *testing.T) {
	for _, test := range []struct {
		name    string
		confirm func() (bool, error)
		opened  bool
	}{
		{"yes", func() (bool, error) { return true, nil }, true},
		{"no", func() (bool, error) { return false, nil }, false},
		{"cancelled", func() (bool, error) { return true, errors.New("input cancelled") }, false},
		{"no terminal", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var opened []string
			continueSetup(&setupInput{out: &bytes.Buffer{}}, "https://app.example/welcome", test.confirm, func(raw string) error {
				opened = append(opened, raw)
				return nil
			})
			if (len(opened) == 1 && opened[0] == "https://app.example/welcome") != test.opened || len(opened) > 1 {
				t.Fatalf("opened = %v", opened)
			}
		})
	}
}

func TestContinueSetupBrowserFailureKeepsLink(t *testing.T) {
	var out bytes.Buffer
	continueSetup(&setupInput{out: &out}, "https://app.example/welcome", func() (bool, error) { return true, nil }, func(string) error {
		return errors.New("browser unavailable")
	})
	if got := out.String(); got != "Your browser couldn't be opened. Open https://app.example/welcome yourself.\n" {
		t.Fatalf("failure output = %q", got)
	}
}

func TestWelcomeURLDoesNotInventHost(t *testing.T) {
	if got := welcomeURL(""); got != "" {
		t.Fatalf("welcomeURL(\"\") = %q", got)
	}
	if got := welcomeURL("http://127.0.0.1:8080"); got != "http://127.0.0.1:8080/welcome" {
		t.Fatalf("welcomeURL = %q", got)
	}
}
