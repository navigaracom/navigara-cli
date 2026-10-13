package main

import (
	"fmt"
)

func setupContinuationURL(host string) (string, error) {
	host, err := canonicalHost(host)
	if err != nil {
		return "", err
	}
	return host + "/welcome/ai-adoption", nil
}

// completeSetup reports a successful upload before offering the web continuation.
// A browser failure is advisory: the URL remains in the terminal output.
func completeSetup(p *setupInput, sent int, host string, open func(string) error) {
	completeSetupForMode(p, sent, host, p.interactive(), open)
}

func completeSetupForMode(p *setupInput, sent int, host string, interactive bool, open func(string) error) {
	p.completedForMode(sent, interactive)
	if host == "" {
		return
	}
	url, err := setupContinuationURL(host)
	if err != nil {
		return
	}
	if !interactive {
		fmt.Fprintf(p.out, "Continue in Navigara: %s\n", url)
		return
	}
	if open != nil && open(url) == nil {
		p.status("Continue in Navigara", "Your browser is opening the AI adoption guide.\n")
		return
	}
	p.status("Continue in Navigara", fmt.Sprintf("Open this URL to finish setup:\n%s\n", url))
}
