package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/models"
)

const (
	logsDefaultLines = 200
	logsMaxLines     = 1000
	logsMaxSources   = 8
	logsMaxText      = 256
	logsMaxCursor    = 512
	logsScanEntries  = 5000
	logsMaxBytes     = 1 << 20
	logsDefaultSpan  = 7 * 24 * time.Hour
	logsMaxProcs     = 4
	logsRate         = 2.0
	logsLocalBucket  = "local"

	// Without -a journalctl prints null above 4096 bytes, so a sane line is
	// well under this; a longer one is dropped rather than buffered.
	logsLineCap = 256 << 10
	// Room for the envelope and next_cursor around the entries.
	logsEnvelope = 2 << 10
)

// Test seams.
var (
	journalctlCmd = "journalctl"
	logsScanTime  = 5 * time.Second
)

type logLimits struct {
	procs chan struct{}

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newLogLimits() *logLimits {
	return &logLimits{procs: make(chan struct{}, logsMaxProcs), buckets: map[string]*tokenBucket{}}
}

// allow takes a token from key's bucket. A clock reading earlier than the
// last one (a wall-clock step without a monotonic reading) refills nothing
// instead of starving the bucket until the clock catches up.
func (l *logLimits) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) > 64 {
		for k, b := range l.buckets {
			if d := now.Sub(b.last); d < 0 || d > 10*time.Second {
				delete(l.buckets, k)
			}
		}
	}
	b := l.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: logsRate, last: now}
		l.buckets[key] = b
	}
	if d := now.Sub(b.last); d > 0 {
		b.tokens += d.Seconds() * logsRate
		if b.tokens > logsRate {
			b.tokens = logsRate
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *logLimits) tryProc() bool {
	select {
	case l.procs <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l *logLimits) releaseProc() { <-l.procs }

func errLogsRateLimited() *apiError {
	e := newAPIError(fiber.StatusTooManyRequests, codeRateLimited, "too many log queries, retry later")
	e.data = fiber.Map{"retry_after": 1}
	e.retryAfter = 1
	return e
}

type logSourceInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func (a *AppServer) v2LogSources(c *fiber.Ctx) error {
	list := make([]logSourceInfo, len(models.LogSources))
	for i, s := range models.LogSources {
		list[i] = logSourceInfo{s.ID, s.Label}
	}
	return sendOK(c, fiber.StatusOK, fiber.Map{"sources": list})
}

type logsQueryBody struct {
	Sources   *[]string       `json:"sources"`
	Priority  json.RawMessage `json:"priority"`
	Since     *string         `json:"since"`
	Until     *string         `json:"until"`
	Text      *string         `json:"text"`
	Lines     *int            `json:"lines"`
	Cursor    *string         `json:"cursor"`
	Direction *string         `json:"direction"`
}

type logsRequest struct {
	q     models.JournalQuery
	lines int
	text  string
}

func parsePriority(raw json.RawMessage) (*int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var name string
		if err := json.Unmarshal(raw, &name); err != nil {
			return nil, errInvalid("invalid priority")
		}
		p, ok := models.LogPriority(name)
		if !ok {
			return nil, errInvalid("invalid priority")
		}
		return &p, nil
	}
	var p int
	if err := json.Unmarshal(raw, &p); err != nil || p < 0 || p > 7 {
		return nil, errInvalid("invalid priority")
	}
	return &p, nil
}

func parseLogTime(v *string, name string) (*time.Time, error) {
	if v == nil {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, *v)
	if err != nil {
		return nil, errInvalid("invalid " + name + ": RFC 3339 time expected")
	}
	// journalctl fails on years it cannot represent: refused here, not 500.
	if y := t.UTC().Year(); y < 1970 || y > 2100 {
		return nil, errInvalid("invalid " + name + ": year 1970 to 2100")
	}
	return &t, nil
}

func validCursor(s string) bool {
	if s == "" || len(s) > logsMaxCursor {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

func parseLogsQuery(c *fiber.Ctx, now time.Time) (logsRequest, error) {
	var b logsQueryBody
	var r logsRequest
	if len(bytes.TrimSpace(c.Body())) > 0 {
		if err := decodeBody(c, &b); err != nil {
			return r, err
		}
	}

	ids := []string{models.LogSourceSystem}
	if b.Sources != nil {
		ids = *b.Sources
	}
	if len(ids) < 1 || len(ids) > logsMaxSources {
		return r, errInvalid("sources: 1 to 8 ids")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		s, ok := models.LookupLogSource(id)
		if !ok {
			return r, errInvalid("unknown source")
		}
		if !seen[id] {
			seen[id] = true
			r.q.Sources = append(r.q.Sources, s)
		}
	}

	var err error
	if r.q.Priority, err = parsePriority(b.Priority); err != nil {
		return r, err
	}
	if r.q.Since, err = parseLogTime(b.Since, "since"); err != nil {
		return r, err
	}
	if r.q.Until, err = parseLogTime(b.Until, "until"); err != nil {
		return r, err
	}
	if r.q.Since != nil && r.q.Until != nil && !r.q.Since.Before(*r.q.Until) {
		return r, errInvalid("since must be before until")
	}

	if b.Text != nil {
		if utf8.RuneCountInString(*b.Text) > logsMaxText {
			return r, errInvalid("text: at most 256 characters")
		}
		r.text = strings.ToLower(*b.Text)
	}

	r.lines = logsDefaultLines
	if b.Lines != nil {
		if *b.Lines < 1 || *b.Lines > logsMaxLines {
			return r, errInvalid("lines: 1 to 1000")
		}
		r.lines = *b.Lines
	}

	if b.Cursor != nil {
		if !validCursor(*b.Cursor) {
			return r, errInvalid("invalid cursor")
		}
		r.q.Cursor = *b.Cursor
	}

	dir := "backward"
	if b.Direction != nil {
		dir = *b.Direction
	}
	switch dir {
	case "backward":
		if r.q.Since == nil {
			end := now
			if r.q.Until != nil {
				end = *r.q.Until
			}
			since := end.Add(-logsDefaultSpan)
			r.q.Since = &since
		}
	case "forward":
		r.q.Forward = true
		if r.q.Cursor == "" && r.q.Since == nil {
			return r, errInvalid("a forward query needs a cursor or since")
		}
	default:
		return r, errInvalid("direction: backward or forward")
	}
	return r, nil
}

func (a *AppServer) v2LogsQuery(c *fiber.Ctx) error {
	now := a.now()
	req, err := parseLogsQuery(c, now)
	if err != nil {
		return err
	}

	// Follow polls (forward from a cursor, every 2 s) are not audited; a
	// search or a page of history is.
	if req.q.Forward && req.q.Cursor != "" {
		c.Locals(localAuditSkip, true)
	} else {
		ids := make([]string, len(req.q.Sources))
		for i, s := range req.q.Sources {
			ids[i] = s.ID
		}
		dir, text := "backward", "no"
		if req.q.Forward {
			dir = "forward"
		}
		if req.text != "" {
			text = "yes"
		}
		auditField(c, "sources", strings.Join(ids, "+"))
		auditField(c, "lines", strconv.Itoa(req.lines))
		auditField(c, "dir", dir)
		auditField(c, "text", text)
	}

	key := logsLocalBucket
	if s, ok := adminSession(c); ok {
		key = "s:" + admin.SessionAuditID(s)
	}
	if !a.logs.tryProc() {
		return errLogsRateLimited()
	}
	defer a.logs.releaseProc()
	if !a.logs.allow(key, now) {
		return errLogsRateLimited()
	}

	res, err := readJournal(req)
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, res)
}

type logEntry struct {
	Cursor   string `json:"cursor"`
	TS       string `json:"ts"`
	Source   string `json:"source"`
	Unit     string `json:"unit"`
	Priority int    `json:"priority"`
	PID      *int   `json:"pid,omitempty"`
	Message  string `json:"message"`
	Cut      bool   `json:"cut,omitempty"`
}

type logsResult struct {
	Entries    []logEntry `json:"entries"`
	Truncated  bool       `json:"truncated"`
	Complete   bool       `json:"complete"`
	NextCursor *string    `json:"next_cursor"`
}

func toLogEntry(q models.JournalQuery, rec models.JournalRecord) logEntry {
	e := logEntry{
		Cursor:   rec.Cursor,
		TS:       rec.Realtime.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Source:   q.Attribute(rec),
		Unit:     rec.Unit,
		Priority: rec.Priority,
		PID:      rec.PID,
	}
	if rec.Kernel && e.Unit == "" {
		e.Unit = models.LogSourceKernel
	}
	e.Message, e.Cut = models.CutMessage(rec.Message)
	if rec.TooLong {
		e.Cut = true
	}
	return e
}

type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// readLine returns one line without its newline; overlong lines come back
// with long set and their content dropped.
func readLine(r *bufio.Reader) (line []byte, long bool, err error) {
	for {
		chunk, err := r.ReadSlice('\n')
		if !long {
			if len(line)+len(chunk) > logsLineCap {
				long, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == nil {
			line = bytes.TrimSuffix(line, []byte("\n"))
		}
		return line, long, err
	}
}

var errLongLine = errors.New("journal line too long")

func parseLine(line []byte, long bool) (models.JournalRecord, error) {
	if long {
		return models.JournalRecord{}, errLongLine
	}
	return models.ParseJournalLine(line)
}

// readJournal runs one journalctl and stops it, with its whole process
// group, once the answer is full, the scan window is spent or logsScanTime
// has passed.
func readJournal(req logsRequest) (logsResult, error) {
	res := logsResult{Entries: []logEntry{}}

	ctx, cancel := context.WithTimeout(context.Background(), logsScanTime)
	defer cancel()

	pr, pw, err := os.Pipe()
	if err != nil {
		return res, err
	}
	stderr := &cappedBuffer{max: 4 << 10}
	// #nosec G204 -- argv built by models.JournalArgs from validated values.
	cmd := exec.CommandContext(ctx, journalctlCmd, models.JournalArgs(req.q)...)
	cmd.Stdout = pw
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return os.ErrProcessDone
	}
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return res, err
	}
	_ = pw.Close()
	// A process that left the group could keep the pipe open: closing our
	// end on cancel is what unblocks the read.
	go func() {
		<-ctx.Done()
		_ = pr.Close()
	}()

	var (
		scanned  int
		size     = logsEnvelope
		last     string
		finished bool
		past     bool
	)
	br := bufio.NewReaderSize(pr, 64<<10)
scan:
	for {
		line, long, rerr := readLine(br)
		if len(line) > 0 || long {
			scanned++
			if rec, perr := parseLine(line, long); perr == nil {
				switch req.q.Window(rec) {
				case models.PastWindow:
					finished, past = true, true
					break scan
				case models.InWindow:
					if req.text == "" || strings.Contains(strings.ToLower(rec.Message), req.text) {
						e := toLogEntry(req.q, rec)
						b, _ := json.Marshal(e)
						if size+len(b)+1 > logsMaxBytes {
							res.Truncated = true
							break scan
						}
						size += len(b) + 1
						res.Entries = append(res.Entries, e)
					}
				}
				last = rec.Cursor
			}
			if len(res.Entries) >= req.lines {
				finished = true
				break
			}
			if scanned >= logsScanEntries {
				break
			}
		}
		if rerr != nil {
			finished = ctx.Err() == nil && (errors.Is(rerr, io.EOF) || errors.Is(rerr, os.ErrClosed))
			break
		}
	}

	timedOut := ctx.Err() != nil
	stopped := !timedOut && (past || len(res.Entries) >= req.lines || res.Truncated || scanned >= logsScanEntries)
	// A journalctl that reached its end is reaped before the cancel, so its
	// own exit status is what Wait reports.
	if stopped {
		cancel()
	}
	werr := cmd.Wait()
	cancel()
	_ = pr.Close()

	// An exit status only means a failure when journalctl ended on its own.
	if werr != nil && !timedOut && !stopped {
		logging.Errorf("journalctl: %v: %s", werr, strings.TrimSpace(stderr.String()))
		return res, errors.New("journalctl failed")
	}

	res.Complete = finished && !timedOut || res.Truncated
	if res.Truncated && len(res.Entries) > 0 {
		last = res.Entries[len(res.Entries)-1].Cursor
	}
	if last != "" {
		res.NextCursor = &last
	}
	if !req.q.Forward {
		for i, j := 0, len(res.Entries)-1; i < j; i, j = i+1, j-1 {
			res.Entries[i], res.Entries[j] = res.Entries[j], res.Entries[i]
		}
	}
	return res, nil
}
