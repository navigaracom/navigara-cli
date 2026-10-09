package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
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
			Options(tools...).Value(selected).Height(len(available)+1).
			Validate(func(selected []string) error {
				if len(selected) == 0 {
					return errors.New("select at least one tool")
				}
				return nil
			}),
		huh.NewInput().Title("Your email").Prompt("  ").Value(&settings.email).
			Validate(func(email string) error { return validEmail(strings.TrimSpace(email)) }),
		huh.NewSelect[string]().Title("Import past sessions").Options(
			huh.NewOption("All available history", "all"),
			huh.NewOption("Last 30 days", "30d"),
			huh.NewOption("Last 7 days", "7d"),
			huh.NewOption("Last 24 hours", "24h"),
			huh.NewOption("Do not import history", "skip"),
			huh.NewOption("Custom duration", "custom"),
		).Value(history).Height(7),
		huh.NewConfirm().Key("continue").Title("Close running sessions first.").
			Description("Enter to connect; Shift+Tab to edit.").
			Affirmative("Connect").Negative("Cancel").Value(&continueSetup).
			WithButtonAlignment(lipgloss.Left),
	).Title("  Connect this computer to Navigara").Description(fmt.Sprintf("  Account       %s\n  Organization  %s\n  Instance      %s", wizardHeaderValue(account.Email), wizardHeaderValue(account.OrganizationName), wizardHeaderValue(account.Host))))
	form.WithTheme(huh.ThemeFunc(func(isDark bool) *huh.Styles {
		styles := huh.ThemeCharm(isDark)
		styles.FieldSeparator = lipgloss.NewStyle().SetString("\n")
		styles.Focused.Base = styles.Focused.Base.BorderForeground(styles.Focused.Title.GetForeground())
		styles.Focused.SelectedPrefix = styles.Focused.SelectedPrefix.SetString("[x] ")
		styles.Focused.UnselectedPrefix = styles.Focused.UnselectedPrefix.SetString("[ ] ")
		styles.Blurred.Title = styles.Blurred.Option.Bold(true)
		styles.Blurred.SelectedOption = styles.Blurred.Option
		styles.Blurred.SelectedPrefix = styles.Blurred.Option.SetString("[x] ")
		styles.Blurred.UnselectedPrefix = styles.Blurred.UnselectedPrefix.SetString("[ ] ")
		styles.Blurred.MultiSelectSelector = lipgloss.NewStyle().SetString("  ")
		styles.Blurred.SelectSelector = styles.Blurred.Option.SetString("● ")
		styles.Blurred.TextInput.Prompt = lipgloss.NewStyle()
		styles.Blurred.TextInput.Cursor = lipgloss.NewStyle()
		styles.Blurred.TextInput.Text = styles.Blurred.Option
		styles.Blurred.FocusedButton = styles.Blurred.BlurredButton.Bold(true)
		return styles
	}))
	form.NextField()
	form.NextField()
	form.NextField()
	return form
}

func wizardHeaderValue(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

func (p *setupInput) confirmCodexReplacement() (bool, error) {
	if !p.interactive() {
		return p.yes("Replace Codex's existing telemetry with Navigara and save a private backup")
	}
	value := false
	err := p.form(huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title("Replace Codex telemetry?").
			Description("Stream new activity to Navigara.\nSave a private settings backup.").
			Affirmative("Replace").Negative("Keep existing").Value(&value).
			WithButtonAlignment(lipgloss.Left),
	)))
	return value, err
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
func wizardPlans(opts *options, home string, selected []string, p *setupInput) ([]*setupFile, []string, error) {
	var files []*setupFile
	var connected []string
	for _, tool := range selected {
		toolOpts := *opts
		toolOpts.source = tool
		planned, err := setupPlans(toolOpts, home)
		if tool == "codex" && errors.Is(err, errCodexTelemetry) {
			replace, inputErr := p.confirmCodexReplacement()
			if inputErr != nil {
				return nil, nil, inputErr
			}
			if replace {
				toolOpts.replaceCodexTelemetry = true
				planned, err = setupPlans(toolOpts, home)
			}
		}
		if err != nil {
			var skip bool
			var inputErr error
			if p.interactive() {
				description := "Its settings have not changed. Skipping excludes both its history and new activity from this setup.\n\nDetails\n" + wizardHeaderValue(err.Error())
				inputErr = p.form(huh.NewForm(huh.NewGroup(
					huh.NewConfirm().Title(setupToolName(tool) + " couldn't be connected").
						Description(lipgloss.Wrap(description, wizardStatusWidth(p)-wizardStatusInset, " /\\?&=._")).
						Affirmative("Skip").Negative("Cancel").Value(&skip).
						WithButtonAlignment(lipgloss.Left),
				)))
			} else {
				fmt.Fprintf(p.out, "\n%s couldn't be connected safely. Its settings have not changed.\nDetails: %v\n", setupToolName(tool), err)
				skip, inputErr = p.yes("Skip " + setupToolName(tool) + " for this setup (history and new activity)")
			}
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
		if tool == "codex" {
			opts.replaceCodexTelemetry = toolOpts.replaceCodexTelemetry
		}
	}
	return files, connected, nil
}
