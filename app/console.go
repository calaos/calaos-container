package app

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v2"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/app/console"
)

func consoleError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, console.ErrLimit):
		e := newAPIError(fiber.StatusConflict, codeConsoleLimit, "too many open consoles")
		e.data = fiber.Map{"max": console.MaxConsoles}
		return e
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

// takeGrant lets tests end the session while its console starts.
var takeGrant = (*admin.Manager).TakeGrant

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
		return takeGrant(a.admin, s, *b.CodeID, admin.ActionConsoleOpen)
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
