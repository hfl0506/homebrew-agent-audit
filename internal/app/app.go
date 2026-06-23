package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sze/agent-audit/internal/audit"
)

var (
	ErrUsage = errors.New("usage error")
	version  = "0.1.0-dev"
)

type ExitError struct {
	Code int
}

func (e ExitError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

type command struct {
	name string
	run  func(context.Context, []string, io.Writer, io.Writer) error
}

func Run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	commands := []command{
		{name: "run", run: runCommand},
		{name: "sessions", run: sessionsCommand},
		{name: "show", run: showCommand},
		{name: "export", run: exportCommand},
		{name: "doctor", run: doctorCommand},
	}

	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "-h", "--help", "help":
		printUsage(stdout)
		return nil
	case "-v", "--version", "version":
		fmt.Fprintln(stdout, version)
		return nil
	}

	for _, cmd := range commands {
		if args[0] == cmd.name {
			return cmd.run(ctx, args[1:], stdout, stderr)
		}
	}

	fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
	printUsage(stderr)
	return ErrUsage
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `agent-audit records local audit trails for AI coding agents.

Usage:
  agent-audit run [--agent name] [--db path] -- <command> [args...]
  agent-audit sessions [--db path] [--limit n]
  agent-audit show [--db path] <session-id>
  agent-audit export [--db path] <session-id>
  agent-audit doctor

Examples:
  agent-audit run -- codex
  agent-audit run --agent claude -- claude
  agent-audit sessions
`)
}

func runCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", defaultDBPath(), "SQLite database path")
	agent := fs.String("agent", "", "agent name to store in the session")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}

	cmdArgs := fs.Args()
	if len(cmdArgs) > 0 && cmdArgs[0] == "--" {
		cmdArgs = cmdArgs[1:]
	}
	if len(cmdArgs) == 0 {
		fmt.Fprintln(stderr, "missing command after --")
		return ErrUsage
	}

	if *agent == "" {
		*agent = filepath.Base(cmdArgs[0])
	}

	store, err := audit.OpenStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	opts := audit.RunOptions{
		Agent:   *agent,
		Argv:    cmdArgs,
		Cwd:     mustGetwd(),
		Env:     os.Environ(),
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Started: time.Now(),
	}

	exitCode, sessionID, err := audit.RunPTY(ctx, store, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "\nagent-audit: recorded session %d in %s\n", sessionID, *dbPath)

	if exitCode != 0 {
		return ExitError{Code: exitCode}
	}
	return nil
}

func sessionsCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	_ = ctx
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", defaultDBPath(), "SQLite database path")
	limit := fs.Int("limit", 20, "maximum sessions to list")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}

	store, err := audit.OpenStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	sessions, err := store.ListSessions(*limit)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Fprintln(stdout, "No sessions recorded.")
		return nil
	}

	fmt.Fprintf(stdout, "%-6s %-14s %-19s %-9s %s\n", "ID", "AGENT", "STARTED", "EXIT", "COMMAND")
	for _, s := range sessions {
		fmt.Fprintf(stdout, "%-6d %-14s %-19s %-9s %s\n",
			s.ID,
			truncate(s.Agent, 14),
			s.StartedAt.Local().Format("2006-01-02 15:04:05"),
			formatExit(s.ExitCode),
			strings.Join(s.Argv, " "),
		)
	}
	return nil
}

func showCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	_ = ctx
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", defaultDBPath(), "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "show requires a session id")
		return ErrUsage
	}
	sessionID, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid session id: %w", err)
	}

	store, err := audit.OpenStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	session, chunks, err := store.GetSessionWithTranscript(sessionID)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Session %d\n", session.ID)
	fmt.Fprintf(stdout, "Agent:   %s\n", session.Agent)
	fmt.Fprintf(stdout, "Command: %s\n", strings.Join(session.Argv, " "))
	fmt.Fprintf(stdout, "Cwd:     %s\n", session.Cwd)
	fmt.Fprintf(stdout, "Started: %s\n", session.StartedAt.Local().Format(time.RFC3339))
	if session.EndedAt != nil {
		fmt.Fprintf(stdout, "Ended:   %s\n", session.EndedAt.Local().Format(time.RFC3339))
	}
	fmt.Fprintf(stdout, "Exit:    %s\n\n", formatExit(session.ExitCode))
	for _, chunk := range chunks {
		if chunk.Stream == "pty" {
			fmt.Fprint(stdout, chunk.Content)
		}
	}
	return nil
}

func exportCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	_ = ctx
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", defaultDBPath(), "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "export requires a session id")
		return ErrUsage
	}
	sessionID, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid session id: %w", err)
	}

	store, err := audit.OpenStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	return store.ExportSessionJSON(stdout, sessionID)
}

func doctorCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	_ = ctx
	if len(args) != 0 {
		fmt.Fprintln(stderr, "doctor does not accept arguments")
		return ErrUsage
	}
	dbPath := defaultDBPath()
	store, err := audit.OpenStore(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	fmt.Fprintf(stdout, "database: %s\n", dbPath)
	fmt.Fprintln(stdout, "status: ok")
	return nil
}

func defaultDBPath() string {
	if path := os.Getenv("AGENT_AUDIT_DB"); path != "" {
		return path
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "agent-audit", "audit.sqlite3")
	}
	return "agent-audit.sqlite3"
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "."
}

func formatExit(code *int) string {
	if code == nil {
		return "running"
	}
	return strconv.Itoa(*code)
}
