package models

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Executor is the seam through which this package runs external commands. It
// exists so tests can substitute a fake for the real fork/exec (see
// SetExecutor) and so the two very different execution modes the daemon needs
// are explicit:
//
//   - Run is for short, synchronous commands whose stdout is the result
//     (lsblk, findmnt, snapper, apt-get). The caller's context bounds the run.
//   - StartLogged is for long-running commands that must outlive the HTTP
//     request that triggered them (calaos_install). Their output goes to a
//     file, never to an in-process pipe, so no reader can ever throttle or
//     hang the child.
type Executor interface {
	// Run executes name with args, waits for it to finish and returns its
	// stdout. Cancelling ctx kills the process.
	Run(ctx context.Context, name string, args ...string) (string, error)

	// StartLogged starts a command detached from its requester and returns as
	// soon as the child is running. It deliberately takes no context: the
	// lifetime of the process must not be tied to the caller (see
	// LoggedCommand).
	StartLogged(spec LoggedCommand) (Process, error)
}

// LoggedCommand describes a long-running command whose lifetime is
// independent of whoever asked for it.
//
// The contract with the outside world is made of two files:
//
//   - LogFile receives the child's stdout *and* stderr. It is truncated when
//     the command starts and is written by the child itself (the kernel hands
//     it its own duplicate of the file descriptor), so the daemon is never in
//     the data path: a slow or vanished reader cannot block the child.
//   - ExitCodeFile is removed when the command starts and (re)created with the
//     decimal exit status once the command terminates, whatever happened to
//     the client meanwhile. Its presence therefore means "this run finished",
//     and it is written atomically (temp file + rename) so a reader never
//     observes a partial value.
//
// Consumers stream progress by tailing LogFile (see TailFile) and learn the
// outcome from Process.Done()/ExitCodeFile.
type LoggedCommand struct {
	// Name is the executable to run, Args its arguments.
	Name string
	Args []string

	// LogFile is the path receiving stdout+stderr. Required: without it the
	// output would be discarded and nothing could be streamed.
	LogFile string

	// ExitCodeFile is the path receiving the exit status. Optional; when
	// empty no exit status file is maintained (Process.ExitCode still works).
	ExitCodeFile string

	// Env holds extra "KEY=value" entries appended to the daemon environment.
	Env []string
}

// Process is a handle on a command started by Executor.StartLogged. It only
// allows *observing* the child: nothing here can block it, and dropping the
// handle does not affect it.
type Process interface {
	// Pid is the process id of the child.
	Pid() int

	// Done is closed once the child has exited and its exit status has been
	// persisted to LoggedCommand.ExitCodeFile.
	Done() <-chan struct{}

	// ExitCode reports the exit status of the child. ok is false while it is
	// still running. A child killed by a signal reports -1, as
	// os.ProcessState.ExitCode does.
	ExitCode() (code int, ok bool)
}

// runCancelGrace is how long Run waits, after the command was killed by a
// context cancellation or exited, for the output pipes to be released before
// closing them itself.
const runCancelGrace = 2 * time.Second

// OSExecutor is the default Executor: it really forks and execs.
type OSExecutor struct{}

var _ Executor = OSExecutor{}

var (
	executorMu sync.RWMutex
	executor   Executor = OSExecutor{}
)

// CurrentExecutor returns the executor currently used by RunCommand and
// StartLoggedCommand.
func CurrentExecutor() Executor {
	executorMu.RLock()
	defer executorMu.RUnlock()
	return executor
}

// SetExecutor swaps the package-level executor and returns a function that
// restores the previous one, for use as `defer SetExecutor(fake)()` in tests.
// Passing nil restores the default OSExecutor.
func SetExecutor(e Executor) (restore func()) {
	if e == nil {
		e = OSExecutor{}
	}

	executorMu.Lock()
	previous := executor
	executor = e
	executorMu.Unlock()

	return func() {
		executorMu.Lock()
		executor = previous
		executorMu.Unlock()
	}
}

// Run implements Executor.
func (OSExecutor) Run(ctx context.Context, name string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	logging.Debugf("RunCommand: %s %v", name, args)

	// #nosec G204 -- the command and its arguments come from the daemon
	// itself; the only externally influenced value (the install target
	// device) is validated against the block device inventory before it ever
	// reaches this point.
	cmd := exec.CommandContext(ctx, name, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Without this, cancelling ctx kills the command but Run keeps waiting on
	// the output pipes until every process that inherited them is gone: a
	// forgotten grandchild (`sh -c "sleep 10"` keeps the pipe open after sh
	// is killed) would hold the caller hostage long after the cancellation.
	cmd.WaitDelay = runCancelGrace

	//add environment variables DEBIAN_FRONTEND="noninteractive" for apt-get
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")

	if err := cmd.Run(); err != nil {
		e := fmt.Errorf("RunCommand error: %s, stderr: %s", err, stderr.String())
		logging.Error(e)
		return "", e
	}

	return stdout.String(), nil
}

// StartLogged implements Executor. The child is started in its own session
// (setsid) with stdin on /dev/null and stdout/stderr connected straight to the
// log file, so it neither inherits the daemon's terminal/signals nor depends
// on any in-process reader. A goroutine reaps it and records its exit status;
// that goroutine only writes files and closes a channel, so it can never
// block.
func (OSExecutor) StartLogged(spec LoggedCommand) (Process, error) {
	if spec.Name == "" {
		return nil, errors.New("StartLogged: no command given")
	}
	if spec.LogFile == "" {
		return nil, errors.New("StartLogged: LogFile is required")
	}

	if err := os.MkdirAll(filepath.Dir(spec.LogFile), 0o750); err != nil {
		return nil, fmt.Errorf("StartLogged: cannot create log directory: %w", err)
	}

	// #nosec G304 -- the log path is chosen by the daemon (a package
	// constant), never by an API client.
	logFile, err := os.OpenFile(spec.LogFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("StartLogged: cannot open log file %s: %w", spec.LogFile, err)
	}

	// A leftover exit status from a previous run must not be mistaken for the
	// result of this one while it is still running.
	if spec.ExitCodeFile != "" {
		if rerr := os.Remove(spec.ExitCodeFile); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			_ = logFile.Close()
			return nil, fmt.Errorf("StartLogged: cannot clear exit code file %s: %w", spec.ExitCodeFile, rerr)
		}
	}

	logging.Debugf("StartLogged: %s %v (log: %s)", spec.Name, spec.Args, spec.LogFile)

	// #nosec G204 -- see the note in Run: command and arguments are daemon
	// controlled and the install device is validated by the caller.
	cmd := exec.Command(spec.Name, spec.Args...)
	cmd.Stdin = nil // /dev/null: the child must never wait on input
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	cmd.Env = append(cmd.Env, spec.Env...)
	// Own session: the child survives signals aimed at the daemon's process
	// group and has no controlling terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("StartLogged: cannot start %s: %w", spec.Name, err)
	}

	// The child got its own duplicate of the descriptor; keeping ours open
	// would leak one file descriptor per run.
	if cerr := logFile.Close(); cerr != nil {
		logging.Errorf("StartLogged: closing %s: %v", spec.LogFile, cerr)
	}

	p := &osProcess{
		pid:  cmd.Process.Pid,
		done: make(chan struct{}),
	}
	go p.reap(cmd, spec.ExitCodeFile)

	return p, nil
}

// osProcess is the Process implementation returned by OSExecutor.StartLogged.
type osProcess struct {
	pid  int
	done chan struct{}
	code atomic.Int32
}

func (p *osProcess) Pid() int { return p.pid }

func (p *osProcess) Done() <-chan struct{} { return p.done }

func (p *osProcess) ExitCode() (int, bool) {
	select {
	case <-p.done:
		return int(p.code.Load()), true
	default:
		return 0, false
	}
}

// reap waits for the child, publishes its exit status to the exit code file
// and only then closes done, so that a reader woken by done always finds the
// file in place.
func (p *osProcess) reap(cmd *exec.Cmd, exitCodeFile string) {
	err := cmd.Wait()

	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// Not an exit status: the process could not be waited for at all.
			logging.Errorf("Command %s (pid %d) failed: %v", cmd.Path, p.pid, err)
		}
	}

	logging.Debugf("Command %s (pid %d) exited with status %d", cmd.Path, p.pid, code)

	if exitCodeFile != "" {
		if werr := writeExitCodeFile(exitCodeFile, code); werr != nil {
			logging.Errorf("Error writing exit code file %s: %v", exitCodeFile, werr)
		}
	}

	p.code.Store(int32(code))
	close(p.done)
}

// writeExitCodeFile atomically publishes code at path: a reader either sees
// the previous state (usually no file at all) or the complete value.
func writeExitCodeFile(path string, code int) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := fmt.Fprintf(tmp, "%d\n", code); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	return nil
}
