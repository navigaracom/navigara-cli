package main

import (
	"errors"
	"strings"
)

const (
	wizardConnect = "connect"
	wizardEdit    = "edit"
	wizardCancel  = "cancel"
)

func wizardReview(window string, p *setupInput) (string, error) {
	connect := "Connect and import"
	if window == "skip" {
		connect = "Connect"
	}
	if p.interactive() {
		return p.selectOption("Connect this computer to Navigara", []setupChoice{
			{label: connect, value: wizardConnect},
			{label: "Edit settings", value: wizardEdit},
			{label: "Cancel", value: wizardCancel},
		}, wizardCancel)
	}
	answer, err := p.ask("Connect this computer to Navigara (y/n/edit)", "n")
	if err != nil {
		return "", err
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return wizardConnect, nil
	case "e", "edit", "back":
		return wizardEdit, nil
	case "n", "no":
		return wizardCancel, nil
	default:
		return "", errors.New("enter y, n, or edit; run navigara wizard again")
	}
}
