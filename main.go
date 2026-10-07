package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

var version = "dev"

type options struct {
	source, output        string
	claudeHome, codexHome string
	since, until          time.Time
	sessions              []string
	email                 string
	allowAnonymous        bool
	send                  bool
	host, url, token      string
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	invalid := func(message string) int {
		fmt.Fprintln(stderr, "navigara:", message)
		return 2
	}
	if len(args) == 0 {
		fmt.Fprintln(stdout, "Usage: navigara wizard [options]\n       navigara login [--host ORIGIN]\n       navigara logout [--host ORIGIN]\n       navigara sessions upload [options]\n       navigara --version")
		return 0
	}
	switch args[0] {
	case "login":
		return runLogin(args[1:], os.Stdin, stdout, stderr)
	case "logout":
		return runLogout(args[1:], stdout, stderr)
	case "wizard", "setup":
		return runSetup(args[1:], os.Stdin, stdout, stderr)
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
		fmt.Fprintln(stdout, "Usage: navigara sessions upload [options]\n       navigara sessions config patch [options]")
		return 0
	}
	if args[0] == "config" {
		if len(args) < 2 || args[1] != "patch" {
			return invalid("unknown command: sessions config")
		}
		return runConfigPatch(args[2:], stdout, stderr)
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
	fs.StringVar(&opts.source, "source", "all", "session source: all, claude, codex")
	fs.StringVar(&opts.output, "output", "./navigara-backfill", "new directory for OTLP request bodies")
	fs.StringVar(&opts.claudeHome, "claude-home", "", "Claude home containing projects/ (default ~/.claude)")
	fs.StringVar(&opts.codexHome, "codex-home", "", "Codex home containing sessions/ (default ~/.codex)")
	fs.Func("session", "only export this session id (repeatable)", func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("--session must not be empty")
		}
		opts.sessions = append(opts.sessions, value)
		return nil
	})
	fs.Func("since", "only export records from this RFC 3339 time", func(value string) error {
		t, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return errors.New("--since must be an RFC 3339 time")
		}
		opts.since = t.UTC()
		return nil
	})
	fs.Func("until", "only export records up to this RFC 3339 time", func(value string) error {
		t, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return errors.New("--until must be an RFC 3339 time")
		}
		opts.until = t.UTC()
		return nil
	})
	fs.StringVar(&opts.email, "email", "", "email to attribute the sessions to (default: the signed-in account)")
	fs.BoolVar(&opts.allowAnonymous, "allow-anonymous", false, "export without a user email")
	fs.BoolVar(&opts.send, "send", false, "post the request bodies using saved login or --url and --token")
	fs.StringVar(&opts.host, "host", "", "Navigara origin for --send with saved login (default saved host)")
	fs.StringVar(&opts.url, "url", "", "Navigara base URL, for example https://app.navigara.com")
	fs.StringVar(&opts.token, "token", os.Getenv("NAVIGARA_TOKEN"), "API key with the analytics ingestor role (default $NAVIGARA_TOKEN)")
	var last time.Duration
	fs.Func("last", "only export points from the last duration (for example 24h or 7d)", func(value string) error {
		parsed, err := parseLast(value)
		if err != nil {
			return err
		}
		last = parsed
		return nil
	})
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
	if last > 0 {
		if opts.until.IsZero() {
			opts.until = time.Now().UTC()
		}
		opts.since = opts.until.Add(-last)
	}
	return exportSessions(opts, stdout, stderr)
}
