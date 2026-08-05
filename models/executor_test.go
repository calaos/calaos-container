package models

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeStubScript writes an executable shell script standing in for a real
// long-running command (calaos_install is destructive and must never run in a
// test) and returns its path.
func writeStubScript(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "stub.sh")
	// #nosec G306 -- a stub standing in for an installer has to be executable.
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("writing stub script: %v", err)
	}

	return path
}

// readTestFile reads a file created by the test itself.
func readTestFile(t *testing.T, path string) string {
	t.Helper()

	// #nosec G304 -- the path is built by the test from t.TempDir().
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	return string(data)
}

// waitDone waits for a process handle to report completion.
func waitDone(t *testing.T, p Process, timeout time.Duration) {
	t.Helper()

	select {
	case <-p.Done():
	case <-time.After(timeout):
		t.Fatalf("process (pid %d) did not finish within %s", p.Pid(), timeout)
	}
}

func readExitCodeFile(t *testing.T, path string) string {
	t.Helper()

	return strings.TrimSpace(readTestFile(t, path))
}

// TestStartLoggedCommandFileContract covers the contract the HTTP layer relies
// on: output lands in the log file, the exit status lands in the exit code
// file when the command finishes, and a stale exit code from a previous run is
// cleared at start.
func TestStartLoggedCommandFileContract(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "run", "stub.log")
	exitFile := filepath.Join(dir, "run", "stub.code")

	if err := os.MkdirAll(filepath.Dir(exitFile), 0o750); err != nil {
		t.Fatalf("preparing run dir: %v", err)
	}
	// Stale result of an imaginary previous run.
	if err := os.WriteFile(exitFile, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("writing stale exit code: %v", err)
	}

	stub := writeStubScript(t, `
echo "installing on $1"
echo "oops" >&2
sleep 0.3
exit 7`)

	proc, err := StartLoggedCommand(LoggedCommand{
		Name:         stub,
		Args:         []string{"/dev/null-device"},
		LogFile:      logFile,
		ExitCodeFile: exitFile,
	})
	if err != nil {
		t.Fatalf("StartLoggedCommand: %v", err)
	}

	if proc.Pid() <= 0 {
		t.Errorf("expected a valid pid, got %d", proc.Pid())
	}
	if _, ok := proc.ExitCode(); ok {
		t.Error("exit code reported as available while the command is still running")
	}
	if _, err := os.Stat(exitFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale exit code file still present while running (stat err: %v)", err)
	}

	waitDone(t, proc, 10*time.Second)

	code, ok := proc.ExitCode()
	if !ok {
		t.Fatal("exit code still unavailable after Done()")
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if got := readExitCodeFile(t, exitFile); got != "7" {
		t.Errorf("exit code file = %q, want \"7\"", got)
	}

	logged := readTestFile(t, logFile)
	if !strings.Contains(logged, "installing on /dev/null-device") {
		t.Errorf("stdout missing from log file: %q", logged)
	}
	if !strings.Contains(logged, "oops") {
		t.Errorf("stderr missing from log file: %q", logged)
	}
}

func TestStartLoggedCommandRequiresLogFile(t *testing.T) {
	if _, err := StartLoggedCommand(LoggedCommand{Name: "/bin/true"}); err == nil {
		t.Fatal("expected an error when LogFile is empty, got nil")
	}

	if _, err := StartLoggedCommand(LoggedCommand{LogFile: filepath.Join(t.TempDir(), "x.log")}); err == nil {
		t.Fatal("expected an error when Name is empty, got nil")
	}
}

// TestTailFileFollowsUntilDone checks the streaming side: the tail follows the
// file while the command produces output and returns nil once everything has
// been forwarded, including the lines written just before the command exited.
func TestTailFileFollowsUntilDone(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "stub.log")

	stub := writeStubScript(t, `
for i in 1 2 3 4 5; do
    echo "step $i"
    sleep 0.05
done
echo "done"`)

	proc, err := StartLoggedCommand(LoggedCommand{
		Name:    stub,
		LogFile: logFile,
	})
	if err != nil {
		t.Fatalf("StartLoggedCommand: %v", err)
	}

	var out bytes.Buffer
	err = TailFile(context.Background(), logFile, &out, TailOptions{
		Until:        proc.Done(),
		PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("TailFile: %v", err)
	}

	for _, want := range []string{"step 1", "step 5", "done"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("streamed output missing %q: %q", want, out.String())
		}
	}
}

func TestTailFileStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "stub.log")
	if err := os.WriteFile(logFile, []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("writing log file: %v", err)
	}

	// No Until channel: only the context can end this tail.
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer

	errCh := make(chan error, 1)
	go func() {
		errCh <- TailFile(ctx, logFile, &out, TailOptions{PollInterval: 10 * time.Millisecond})
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("TailFile error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TailFile did not return after the context was cancelled")
	}

	if !strings.Contains(out.String(), "hello") {
		t.Errorf("output written before cancellation was lost: %q", out.String())
	}
}

// brokenWriter fails on the first write, standing in for an HTTP client that
// disconnected mid-stream.
type brokenWriter struct {
	mu    sync.Mutex
	calls int
}

var errClientGone = errors.New("client gone")

func (b *brokenWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++

	return 0, errClientGone
}

// TestClientDisconnectDoesNotKillCommand is the core regression test for
// CC-021: a client vanishing mid-stream ends the tail only. The command keeps
// running and still publishes its exit status.
func TestClientDisconnectDoesNotKillCommand(t *testing.T) {
	goroutinesBefore := runtime.NumGoroutine()

	dir := t.TempDir()
	logFile := filepath.Join(dir, "stub.log")
	exitFile := filepath.Join(dir, "stub.code")

	// Emits early output (so the tail has something to fail on), then keeps
	// working well past the client's disappearance.
	stub := writeStubScript(t, `
echo "starting"
sleep 2
echo "finished"
exit 0`)

	proc, err := StartLoggedCommand(LoggedCommand{
		Name:         stub,
		LogFile:      logFile,
		ExitCodeFile: exitFile,
	})
	if err != nil {
		t.Fatalf("StartLoggedCommand: %v", err)
	}

	w := &brokenWriter{}
	tailStart := time.Now()
	err = TailFile(context.Background(), logFile, w, TailOptions{
		Until:        proc.Done(),
		PollInterval: 10 * time.Millisecond,
	})
	tailDuration := time.Since(tailStart)

	if !errors.Is(err, errClientGone) {
		t.Fatalf("TailFile error = %v, want the write error %v", err, errClientGone)
	}
	if tailDuration > time.Second {
		t.Errorf("tail took %s to notice the broken client, expected it to bail out immediately", tailDuration)
	}

	// The command must still be alive: the tail is gone, it is not.
	if _, ok := proc.ExitCode(); ok {
		t.Fatal("command already finished when the client disconnected; test cannot prove it survived")
	}

	waitDone(t, proc, 10*time.Second)

	code, ok := proc.ExitCode()
	if !ok || code != 0 {
		t.Errorf("exit code = %d (available: %t), want 0", code, ok)
	}
	if got := readExitCodeFile(t, exitFile); got != "0" {
		t.Errorf("exit code file = %q, want \"0\"", got)
	}

	// The command ran to completion, writing everything to its log file even
	// though nobody was reading.
	if logged := readTestFile(t, logFile); !strings.Contains(logged, "finished") {
		t.Errorf("command did not run to completion, log: %q", logged)
	}

	// The whole run must be re-readable after the fact, which is what lets a
	// client reconnect and replay the log.
	var replay bytes.Buffer
	if err := TailFile(context.Background(), logFile, &replay, TailOptions{
		Until:        proc.Done(),
		PollInterval: 10 * time.Millisecond,
	}); err != nil {
		t.Fatalf("re-tailing a finished run: %v", err)
	}
	if !strings.Contains(replay.String(), "starting") || !strings.Contains(replay.String(), "finished") {
		t.Errorf("replayed log incomplete: %q", replay.String())
	}

	// Spot check for leaked goroutines (the reaper is the only one this test
	// starts, and it is gone once Done() is closed). Formal coverage: CC-054.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > goroutinesBefore && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > goroutinesBefore {
		t.Errorf("goroutine count grew from %d to %d", goroutinesBefore, after)
	}
}

// fakeExecutor is the kind of stand-in the Executor seam exists for.
type fakeExecutor struct {
	name   string
	args   []string
	output string
	err    error
}

func (f *fakeExecutor) Run(_ context.Context, name string, args ...string) (string, error) {
	f.name = name
	f.args = args

	return f.output, f.err
}

func (f *fakeExecutor) StartLogged(LoggedCommand) (Process, error) {
	return nil, errors.New("not implemented")
}

func TestRunCommandUsesExecutorSeam(t *testing.T) {
	fake := &fakeExecutor{output: "fake output"}
	restore := SetExecutor(fake)

	out, err := RunCommand("lsblk", "--json")
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if out != "fake output" {
		t.Errorf("output = %q, want %q", out, "fake output")
	}
	if fake.name != "lsblk" || len(fake.args) != 1 || fake.args[0] != "--json" {
		t.Errorf("executor received %q %v, want lsblk [--json]", fake.name, fake.args)
	}

	restore()

	if _, ok := CurrentExecutor().(OSExecutor); !ok {
		t.Errorf("restore() did not put the default executor back, got %T", CurrentExecutor())
	}
}

func TestRunCommandRealExecutor(t *testing.T) {
	out, err := RunCommand("/bin/echo", "hello")
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("output = %q, want %q", out, "hello")
	}

	if _, err := RunCommand("/bin/sh", "-c", "exit 3"); err == nil {
		t.Error("expected an error for a command exiting non-zero, got nil")
	}
}

func TestRunCommandContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := RunCommandContext(ctx, "/bin/sh", "-c", "sleep 10"); err == nil {
		t.Fatal("expected an error when the context expires, got nil")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancelled command took %s to return", elapsed)
	}
}
