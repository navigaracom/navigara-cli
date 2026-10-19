package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

type setupInput struct {
	r        *bufio.Reader
	out      io.Writer
	terminal *os.File
	command  string // what the user ran, for login recovery messages
}

func (p *setupInput) ask(label, fallback string) (string, error) {
	if p.interactive() {
		return p.askForm(label, fallback)
	}
	if fallback != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, fallback)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	line, err := p.r.ReadString('\n')
	if err != nil {
		return "", errors.New("input ended; no further changes applied")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = fallback
	}
	return line, nil
}

func (p *setupInput) yes(label string) (bool, error) {
	if p.interactive() {
		return p.yesForm(label)
	}
	answer, err := p.ask(label+" (y/n)", "n")
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, errors.New("enter y or n; run navigara wizard again")
	}
}

func (p *setupInput) token() (string, error) {
	if p.interactive() {
		return p.tokenForm()
	}
	if p.terminal == nil {
		return p.ask("Paste analytics ingestor token", "")
	}
	stty := func(args ...string) ([]byte, error) {
		cmd := exec.Command("stty", args...)
		cmd.Stdin = p.terminal
		return cmd.Output()
	}
	state, err := stty("-g")
	if err != nil {
		return "", errors.New("cannot hide token input on this terminal")
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)
	defer signal.Stop(interrupt)
	defer func() { _, _ = stty(strings.TrimSpace(string(state))); fmt.Fprintln(p.out) }()
	if _, err = stty("-echo"); err != nil {
		return "", errors.New("cannot hide token input on this terminal")
	}
	type reply struct {
		value string
		err   error
	}
	done := make(chan reply, 1)
	go func() { value, err := p.ask("Paste analytics ingestor token (hidden)", ""); done <- reply{value, err} }()
	select {
	case r := <-done:
		return r.value, r.err
	case <-interrupt:
		return "", errors.New("setup interrupted")
	}
}
