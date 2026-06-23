package audit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
)

type RunOptions struct {
	Agent   string
	Argv    []string
	Cwd     string
	Env     []string
	Stdin   *os.File
	Stdout  io.Writer
	Stderr  io.Writer
	Started time.Time
}

func RunPTY(ctx context.Context, store *Store, opts RunOptions) (int, int64, error) {
	if len(opts.Argv) == 0 {
		return 1, 0, errors.New("missing argv")
	}
	if opts.Started.IsZero() {
		opts.Started = time.Now()
	}

	sessionID, err := store.CreateSession(Session{
		Agent:     opts.Agent,
		Argv:      opts.Argv,
		Cwd:       opts.Cwd,
		RepoRoot:  detectRepoRoot(opts.Cwd),
		GitBranch: detectGitBranch(opts.Cwd),
		StartedAt: opts.Started,
	})
	if err != nil {
		return 1, 0, err
	}
	if err := store.InsertEvent(sessionID, "process.start", map[string]any{
		"argv": opts.Argv,
		"cwd":  opts.Cwd,
	}); err != nil {
		return 1, sessionID, err
	}

	cmd := exec.CommandContext(ctx, opts.Argv[0], opts.Argv[1:]...)
	cmd.Dir = opts.Cwd
	cmd.Env = opts.Env

	ptmx, err := pty.Start(cmd)
	if err != nil {
		finishErr := store.FinishSession(sessionID, time.Now(), 127)
		return 127, sessionID, errors.Join(err, finishErr)
	}
	defer ptmx.Close()

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				_, _ = opts.Stdout.Write(chunk)
				_ = store.InsertTranscript(sessionID, "pty", chunk)
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) || errors.Is(readErr, os.ErrClosed) {
					done <- nil
					return
				}
				done <- readErr
				return
			}
		}
	}()

	var inputActive atomic.Bool
	inputActive.Store(true)
	go func() {
		buf := make([]byte, 32*1024)
		wroteInput := false
		for {
			n, readErr := opts.Stdin.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				if !inputActive.Load() {
					return
				}
				_, _ = ptmx.Write(chunk)
				_ = store.InsertTranscript(sessionID, "stdin", chunk)
				wroteInput = true
			}
			if readErr != nil {
				if inputActive.Load() && wroteInput {
					_, _ = ptmx.Write([]byte{4})
				}
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	inputActive.Store(false)
	_ = ptmx.Close()
	readErr := <-done

	exitCode := exitCodeFromError(waitErr)
	ended := time.Now()
	if err := store.FinishSession(sessionID, ended, exitCode); err != nil {
		return exitCode, sessionID, err
	}
	if err := store.InsertEvent(sessionID, "process.exit", map[string]any{
		"exit_code":   exitCode,
		"duration_ms": ended.Sub(opts.Started).Milliseconds(),
	}); err != nil {
		return exitCode, sessionID, err
	}

	if readErr != nil {
		return exitCode, sessionID, readErr
	}
	if waitErr != nil && exitCode == 0 {
		return exitCode, sessionID, waitErr
	}
	return exitCode, sessionID, nil
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return 1
}

func detectRepoRoot(cwd string) string {
	out, err := gitOutput(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return out
}

func detectGitBranch(cwd string) string {
	out, err := gitOutput(cwd, "branch", "--show-current")
	if err != nil {
		return ""
	}
	return out
}

func gitOutput(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return trimTrailingNewline(string(output)), nil
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func debugf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, format, args...)
}
