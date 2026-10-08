package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestSelectOptionPlainUsesNumberedChoice(t *testing.T) {
	var out bytes.Buffer
	p := &setupInput{r: bufio.NewReader(strings.NewReader("2\n")), out: &out}

	got, err := p.selectOption("Source", []setupChoice{{"Claude", "claude"}, {"Codex", "codex"}}, "claude")
	if err != nil {
		t.Fatalf("selectOption() error = %v", err)
	}
	if got != "codex" {
		t.Fatalf("selectOption() = %q, want Codex", got)
	}
	if !strings.Contains(out.String(), "1) Claude") || !strings.Contains(out.String(), "2) Codex") {
		t.Fatalf("numbered choices missing from output: %q", out.String())
	}
}

func TestSelectOptionPlainRejectsInvalidChoice(t *testing.T) {
	p := &setupInput{r: bufio.NewReader(strings.NewReader("3\n")), out: &bytes.Buffer{}}

	_, err := p.selectOption("Source", []setupChoice{{"Claude", "claude"}, {"Codex", "codex"}}, "claude")
	if err == nil || !strings.Contains(err.Error(), "choose a listed option") {
		t.Fatalf("selectOption() error = %v, want listed-option validation", err)
	}
}

func TestNewSetupInputDisablesRichModeForRedirectedStreams(t *testing.T) {
	p := newSetupInput(strings.NewReader(""), &bytes.Buffer{})
	if p.interactive() {
		t.Fatal("redirected setup input reported interactive")
	}
}
