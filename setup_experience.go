package main

import (
	"errors"
	"fmt"

	"charm.land/huh/v2"
)

func wizardTools(available []string, p *setupInput) ([]string, error) {
	if len(available) == 0 {
		return nil, errors.New("no tools selected; run Claude Code or Codex once, then rerun navigara wizard")
	}
	var selected []string
	if p.interactive() {
		selected = append(selected, available...)
		choices := make([]huh.Option[string], len(available))
		for i, tool := range available {
			choices[i] = huh.NewOption(setupToolName(tool), tool)
		}
		err := p.form(huh.NewForm(huh.NewGroup(huh.NewMultiSelect[string]().
			Title("Which tools would you like to connect?").
			Description("Import past sessions and keep future activity in sync.").
			Options(choices...).Value(&selected).Validate(func(tools []string) error {
			if len(tools) == 0 {
				return errors.New("select at least one tool")
			}
			return nil
		}))))
		return selected, err
	}
	for _, tool := range available {
		connect, err := p.yes("Connect " + setupToolName(tool))
		if err != nil {
			return nil, err
		}
		if connect {
			selected = append(selected, tool)
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("no tools selected; rerun navigara wizard and choose a tool to connect")
	}
	return selected, nil
}

func wizardHistory(p *setupInput) (string, error) {
	if !p.interactive() {
		return p.ask("How much history should we import? (7d, 24h, all, or skip)", "30d")
	}
	window, err := p.selectOption("How much history should we import?", []setupChoice{
		{"Last 24 hours", "24h"}, {"Last 7 days", "7d"}, {"Last 30 days", "30d"},
		{"All available history", "all"}, {"Only sync new activity", "skip"}, {"Custom duration", "custom"},
	}, "30d")
	if err != nil || window != "custom" {
		return window, err
	}
	return p.ask("How far back? (for example 12h or 14d)", "7d")
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
