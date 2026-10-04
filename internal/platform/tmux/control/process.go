package tmuxcc

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	tmuxexec "github.com/colonyops/hive/internal/platform/tmux/exec"
	"github.com/colonyops/hive/pkg/executil"
)

// process is the seam that makes the reader/exit/join logic testable without
// real tmux: os/exec satisfies it in production, in-memory pipes in tests.
type process interface {
	Start(ctx context.Context) (stdin io.Writer, stdout io.Reader, err error)
	Wait() error
	Kill() error
}

// defaultBinary is the fallback when no caller resolved one: what $PATH says.
// This package holds no discovery policy — locating tmux is the composition
// root's job (ADR tmux-discovery).
const defaultBinary = "tmux"

type execProcess struct {
	slug   string
	binary string
	env    []string

	mu     sync.Mutex
	cmd    *exec.Cmd
	stderr *executil.HeadWriter

	waitOnce sync.Once
	waitErr  error
}

func newExecProcess(opts Options) process {
	return &execProcess{
		slug:   opts.Slug,
		binary: opts.Binary,
		env:    withoutTmuxClient(opts.Environ),
		stderr: &executil.HeadWriter{Max: 4 << 10},
	}
}

// Start ignores ctx deliberately: the control client outlives the attach
// request that spawned it, so it is killed explicitly on teardown rather than
// bound to a context.
func (p *execProcess) Start(context.Context) (io.Writer, io.Reader, error) {
	// Hive's session-creating commands run with $TMUX intact, so when this
	// process is itself inside tmux the sessions live on the server $TMUX
	// names — which outranks TMUX_TMPDIR for those commands but not for our
	// scrubbed client. Passing that socket explicitly keeps attach and create
	// pointed at the same server; outside tmux both resolve identically.
	args := []string{"-C", "attach", "-t", p.slug}
	if socket := socketFromTMUX(os.Getenv("TMUX")); socket != "" {
		args = append([]string{"-S", socket}, args...)
	}
	cmd := exec.Command(p.binary, args...) //nolint:noctx // lifetime is teardown-managed, see above
	cmd.Env = p.env
	cmd.Stderr = p.stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("tmuxcc: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("tmuxcc: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("tmuxcc: start tmux: %w", err)
	}

	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()
	return stdin, stdout, nil
}

// Wait is callable more than once: the attach path reports why a child died
// before the handshake, and teardown reaps it again.
func (p *execProcess) Wait() error {
	p.waitOnce.Do(func() {
		p.mu.Lock()
		cmd := p.cmd
		p.mu.Unlock()
		if cmd == nil {
			return
		}
		p.waitErr = cmd.Wait()
		if p.waitErr != nil {
			if msg := strings.TrimSpace(p.stderr.String()); msg != "" {
				p.waitErr = fmt.Errorf("%w: %s", p.waitErr, msg)
			}
		}
	})
	return p.waitErr
}

func (p *execProcess) Kill() error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// runTmux runs a one-shot tmux command and returns stdout as lines.
//
// env is what the command client runs with, and a new-session's pane inherits
// it: a tmux pane takes its environment from the client that created it, so
// this is the only place the app can put anything on an agent's PATH.
func runTmux(ctx context.Context, binary string, env []string, args ...string) ([]string, error) {
	out, _, err := oneShotRunner(binary, env).Capture(ctx, args...)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimRight(string(out), "\n")
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

// inputTmux exists for load-buffer: pasted text passed as an argument would be
// readable in the process table by anything running as this user.
func inputTmux(ctx context.Context, binary string, env []string, stdin io.Reader, args ...string) error {
	_, _, err := oneShotRunner(binary, env).Input(ctx, stdin, args...)
	return err
}

// oneShotRunner targets the server the control clients attach to; see Start
// for why the socket is passed explicitly.
func oneShotRunner(binary string, env []string) tmuxexec.Runner {
	if binary == "" {
		binary = defaultBinary
	}
	return tmuxexec.NewExecRunner(tmuxexec.ExecRunnerOptions{
		Binary:      func(context.Context) (string, error) { return binary, nil },
		Environ:     func(context.Context) []string { return withoutTmuxClient(env) },
		PrepareArgs: RunnerArgs,
	})
}

// socketFromTMUX extracts the server socket path from a $TMUX value
// ("<socket>,<pid>,<session>"). Empty when unset or malformed.
func socketFromTMUX(v string) string {
	socket, _, found := strings.Cut(v, ",")
	if !found {
		return ""
	}
	return socket
}

// RunnerEnviron adapts Desktop's resolved environment for one-shot tmux
// commands without exposing the parent client's tmux variables to child panes.
func RunnerEnviron(environ func(context.Context) []string) func(context.Context) []string {
	return func(ctx context.Context) []string {
		if environ == nil {
			return withoutTmuxClient(nil)
		}
		return withoutTmuxClient(environ(ctx))
	}
}

// RunnerArgs keeps one-shot commands on an inherited custom tmux socket after
// RunnerEnviron removes the parent client's tmux variables.
func RunnerArgs(args []string) []string {
	socket := socketFromTMUX(os.Getenv("TMUX"))
	if socket == "" {
		return args
	}
	return append([]string{"-S", socket}, args...)
}

// withoutTmuxClient drops base's tmux client variables so the command runs as
// an independent client rather than nesting. A nil base is this process's own
// environment, which is what a caller outside the app's composition root gets.
func withoutTmuxClient(base []string) []string {
	return executil.WithoutEnv(base, "TMUX", "TMUX_PANE")
}
