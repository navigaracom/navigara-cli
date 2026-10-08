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

func newWizardForm(account accountConfig, available []string, settings *wizardSettings, selected *[]string, history *string) *huh.Form {
	continueSetup := true
	tools := make([]huh.Option[string], 0, len(available))
	for _, source := range available {
		tools = append(tools, huh.NewOption(setupToolName(source), source))
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().Title("Tools to connect").
			Options(tools...).Value(selected).Height(len(available)+2).
			Validate(func(selected []string) error {
				if len(selected) == 0 {
					return errors.New("select at least one tool")
				}
				return nil
			}),
		huh.NewInput().Title("Your email").Value(&settings.email).
			Validate(func(email string) error { return validEmail(strings.TrimSpace(email)) }),
		huh.NewSelect[string]().Title("Import history").Options(
			huh.NewOption("All available history", "all"),
			huh.NewOption("Last 30 days", "30d"),
			huh.NewOption("Last 7 days", "7d"),
			huh.NewOption("Last 24 hours", "24h"),
			huh.NewOption("Only sync new activity", "skip"),
			huh.NewOption("Custom duration", "custom"),
		).Value(history).Height(2),
		huh.NewConfirm().Key("continue").Title("Ready to preview your connection").
			Description("Close running sessions first.\nEnter to continue; Shift+Tab to edit.").
			Affirmative("Continue").Negative("Cancel").Value(&continueSetup),
	).Title("Connect this computer to Navigara").Description(fmt.Sprintf("Account: %q\nOrg: %q\nInstance: %q", account.Email, account.OrganizationName, account.Host)))
	form.WithTheme(huh.ThemeFunc(func(isDark bool) *huh.Styles {
		styles := huh.ThemeCharm(isDark)
		styles.Focused.SelectedPrefix = styles.Focused.SelectedPrefix.SetString("[x] ")
		styles.Focused.UnselectedPrefix = styles.Focused.UnselectedPrefix.SetString("[ ] ")
		styles.Blurred.SelectedPrefix = styles.Blurred.SelectedPrefix.SetString("[x] ")
		styles.Blurred.UnselectedPrefix = styles.Blurred.UnselectedPrefix.SetString("[ ] ")
		return styles
	}))
	form.NextField()
	form.NextField()
	form.NextField()
	return form
}

func wizardPreferences(account accountConfig, available []string, p *setupInput) (wizardSettings, error) {
	if len(available) == 0 {
		return wizardSettings{}, errors.New("no tools selected; run Claude Code or Codex once, then rerun navigara wizard")
	}
	settings := wizardSettings{source: setupSource(available), email: account.Email, history: "all"}
	return editWizardPreferences(account, available, settings, p)
}

func editWizardPreferences(account accountConfig, available []string, settings wizardSettings, p *setupInput) (wizardSettings, error) {
	selected := selectedTools(available, settings.source)
	history, customHistory := settings.history, "7d"
	if !knownHistory(history) {
		customHistory, history = history, "custom"
	}
	if p.interactive() {
		form := newWizardForm(account, available, &settings, &selected, &history)
		if err := p.form(form); err != nil {
			return settings, err
		}
		if !form.GetBool("continue") {
			return settings, errors.New("input cancelled; no further changes applied")
		}
		settings.history = history
	} else {
		selected = selected[:0]
		for _, tool := range available {
			fallback := "n"
			if selectedBySource(tool, settings.source) {
				fallback = "y"
			}
			answer, err := p.ask("Connect "+setupToolName(tool)+" (y/n)", fallback)
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
		var err error
		if settings.email, err = p.ask("Your email", settings.email); err != nil {
			return settings, err
		}
		if settings.history, err = p.ask("How much history should we import? (7d, 24h, all, or skip)", settings.history); err != nil {
			return settings, err
		}
	}
	if len(selected) == 0 {
		return settings, errors.New("no tools selected; rerun navigara wizard and choose a tool to connect")
	}
	settings.source = setupSource(selected)
	settings.email = strings.TrimSpace(settings.email)
	if err := validEmail(settings.email); err != nil {
		return settings, err
	}
	if settings.history == "custom" {
		var err error
		settings.history, err = p.ask("How far back? (for example 12h or 14d)", customHistory)
		if err != nil {
			return settings, err
		}
	}
	return settings, nil
}

func knownHistory(history string) bool {
	for _, candidate := range []string{"all", "30d", "7d", "24h", "skip", "custom"} {
		if history == candidate {
			return true
		}
	}
	return false
}

func selectedTools(available []string, source string) []string {
	selected := make([]string, 0, len(available))
	for _, tool := range available {
		if selectedBySource(tool, source) {
			selected = append(selected, tool)
		}
	}
	return selected
}

func selectedBySource(tool, source string) bool {
	return source == "all" || source == tool
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
