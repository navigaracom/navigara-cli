package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/term"
)

// newSetupInput keeps interactive forms opt-in: both streams must be terminals.
func newSetupInput(input io.Reader, output io.Writer) *setupInput {
	p := &setupInput{r: bufio.NewReader(input), out: output}
	in, inOK := input.(*os.File)
	if inOK && term.IsTerminal(in.Fd()) {
		p.terminal = in
	}
	return p
}

func (p *setupInput) interactive() bool {
	out, ok := p.out.(*os.File)
	return p.terminal != nil && ok && term.IsTerminal(out.Fd()) && os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == ""
}

func (p *setupInput) form(form *huh.Form) error {
	// Pass the file itself so Bubble Tea can enter and restore terminal raw mode.
	width := 72
	if columns, _, err := term.GetSize(p.terminal.Fd()); err == nil && columns > 0 {
		width = min(width, columns)
	}
	form.WithInput(p.terminal).WithOutput(p.out).WithWidth(width)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return errors.New("input cancelled; no further changes applied")
		}
		return err
	}
	return nil
}

func (p *setupInput) askForm(label, fallback string) (string, error) {
	value := fallback
	err := p.form(huh.NewForm(huh.NewGroup(
		huh.NewInput().Title(label).Value(&value),
	)))
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	return value, err
}

func (p *setupInput) yesForm(label string) (bool, error) {
	value := false
	err := p.form(huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(label).Value(&value),
	)))
	return value, err
}

func (p *setupInput) tokenForm() (string, error) {
	var value string
	err := p.form(huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Paste analytics ingestor token").EchoMode(huh.EchoModePassword).Value(&value),
	)))
	return strings.TrimSpace(value), err
}

type setupChoice struct{ label, value string }

func (p *setupInput) selectOption(title string, choices []setupChoice, fallback string) (string, error) {
	if len(choices) == 0 {
		return "", errors.New("no choices available")
	}
	values := make([]string, len(choices))
	for i, choice := range choices {
		values[i] = choice.value
	}
	if fallback == "" {
		fallback = values[0]
	}
	if !slices.Contains(values, fallback) {
		return "", fmt.Errorf("fallback %q is not a listed option", fallback)
	}
	if !p.interactive() {
		fmt.Fprintln(p.out, title)
		for i, choice := range choices {
			fmt.Fprintf(p.out, "  %d) %s\n", i+1, choice.label)
		}
		answer, err := p.ask("Choose", strconv.Itoa(slices.Index(values, fallback)+1))
		if err != nil {
			return "", err
		}
		n, err := strconv.Atoi(answer)
		if err != nil || n < 1 || n > len(values) {
			return "", errors.New("choose a listed option by number; run navigara wizard again")
		}
		return values[n-1], nil
	}

	selected := fallback
	options := make([]huh.Option[string], len(choices))
	for i, choice := range choices {
		options[i] = huh.NewOption(choice.label, choice.value)
	}
	err := p.form(huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title(title).Options(options...).Value(&selected),
	)))
	return selected, err
}
