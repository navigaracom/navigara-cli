package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

// exitInterrupted is the conventional status of a command stopped by an interrupt.
const exitInterrupted = 130

type options struct {
	source, output        string
	claudeHome, codexHome string
	since, until          time.Time
	sourceSince           map[string]time.Time
	skippedTools          map[string]bool
	sessions              []string
	email                 string
	allowAnonymous        bool
	humanSummary          bool
	shell                 string
	send                  bool
	resume                string
	host, url, token      string
	tokenExplicit         bool
	organizationID        string
}

// sinceFor lets the wizard resume each tool from its own last import.
func (o options) sinceFor(tool string) time.Time {
	if since, ok := o.sourceSince[tool]; ok {
		return since
	}
	return o.since
}

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
		return runLogin(ctx, args[1:], os.Stdin, stdout, stderr)
	case "logout":
		return runLogout(ctx, args[1:], stdout, stderr)
	case "wizard", "setup":
		return runSetup(ctx, args[1:], os.Stdin, stdout, stderr)
	case "--help", "-h", "--version":
		if len(args) != 1 {
			return invalid("unexpected argument: " + args[1])
		}
		if args[0] == "--version" {
			fmt.Fprintln(stdout, "navigara", version)
			return 0
		}
		return run(ctx, nil, stdout, stderr)
	case "sessions":
		args = args[1:]
	default:
		return invalid("unknown command: " + args[0])
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		if len(args) > 1 {
			return invalid("unexpected argument: " + args[1])
		}
		fmt.Fprintln(stdout, "Usage: navigara sessions upload [options]\n       navigara sessions config patch [options]\n       navigara sessions config remove [options]\n       navigara sessions status")
		return 0
	}
	if args[0] == "status" {
		return runStatus(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "config" {
		switch {
		case len(args) >= 2 && args[1] == "patch":
			return runConfigPatch(ctx, args[2:], stdout, stderr)
		case len(args) >= 2 && args[1] == "remove":
			return runConfigRemove(args[2:], stdout, stderr)
		}
		return invalid("unknown command: sessions config")
	}
	if args[0] != "upload" {
		return invalid("unknown command: sessions " + args[0])
	}
	var opts options
	var help, shortHelp, dryRun, send bool
	fs := flag.NewFlagSet("sessions upload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	fs.BoolVar(&help, "help", false, "show upload help")
	fs.BoolVar(&shortHelp, "h", false, "show upload help")
	fs.StringVar(&opts.source, "source", "all", "session source: all, claude, codex")
	fs.StringVar(&opts.output, "output", "", "new directory for gzip-compressed OTLP request bodies (default: a private temporary directory)")
	fs.StringVar(&opts.claudeHome, "claude-home", "", "Claude home containing projects/ (default $CLAUDE_CONFIG_DIR or ~/.claude)")
	fs.StringVar(&opts.codexHome, "codex-home", "", "Codex home containing sessions/ (default $CODEX_HOME or ~/.codex)")
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
	fs.BoolVar(&dryRun, "dry-run", false, "write the request bodies to --output, but do not send them")
	fs.BoolVar(&send, "send", false, "no effect; upload sends by default (kept for old scripts)")
	fs.StringVar(&opts.resume, "resume", "", "send the batches of an earlier upload in DIR that Navigara did not receive")
	fs.StringVar(&opts.host, "host", "", "Navigara origin with saved login (default saved host)")
	fs.StringVar(&opts.url, "url", "", "Navigara base URL, for example https://app.navigara.com")
	fs.StringVar(&opts.token, "token", "", "Analytics Ingestor key (default $NAVIGARA_TOKEN)")
	var last time.Duration
	fs.Func("last", "only export records from the last duration (for example 24h or 7d)", func(value string) error {
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
	var emptyPath string
	fs.Visit(func(f *flag.Flag) {
		if (f.Name == "output" || f.Name == "claude-home" || f.Name == "codex-home" || f.Name == "resume") && f.Value.String() == "" {
			emptyPath = f.Name
		}
	})
	if emptyPath != "" {
		return invalid("--" + emptyPath + " must not be empty")
	}
	if help || shortHelp {
		fmt.Fprintln(stdout, "Usage: navigara sessions upload [options]\n       navigara sessions upload --resume DIR")
		fs.SetOutput(stdout)
		fs.PrintDefaults()
		return 0
	}
	if dryRun && send {
		return invalid("use either --dry-run or --send")
	}
	var resumeConflict string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "token" {
			opts.tokenExplicit = true
		}
		if opts.resume != "" && f.Name != "resume" && f.Name != "host" && f.Name != "url" && f.Name != "token" && f.Name != "send" {
			resumeConflict = f.Name
		}
	})
	if resumeConflict != "" {
		return invalid("--resume cannot be used with --" + resumeConflict)
	}
	opts.send = !dryRun
	if !opts.tokenExplicit {
		opts.token = os.Getenv("NAVIGARA_TOKEN")
	}
	if last > 0 {
		if opts.until.IsZero() {
			opts.until = time.Now().UTC()
		}
		opts.since = opts.until.Add(-last)
	}
	return exportSessions(ctx, opts, stdout, stderr)
}

// interruptible cancels ctx on the first interrupt, so a long export or upload stops between
// requests and leaves its frozen bodies for a retry. A second interrupt exits as usual.
func interruptible(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}
