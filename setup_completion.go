package main

import (
	"fmt"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// welcomeURL uses the authenticated frontend host, never a guessed API host.
func welcomeURL(host string) string {
	host, err := canonicalHost(host)
	if err != nil {
		return ""
	}
	return host + "/welcome"
}

// confirmOpen is nil without a terminal: piped runs never open a browser.
func (p *setupInput) confirmOpen() func() (bool, error) {
	if !p.interactive() {
		return nil
	}
	return func() (bool, error) {
		yes := true
		err := p.form(huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title("Open it now? [Y/n]").Value(&yes).
				WithButtonAlignment(lipgloss.Left),
		)))
		return yes, err
	}
}

// continueSetup opens the welcome page only after the user agrees.
func continueSetup(p *setupInput, url string, confirm func() (bool, error), open func(string) error) {
	if url == "" || confirm == nil || open == nil {
		return
	}
	if yes, err := confirm(); err != nil || !yes {
		return
	}
	if err := open(url); err != nil {
		fmt.Fprintf(p.out, "Your browser couldn't be opened. Open %s yourself.\n", url)
	}
}
