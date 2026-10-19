package main

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

type wizardSettings struct {
	source, email, history, custom string
	// imports holds the end of the last import for each tool imported before.
	imports map[string]time.Time
}

func newWizardForm(account accountConfig, available []string, settings *wizardSettings, selected *[]string, history *string) *huh.Form {
	continueSetup := true
	tools := make([]huh.Option[string], 0, len(available))
	connectDescription := "Enter to connect; Shift+Tab to edit."
	for _, source := range available {
		tools = append(tools, huh.NewOption(setupToolName(source), source))
		if source == "claude" {
			connectDescription = "Claude sends prompt text and tool arguments.\n" + connectDescription
		}
	}
	historyOptions := []huh.Option[string]{huh.NewOption("All available history", "all")}
	if since := earliestImport(settings.imports); !since.IsZero() {
		historyOptions = append([]huh.Option[string]{huh.NewOption("Since last import ("+since.Local().Format(time.DateOnly)+")", "since")}, historyOptions...)
	}
	historyOptions = append(historyOptions,
		huh.NewOption("Last 30 days", "30d"),
		huh.NewOption("Last 7 days", "7d"),
		huh.NewOption("Last 24 hours", "24h"),
		huh.NewOption("Do not import history", "skip"),
		huh.NewOption("Custom duration", "custom"),
	)
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
		huh.NewSelect[string]().Title("Import past sessions").Options(historyOptions...).
			Value(history).Height(len(historyOptions)+1),
		huh.NewConfirm().Key("continue").Title("Close running sessions first.").
			Description(connectDescription).
			Affirmative("Connect").Negative("Cancel").Value(&continueSetup).
			WithButtonAlignment(lipgloss.Left),
	).Title("  Connect this computer to Navigara").Description(fmt.Sprintf("  Account       %s\n  Organization  %s\n  Instance      %s", wizardHeaderValue(account.Email), wizardHeaderValue(account.OrganizationName), wizardHeaderValue(account.Host))),
		huh.NewGroup(
			huh.NewInput().Title("How far back?").Description("For example 14d or 36h.").Prompt("  ").
				Placeholder("14d").Value(&settings.custom).Validate(validDuration),
		).WithHideFunc(func() bool { return *history != "custom" || !continueSetup }),
	)
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

func (p *setupInput) confirmReplacement(tool string, files []*setupFile) (bool, error) {
	var changes []string
	for _, f := range files {
		if f.conflict != nil {
			for _, note := range f.notes {
				changes = append(changes, wizardHeaderValue(note))
			}
		}
	}
	if !p.interactive() {
		fmt.Fprintf(p.out, "\n%s already sends telemetry. These settings change:\n  %s\n", setupToolName(tool), strings.Join(changes, "\n  "))
		return p.yes("Replace " + setupToolName(tool) + "'s existing telemetry with Navigara and save a private backup")
	}
	value := false
	err := p.form(huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title("Replace " + setupToolName(tool) + " telemetry?").
			Description("Stream new activity to Navigara.\nSave a private settings backup.\n\n" + strings.Join(changes, "\n")).
			Affirmative("Replace").Negative("Keep existing").Value(&value).
			WithButtonAlignment(lipgloss.Left),
	)))
	return value, err
}

func wizardPreferences(account accountConfig, available []string, p *setupInput) (wizardSettings, error) {
	settings := wizardSettings{source: setupSource(available), email: account.Email, history: "all", imports: map[string]time.Time{}}
	connected := false
	for key, at := range account.Imports {
		org, tool, _ := strings.Cut(key, "/")
		if org != account.OrganizationID {
			continue
		}
		connected = true
		if slices.Contains(available, tool) {
			settings.imports[tool] = at
			settings.history = "since"
		}
	}
	if connected {
		fmt.Fprintf(p.out, "Already connected to %s as %s.\n", account.Host, account.Email)
	}
	return editWizardPreferences(account, available, settings, p)
}

func editWizardPreferences(account accountConfig, available []string, settings wizardSettings, p *setupInput) (wizardSettings, error) {
	selected := selectedTools(available, settings.source)
	history := settings.history
	if !knownHistory(history) {
		settings.custom, history = history, "custom"
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
			question := "Connect " + setupToolName(tool) + " (y/n)"
			if tool == "claude" {
				question = "Connect Claude Code (sends prompt text and tool arguments) (y/n)"
			}
			answer, err := p.ask(question, fallback)
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
		question := "How much history should we import? (7d, 24h, all, or skip)"
		if len(settings.imports) > 0 {
			question = "How much history should we import? (since last import, 7d, 24h, all, or skip)"
		}
		if settings.history, err = p.askHistory(question, settings.history, func(value string) error {
			if knownHistory(value) && (value != "since" || len(settings.imports) > 0) {
				return nil
			}
			return validDuration(value)
		}); err != nil {
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
		if p.interactive() {
			settings.history = strings.TrimSpace(settings.custom)
		} else {
			var err error
			if settings.history, err = p.askHistory("How far back? (for example 12h or 14d)", firstNonEmpty(settings.custom, "7d"), validDuration); err != nil {
				return settings, err
			}
		}
	}
	return settings, nil
}

func validDuration(value string) error {
	if _, err := parseLast(strings.TrimSpace(value)); err != nil {
		return errors.New("Enter a duration like 14d or 36h.")
	}
	return nil
}

// askHistory asks again until the answer is valid, so a typo never ends the wizard.
func (p *setupInput) askHistory(question, fallback string, valid func(string) error) (string, error) {
	for {
		answer, err := p.ask(question, fallback)
		if err != nil {
			return "", err
		}
		if err = valid(answer); err == nil {
			return answer, nil
		}
		fmt.Fprintln(p.out, err)
	}
}

func earliestImport(imports map[string]time.Time) time.Time {
	var earliest time.Time
	for _, at := range imports {
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	return earliest
}

func knownHistory(history string) bool {
	for _, candidate := range []string{"all", "since", "30d", "7d", "24h", "skip", "custom"} {
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
		if conflict := planConflict(planned); err == nil && conflict != nil {
			replace, inputErr := p.confirmReplacement(tool, planned)
			if inputErr != nil {
				return nil, nil, inputErr
			}
			if !replace {
				err = conflict
			}
		}
		if err != nil {
			skip := true
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
				var answer string
				if answer, inputErr = p.ask("Skip "+setupToolName(tool)+" for this setup (history and new activity) (y/n)", "y"); inputErr == nil {
					switch strings.ToLower(answer) {
					case "y", "yes":
					case "n", "no":
						skip = false
					default:
						inputErr = errors.New("enter y or n; run navigara wizard again")
					}
				}
			}
			if inputErr != nil {
				return nil, nil, inputErr
			}
			if !skip {
				return nil, nil, nil
			}
			continue
		}
		for _, f := range planned {
			if f.warning != "" {
				p.status("Codex identity not set", f.warning+"\n")
			}
		}
		files = append(files, planned...)
		connected = append(connected, tool)
	}
	return files, connected, nil
}
