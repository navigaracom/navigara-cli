package main

// continueSetup uses the authenticated frontend host, never a guessed API host.
// Keep the URL visible even when the browser launcher reports success.
func continueSetup(p *setupInput, host string, interactive bool, open func(string) error) {
	if host == "" {
		return
	}
	host, err := canonicalHost(host)
	if err != nil {
		return
	}
	url := host + "/welcome/ai-adoption"
	body := "Continue onboarding in Navigara:\n" + url + "\n"
	if interactive && open != nil {
		if err := open(url); err != nil {
			body = "Your browser couldn't be opened. Continue here:\n" + url + "\n"
		}
	}
	p.status("Continue in Navigara", body)
}
