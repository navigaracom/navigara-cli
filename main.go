package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

var version = "dev"

type options struct {
	dryRun                bool
	source, output        string
	claudeHome, codexHome string
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	invalid := func(message string) int {
		fmt.Fprintln(stderr, "navigara:", message)
		return 2
	}
	if len(args) == 0 {
		fmt.Fprintln(stdout, "Usage: navigara sessions upload [options]\n       navigara --version")
		return 0
	}
	switch args[0] {
	case "--help", "-h", "--version":
		if len(args) != 1 {
			return invalid("unexpected argument: " + args[1])
		}
		if args[0] == "--version" {
			fmt.Fprintln(stdout, "navigara", version)
			return 0
		}
		return run(nil, stdout, stderr)
	case "sessions":
		args = args[1:]
	default:
		return invalid("unknown command: " + args[0])
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		if len(args) > 1 {
			return invalid("unexpected argument: " + args[1])
		}
		fmt.Fprintln(stdout, "Usage: navigara sessions upload [options]")
		return 0
	}
	if args[0] != "upload" {
		return invalid("unknown command: sessions " + args[0])
	}
	var opts options
	var help, shortHelp bool
	fs := flag.NewFlagSet("sessions upload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	fs.BoolVar(&help, "help", false, "show upload help")
	fs.BoolVar(&shortHelp, "h", false, "show upload help")
	fs.BoolVar(&opts.dryRun, "dry-run", true, "export local OTLP files without network requests")
	fs.StringVar(&opts.source, "source", "all", "session source: all, claude, codex")
	fs.StringVar(&opts.output, "output", "./navigara-backfill", "new directory for OTLP request bodies")
	fs.StringVar(&opts.claudeHome, "claude-home", "", "Claude home containing projects/ (default ~/.claude)")
	fs.StringVar(&opts.codexHome, "codex-home", "", "Codex home containing sessions/ (default ~/.codex)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return invalid("unexpected argument: " + fs.Arg(0))
	}
	if opts.source != "all" && opts.source != "claude" && opts.source != "codex" {
		return invalid("--source must be all, claude, or codex")
	}
	if opts.output == "" {
		return invalid("--output must not be empty")
	}
	var emptyHome string
	fs.Visit(func(f *flag.Flag) {
		if (f.Name == "claude-home" || f.Name == "codex-home") && f.Value.String() == "" {
			emptyHome = f.Name
		}
	})
	if emptyHome != "" {
		return invalid("--" + emptyHome + " must not be empty")
	}
	if help || shortHelp {
		fmt.Fprintln(stdout, "Usage: navigara sessions upload [options]")
		fs.SetOutput(stdout)
		fs.PrintDefaults()
		return 0
	}
	if !opts.dryRun {
		return invalid("--dry-run=false is unsupported; network upload is not implemented")
	}
	return upload(opts, stdout, stderr)
}
