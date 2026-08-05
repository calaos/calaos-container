package models

import (
	"context"
	"io"
	"os"
	"time"
)

// RunCommand runs command with args, waits for it and returns its stdout. It
// is the historical, context-less entry point kept for the many call sites
// that just need a short command run to completion; it delegates to the
// current Executor.
func RunCommand(command string, args ...string) (string, error) {
	return RunCommandContext(context.Background(), command, args...)
}

// RunCommandContext is RunCommand bounded by ctx: cancelling ctx kills the
// command.
func RunCommandContext(ctx context.Context, command string, args ...string) (string, error) {
	return CurrentExecutor().Run(ctx, command, args...)
}

// StartLoggedCommand starts a long-running command whose output is written to
// a log file and whose exit status is published to an exit code file when it
// terminates, independently of whoever requested it. See LoggedCommand for
// the file contract and TailFile for the streaming side.
func StartLoggedCommand(spec LoggedCommand) (Process, error) {
	return CurrentExecutor().StartLogged(spec)
}

// defaultTailPollInterval is how often TailFile looks for new output once it
// has reached the end of the file.
const defaultTailPollInterval = 200 * time.Millisecond

// TailOptions tunes TailFile.
type TailOptions struct {
	// Until, when non-nil, marks the end of the file's growth: once it is
	// closed TailFile drains whatever is left and returns. Typically
	// Process.Done() of the command producing the file. When nil, TailFile
	// only stops on ctx cancellation or a write error.
	Until <-chan struct{}

	// PollInterval overrides how often the file is checked for new data once
	// at EOF. Zero means defaultTailPollInterval.
	PollInterval time.Duration
}

// TailFile copies path to w, following the file as it grows, until either
//
//   - the file reached its end and opts.Until is closed (returns nil: the
//     whole output has been forwarded),
//   - ctx is cancelled (returns ctx.Err()),
//   - or writing to w fails (returns that error) — which is how a vanished
//     HTTP client is detected.
//
// Nothing here is on the producer's path: the command writes to the file
// through its own descriptor, so cancelling, blocking or losing w has no
// effect whatsoever on it. If w exposes a `Flush() error` method (as
// bufio.Writer does), it is flushed after every chunk so output reaches the
// client as it is produced rather than when a buffer happens to fill up.
func TailFile(ctx context.Context, path string, w io.Writer, opts TailOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}

	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultTailPollInterval
	}

	// #nosec G304 -- the tailed path is a daemon constant, not client input.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			logging.Errorf("TailFile: closing %s: %v", path, cerr)
		}
	}()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	buf := make([]byte, 32*1024)

	for {
		// Always drain what is already there before considering stopping, so
		// the last lines a command wrote before exiting are never lost.
		n, rerr := f.Read(buf)
		if n > 0 {
			if werr := writeChunk(w, buf[:n]); werr != nil {
				return werr
			}
			continue
		}
		if rerr != nil && rerr != io.EOF {
			return rerr
		}

		// At EOF: stop if we were cancelled, or if the producer is done and
		// there is nothing left to read.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if isClosed(opts.Until) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-opts.Until: // nil channel simply never fires
		case <-ticker.C:
		}
	}
}

// writeChunk writes p to w and flushes w when it can be flushed.
func writeChunk(w io.Writer, p []byte) error {
	if _, err := w.Write(p); err != nil {
		return err
	}

	if f, ok := w.(interface{ Flush() error }); ok {
		return f.Flush()
	}

	return nil
}

// isClosed reports whether ch is a closed channel. A nil channel is never
// closed.
func isClosed(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}

	select {
	case <-ch:
		return true
	default:
		return false
	}
}
