package main

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/huh/v2"
)

type wizardSettings struct {
	source, email, history string
}

func newWizardForm(available []string, settings *wizardSettings) *huh.Form {
	tools := make([]huh.Option[string], 0, len(available)+1)
	if len(available) == 2 {
		tools = append(tools, huh.NewOption("Claude Code and Codex", "all"))
	}
	for _, source := range available {
		tools = append(tools, huh.NewOption(setupToolName(source), source))
	}
	return huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Tools to connect").
			Options(tools...).Value(&settings.source),
		huh.NewInput().Title("Your email").Value(&settings.email).
			Validate(func(email string) error { return validEmail(strings.TrimSpace(email)) }),
		huh.NewSelect[string]().Title("Import history").Options(
			huh.NewOption("All available history", "all"),
			huh.NewOption("Last 30 days", "30d"),
			huh.NewOption("Last 7 days", "7d"),
			huh.NewOption("Last 24 hours", "24h"),
			huh.NewOption("Only sync new activity", "skip"),
			huh.NewOption("Custom duration", "custom"),
		).Value(&settings.history),
	).Description("Defaults are ready. Press Enter to keep each value; use Tab or arrows to edit."))
}

func wizardPreferences(available []string, email string, p *setupInput) (wizardSettings, error) {
	if len(available) == 0 {
		return wizardSettings{}, errors.New("no tools selected; run Claude Code or Codex once, then rerun navigara wizard")
	}
	settings := wizardSettings{source: setupSource(available), email: email, history: "all"}
	if p.interactive() {
		if err := p.form(newWizardForm(available, &settings)); err != nil {
			return settings, err
		}
	} else {
		var selected []string
		for _, tool := range available {
			answer, err := p.ask("Connect "+setupToolName(tool)+" (y/n)", "y")
			if err != nil {
				return settings, err
			}
			switch strings.ToLower(answer) {
			case "y", "yes":
				selected = append(selected, tool)
			case "n", "no":
			default:
				return settings, errors.New("enter y or n; run navigara wizard again")
			}
		}
		if len(selected) == 0 {
			return settings, errors.New("no tools selected; rerun navigara wizard and choose a tool to connect")
		}
		settings.source = setupSource(selected)
		var err error
		if settings.email, err = p.ask("Your email", email); err != nil {
			return settings, err
		}
		if settings.history, err = p.ask("How much history should we import? (7d, 24h, all, or skip)", "all"); err != nil {
			return settings, err
		}
	}
	settings.email = strings.TrimSpace(settings.email)
	if err := validEmail(settings.email); err != nil {
		return settings, err
	}
	if settings.history == "custom" {
		var err error
		settings.history, err = p.ask("How far back? (for example 12h or 14d)", "7d")
		if err != nil {
			return settings, err
		}
	}
	return settings, nil
}

func (p *setupInput) wizardContinue(label, action string) (bool, error) {
	if !p.interactive() {
		return p.yes(label)
	}
	accepted := true
	err := p.form(huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(label).Affirmative(action).Negative("Cancel").Value(&accepted),
	)))
	return accepted, err
}

func setupToolName(source string) string {
	if source == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

func setupSource(selected []string) string {
	if len(selected) == 2 {
		return "all"
	}
	return selected[0]
}

// A tool that cannot be updated safely must not prevent connecting another tool.
// Skipping excludes both its history and its streaming settings from this run.
func wizardPlans(opts options, home string, selected []string, p *setupInput) ([]*setupFile, []string, error) {
	var files []*setupFile
	var connected []string
	for _, tool := range selected {
		opts.source = tool
		planned, err := setupPlans(opts, home)
		if err != nil {
			fmt.Fprintf(p.out, "\n%s couldn't be connected safely. Its settings have not changed.\nDetails: %v\n", setupToolName(tool), err)
			skip, inputErr := p.yes("Skip " + setupToolName(tool) + " for this setup (history and new activity)")
			if inputErr != nil {
				return nil, nil, inputErr
			}
			if !skip {
				return nil, nil, nil
			}
			continue
		}
		files = append(files, planned...)
		connected = append(connected, tool)
	}
	return files, connected, nil
}
