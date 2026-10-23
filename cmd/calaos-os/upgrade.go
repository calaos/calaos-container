package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/raoulh/go-progress"

	"github.com/calaos/calaos-container/cmd/calaos-os/api"
	"github.com/calaos/calaos-container/events"
	"github.com/calaos/calaos-container/models/structs"
)

var (
	// upgradeOverallTimeout bounds the whole upgrade, so a daemon that stops
	// publishing mid-upgrade does not hang the CLI forever.
	upgradeOverallTimeout = 30 * time.Minute

	// The daemon restarts after upgrading itself; Restart=always brings it
	// back within seconds.
	reconnectFirstDelay = time.Second
	reconnectMaxDelay   = 10 * time.Second
	reconnectGiveUp     = 2 * time.Minute

	// legacyGrace is how long the legacy path waits for the terminal
	// progress once the synchronous POST has answered.
	legacyGrace = 3 * time.Second
)

// exitError carries the exit code of a failed command.
type exitError struct {
	err  error
	code int
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func withCode(err error, code int) error { return &exitError{err: err, code: code} }

func exitCode(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return classifyAPIErr(err)
}

// eventStream reads the daemon events of one WebSocket connection, in order.
type eventStream struct {
	conn *websocket.Conn
	evs  chan api.Event
	errs chan error
	done chan struct{}
	once sync.Once
}

// newEventStream drops the first upgrade_progress when skipSnapshot is set:
// it is the state before our request, possibly the end of an older run.
func newEventStream(conn *websocket.Conn, skipSnapshot bool) *eventStream {
	s := &eventStream{conn: conn, evs: make(chan api.Event), errs: make(chan error, 1), done: make(chan struct{})}
	go func() {
		first := true
		for {
			var ev api.Event
			if err := conn.ReadJSON(&ev); err != nil {
				s.errs <- err
				return
			}
			if ev.Type == events.EventUpgradeProgress && first {
				first = false
				if skipSnapshot {
					continue
				}
			}
			select {
			case s.evs <- ev:
			case <-s.done:
				return
			}
		}
	}()
	return s
}

func (s *eventStream) close() {
	s.once.Do(func() {
		close(s.done)
		_ = s.conn.Close()
	})
}

// progressView draws the upgrade progress bar.
type progressView struct {
	bar        *progress.ProgressBar
	currentPkg string
}

func newProgressView() *progressView {
	bar := progress.New(100)
	bar.Format = progress.ProgressFormats[0]
	bar.ShowNumeric = false
	bar.ShowTextSuffix = true
	return &progressView{bar: bar}
}

// clearLine erases the live bar so permanent output never lands after its
// padding.
func (v *progressView) clearLine() { fmt.Print("\r\x1b[2K") }

func (v *progressView) render(st structs.Status) {
	// "idle" is the return to rest after a run: nothing to draw.
	if st.Status == "idle" || st.Status == "" {
		return
	}
	// Global phases (download, cache rebuild) have no CurrentPkg: they only
	// move the total.
	if st.CurrentPkg != "" {
		if v.currentPkg != "" && v.currentPkg != st.CurrentPkg {
			v.clearLine()
			fmt.Printf("%s %s installed\n", green(CharCheck), v.currentPkg)
		}
		v.currentPkg = st.CurrentPkg
		v.bar.SetTextSuffix(fmt.Sprintf("  [%d/%d] %s (%s) %d%%", st.PkgIndex, st.PkgCount, st.CurrentPkg, st.CurrentStep, st.CurrentPercent))
	}
	v.bar.Set(st.TotalPercent)
}

func (v *progressView) succeeded() {
	v.clearLine()
	if v.currentPkg != "" {
		fmt.Printf("%s %s installed\n", green(CharCheck), v.currentPkg)
	}
	fmt.Printf("%s Done.\n", green(CharCheck))
}

// runUpgrade upgrades pkg (everything when empty) as a job and follows it on
// the events, or runs the legacy synchronous upgrade on a daemon without
// jobs.
func runUpgrade(a *api.CalaosApi, token, pkg string) error {
	// Subscribed before the request: its events start at once.
	conn, err := a.EventsWS(token)
	if err != nil {
		if errors.Is(err, api.ErrDaemonTooOld) {
			return withCode(errors.New("le daemon calaos-container est trop ancien et ne supporte pas /api/events. Redémarrez le service (systemctl restart calaos-container) puis réessayez"), ExitServer)
		}
		return err
	}
	stream := newEventStream(conn, true)
	defer func() { stream.close() }()

	job, err := a.StartUpgrade(token, pkg)
	if errors.Is(err, api.ErrNoJobs) {
		return legacyUpgrade(a, token, pkg, stream)
	}
	if err != nil {
		if id := api.InProgressJobID(err); id != "" {
			err = fmt.Errorf("%w (job %s)", err, id)
		}
		return err
	}

	view := newProgressView()
	deadline := time.After(upgradeOverallTimeout)
	for {
		select {
		case ev := <-stream.evs:
			if end := handleJobEvent(view, job.ID, ev); end != nil {
				return jobResult(view, end)
			}

		case <-stream.errs:
			// The daemon exits after upgrading itself: follow the job on the
			// next daemon.
			stream.close()
			view.clearLine()
			next, end, err := resume(a, token, job.ID)
			if err != nil {
				return withCode(fmt.Errorf("connexion au daemon perdue: %w", err), ExitNetwork)
			}
			if end != nil {
				return jobResult(view, end)
			}
			stream = next

		case <-deadline:
			view.clearLine()
			return withCode(fmt.Errorf("upgrade timed out after %s waiting for the daemon", upgradeOverallTimeout), ExitNetwork)
		}
	}
}

// handleJobEvent draws progress and returns the job once it has ended.
func handleJobEvent(view *progressView, id string, ev api.Event) *api.Job {
	switch ev.Type {
	case events.EventUpgradeProgress:
		var st structs.Status
		if json.Unmarshal(ev.Data, &st) == nil {
			view.render(st)
		}
	case events.EventUpgradeJob:
		var j api.Job
		if json.Unmarshal(ev.Data, &j) == nil && j.ID == id && j.Terminal() {
			return &j
		}
	}
	return nil
}

// resume reconnects with backoff, then reads the job: events are not
// replayed, the persisted job is the truth. It returns the job when it has
// ended meanwhile, else a stream to follow it on.
func resume(a *api.CalaosApi, token, id string) (*eventStream, *api.Job, error) {
	delay := reconnectFirstDelay
	giveUp := time.Now().Add(reconnectGiveUp)
	for {
		time.Sleep(delay)
		conn, err := a.EventsWS(token)
		if err == nil {
			stream := newEventStream(conn, false)
			job, jerr := a.JobStatus(token)
			switch {
			case jerr != nil:
				err = jerr
			case job == nil || job.ID != id:
				stream.close()
				return nil, nil, fmt.Errorf("the daemon no longer knows job %s", id)
			case job.Terminal():
				stream.close()
				return nil, job, nil
			default:
				return stream, nil, nil
			}
			stream.close()
		}
		if time.Now().After(giveUp) {
			return nil, nil, err
		}
		if delay *= 2; delay > reconnectMaxDelay {
			delay = reconnectMaxDelay
		}
	}
}

func jobResult(view *progressView, j *api.Job) error {
	if j.Status != "done" {
		view.clearLine()
		msg := j.Error
		if msg == "" {
			msg = "upgrade failed"
		}
		return withCode(fmt.Errorf("%s (job %s, details in journalctl -u calaos-container)", msg, j.ID), ExitServer)
	}
	view.succeeded()
	if j.RebootRequired {
		fmt.Printf("%s A reboot is required to finish the upgrade.\n", cyan(CharArrow))
	}
	return nil
}

// legacyUpgrade drives the synchronous upgrade of a daemon without jobs: the
// POST answers when apt ends, progress comes from the events.
func legacyUpgrade(a *api.CalaosApi, token, pkg string, stream *eventStream) error {
	postDone := make(chan error, 1)
	go func() {
		if pkg != "" {
			postDone <- a.UpdatePackage(token, pkg)
		} else {
			postDone <- a.UpgradePackages(token)
		}
	}()

	view := newProgressView()
	var st structs.Status
	var postErr error
	postCh := postDone
	var giveUp <-chan time.Time
	deadline := time.After(upgradeOverallTimeout)

readLoop:
	for {
		select {
		case ev := <-stream.evs:
			if ev.Type != events.EventUpgradeProgress {
				continue
			}
			var s structs.Status
			if json.Unmarshal(ev.Data, &s) != nil || s.Status == "idle" {
				continue
			}
			st = s
			view.render(st)
			if st.Status == "done" || st.Status == "error" {
				break readLoop
			}

		case err := <-stream.errs:
			view.clearLine()
			return withCode(fmt.Errorf("connexion au daemon perdue: %w", err), ExitNetwork)

		case postErr = <-postCh:
			postCh = nil
			if postErr != nil {
				// Refused before it started (unknown package, run in
				// progress): no terminal event will come.
				break readLoop
			}
			giveUp = time.After(legacyGrace)

		case <-giveUp:
			break readLoop

		case <-deadline:
			view.clearLine()
			return withCode(fmt.Errorf("upgrade timed out after %s waiting for the daemon", upgradeOverallTimeout), ExitNetwork)
		}
	}

	stream.close()
	if postCh != nil {
		postErr = <-postDone
	}
	view.clearLine()

	if st.Status == "error" {
		return withCode(errors.New(st.Error), ExitServer)
	}
	if postErr != nil {
		return postErr
	}
	view.succeeded()
	return nil
}
