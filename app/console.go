package app

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/app/console"
)

// Overridden by tests with a fake ttyd.
var consoleTTYD = console.DefaultTTYD

func isConsoleActivityPath(p string) bool {
	p = strings.TrimRight(strings.ToLower(p), "/")
	return strings.HasPrefix(p, apiV2Prefix+"/admin/console/") && strings.HasSuffix(p, "/activity")
}

func consoleError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, console.ErrLimit):
		e := newAPIError(fiber.StatusConflict, codeConsoleLimit, "too many open consoles")
		e.data = fiber.Map{"max": console.MaxConsoles}
		return e
	case errors.Is(err, console.ErrUnavailable):
		return newAPIError(fiber.StatusConflict, codeConsoleUnavail, "the console is not installed on this box")
	case errors.Is(err, console.ErrEnded):
		return errBadSession()
	case errors.Is(err, console.ErrNotFound):
		return adminError(admin.ErrNotFound)
	}
	return codeError(c, err)
}

type consoleOpened struct {
	ConsoleID     string `json:"console_id"`
	Socket        string `json:"socket"`
	WSPath        string `json:"ws_path"`
	WSProtocol    string `json:"ws_protocol"`
	ConnectWithin int    `json:"connect_within"`
	ExpiresIn     int    `json:"expires_in"`
	IdleTimeout   int    `json:"idle_timeout"`
}

// v2ConsoleOpen takes the console_open grant of this very session: the grant
// is bound to the session that verified the code.
func (a *AppServer) v2ConsoleOpen(c *fiber.Ctx) error {
	var b struct {
		CodeID *string `json:"code_id"`
		Term   *string `json:"term"`
	}
	if err := decodeBody(c, &b); err != nil {
		return err
	}
	if b.CodeID == nil || !admin.ValidID(*b.CodeID) {
		return errInvalid("invalid code_id")
	}
	term := console.Terms[0]
	if b.Term != nil {
		if !console.ValidTerm(*b.Term) {
			return errInvalid("invalid term")
		}
		term = *b.Term
	}
	auditField(c, "action", admin.ActionConsoleOpen)
	auditField(c, "code_id", *b.CodeID)

	s, _ := adminSession(c)
	info, err := a.consoles.Open(console.Request{
		SessionHash: admin.SessionHash(s),
		AuditID:     admin.SessionAuditID(s),
		Client:      auditClient(c),
		Term:        term,
	}, func() error {
		return a.admin.TakeGrant(s, *b.CodeID, admin.ActionConsoleOpen)
	})
	if err != nil {
		return consoleError(c, err)
	}
	auditField(c, "console", info.ID)
	a.admin.RaiseConsoleOpened(info.AuditID)

	return sendOK(c, fiber.StatusOK, consoleOpened{
		ConsoleID:     info.ID,
		Socket:        info.Socket,
		WSPath:        console.WSPath,
		WSProtocol:    console.WSProtocol,
		ConnectWithin: int(console.ConnectTimeout.Seconds()),
		ExpiresIn:     int(console.MaxLifetime.Seconds()),
		IdleTimeout:   int(console.IdleTimeout.Seconds()),
	})
}

// v2ConsoleActivity is how calaos_server, which carries the terminal
// traffic, tells the daemon the tunnel is up, used, or gone.
func (a *AppServer) v2ConsoleActivity(c *fiber.Ctx) error {
	id := c.Params("id")
	if !console.ValidID(id) {
		return adminError(admin.ErrNotFound)
	}
	var b struct {
		Event *string `json:"event"`
	}
	if err := decodeBody(c, &b); err != nil {
		return err
	}
	if b.Event == nil {
		return errInvalid("missing event")
	}
	switch *b.Event {
	case console.EventConnected, console.EventInput, console.EventClosed:
	default:
		return errInvalid("invalid event")
	}
	auditField(c, "console", id)
	auditField(c, "event", *b.Event)
	st, err := a.consoles.Activity(id, *b.Event)
	if err != nil {
		return consoleError(c, err)
	}
	return sendOK(c, fiber.StatusOK, st)
}

// consoleClosed writes the close line; the open line is the audit of
// POST admin/console.
func (a *AppServer) consoleClosed(info console.Info, reason string) {
	dur := 0
	if !info.Opened.IsZero() {
		dur = int(a.now().Sub(info.Opened).Seconds())
	}
	a.admin.Audit(admin.AuditEntry{
		Client:  info.Client,
		Method:  "-",
		Path:    "-",
		Result:  "ok",
		Session: info.AuditID,
		Fields: [][2]string{
			{"action", "console_close"},
			{"console", info.ID},
			{"reason", reason},
			{"duration", strconv.Itoa(dur)},
		},
	})
}
