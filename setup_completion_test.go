package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCompleteSetupInteractiveOpensValidatedContinuation(t *testing.T) {
	var out bytes.Buffer
	var opened []string
	p := &setupInput{out: &out}
	completeSetupForMode(p, 3, "https://app.example", true, func(raw string) error {
		opened = append(opened, raw)
		return nil
	})

	if len(opened) != 1 || opened[0] != "https://app.example/welcome/ai-adoption" {
		t.Fatalf("opened URLs = %v", opened)
	}
	if !strings.Contains(out.String(), "History: 3 upload batches received") {
		t.Fatalf("success output missing: %q", out.String())
	}
}

func TestCompleteSetupBrowserFailureKeepsContinuationLink(t *testing.T) {
	var out bytes.Buffer
	p := &setupInput{out: &out}
	completeSetupForMode(p, 2, "https://app.example", true, func(string) error {
		return errors.New("browser unavailable")
	})

	if got := out.String(); !strings.Contains(got, "History: 2 upload batches received") || !strings.Contains(got, "https://app.example/welcome/ai-adoption") {
		t.Fatalf("failure output lost success or URL: %q", got)
	}
}

func TestCompleteSetupHeadlessPrintsLinkWithoutOpening(t *testing.T) {
	var out bytes.Buffer
	p := &setupInput{out: &out}
	opened := false
	completeSetupForMode(p, 1, "http://127.0.0.1:8080", false, func(string) error {
		opened = true
		return nil
	})

	if opened {
		t.Fatal("headless completion opened a browser")
	}
	if !strings.Contains(out.String(), "http://127.0.0.1:8080/welcome/ai-adoption") {
		t.Fatalf("continuation link missing: %q", out.String())
	}
}

func TestCompleteSetupLegacyResumeDoesNotInventHost(t *testing.T) {
	var out bytes.Buffer
	p := &setupInput{out: &out}
	opened := false
	completeSetupForMode(p, 1, "", false, func(string) error {
		opened = true
		return nil
	})

	if opened || strings.Contains(out.String(), "welcome/ai-adoption") {
		t.Fatalf("legacy completion invented a continuation route: %q", out.String())
	}
}
