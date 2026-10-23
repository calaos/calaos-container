package app

import (
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/utils"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/events"
)

const (
	headerClientIP = "X-Calaos-Client-IP"

	localErrCode       = "calaos.err_code"
	localIssuedSession = "calaos.issued_session"
)

// Overridden by tests so they never touch the box directories.
var (
	adminDataDir    = admin.DefaultDataDir
	adminRuntimeDir = admin.DefaultRuntimeDir
	adminHashParams admin.HashParams

	// publishAdmin lets tests lose a live alert, as a full subscriber would.
	publishAdmin = func(h *events.Hub, evType string, data any) { h.Publish(evType, data) }
)

func (a *AppServer) registerAdmin(v2 fiber.Router) {
	s := a.sessionAllowed

	v2.Get("/admin/status", s, a.v2AdminStatus)
	v2.Post("/admin/verify", s, a.v2AdminVerify)
	// admin/set checks the body before the session (docs/API.md).
	v2.Post("/admin/set", a.v2AdminSet)
	v2.Post("/admin/reset", bearerOnly, a.v2AdminReset)
	v2.Post("/admin/alerts/ack", bearerOnly, a.v2AdminAlertsAck)
}

func adminError(err error) *apiError {
	var rl *admin.RateLimitedError
	switch {
	case errors.As(err, &rl):
		e := newAPIError(fiber.StatusTooManyRequests, codeRateLimited, "too many admin password attempts")
		e.data = fiber.Map{"retry_after": rl.RetryAfter}
		e.retryAfter = rl.RetryAfter
		return e
	case errors.Is(err, admin.ErrNotSet):
		return newAPIError(fiber.StatusConflict, codeAdminNotSet, "the admin password is not set")
	case errors.Is(err, admin.ErrAlreadySet):
		return newAPIError(fiber.StatusConflict, codeAdminAlreadySet, "the admin password is already set")
	case errors.Is(err, admin.ErrBadPassword):
		return newAPIError(fiber.StatusForbidden, codeBadPassword, "wrong admin password")
	case errors.Is(err, admin.ErrBadSession):
		return errBadSession()
	case errors.Is(err, admin.ErrTooShort):
		e := newAPIError(fiber.StatusBadRequest, codePasswordTooShort, "the admin password is too short")
		e.data = fiber.Map{"min_length": admin.MinPasswordLength}
		return e
	case errors.Is(err, admin.ErrTooLong):
		return errPasswordTooLong()
	}
	return errInternal(err)
}

func errPasswordTooLong() *apiError {
	return errInvalid("invalid password: at most " + strconv.Itoa(admin.MaxPasswordBytes) + " bytes")
}

type passwordBody struct {
	Password *string `json:"password"`
	Recovery *bool   `json:"recovery"`
}

func parsePasswordBody(c *fiber.Ctx) (passwordBody, error) {
	var b passwordBody
	if err := json.Unmarshal(c.Body(), &b); err != nil {
		return b, errInvalid("invalid JSON body")
	}
	if b.Password == nil {
		return b, errInvalid("missing password")
	}
	if len(*b.Password) > admin.MaxPasswordBytes {
		return b, errPasswordTooLong()
	}
	if !utf8.ValidString(*b.Password) {
		return b, errInvalid("invalid password: not UTF-8")
	}
	return b, nil
}

func (a *AppServer) v2AdminStatus(c *fiber.Ctx) error {
	set, err := a.admin.Status()
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, struct {
		AdminSet bool `json:"admin_set"`
	}{set})
}

func (a *AppServer) v2AdminVerify(c *fiber.Ctx) error {
	b, err := parsePasswordBody(c)
	if err != nil {
		return err
	}
	session, expiresIn, err := a.admin.Verify(*b.Password)
	if err != nil {
		return adminError(err)
	}
	c.Locals(localIssuedSession, admin.SessionAuditID(session))
	return sendOK(c, fiber.StatusOK, struct {
		Session   string `json:"session"`
		ExpiresIn int    `json:"expires_in"`
	}{session, expiresIn})
}

func (a *AppServer) v2AdminSet(c *fiber.Ctx) error {
	b, err := parsePasswordBody(c)
	if err != nil {
		return err
	}

	session, present := adminSession(c)
	mode := admin.ModeBootstrap
	switch {
	case b.Recovery != nil && *b.Recovery:
		if present {
			return errNotAllowed()
		}
		mode = admin.ModeRecovery
	case present:
		mode = admin.ModeChange
	}

	if err := a.admin.Set(mode, *b.Password, session); err != nil {
		return adminError(err)
	}
	return sendOK(c, fiber.StatusOK, struct {
		Mode admin.Mode `json:"mode"`
	}{mode})
}

func (a *AppServer) v2AdminReset(c *fiber.Ctx) error {
	wasSet, err := a.admin.Reset()
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, struct {
		WasSet bool `json:"was_set"`
	}{wasSet})
}

func (a *AppServer) v2AdminAlertsAck(c *fiber.Ctx) error {
	var b struct {
		IDs *[]string `json:"ids"`
	}
	if err := json.Unmarshal(c.Body(), &b); err != nil {
		return errInvalid("invalid JSON body")
	}
	if b.IDs == nil {
		return errInvalid("missing ids")
	}
	n, err := a.admin.Ack(*b.IDs)
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, struct {
		Acked int `json:"acked"`
	}{n})
}

// auditClient never trusts X-Calaos-Client-IP: it is only normalised for the
// log.
func auditClient(c *fiber.Ctx) string {
	v := c.Request().Header.Peek(headerClientIP)
	if v == nil {
		return "local"
	}
	ip := net.ParseIP(strings.TrimSpace(string(v)))
	if ip == nil {
		return "invalid"
	}
	return ip.String()
}

func isAdminPath(p string) bool {
	return strings.HasPrefix(strings.ToLower(p)+"/", apiV2Prefix+"/admin/")
}

func isAdminStatusPath(p string) bool {
	return strings.TrimRight(strings.ToLower(p), "/") == apiV2Prefix+"/admin/status"
}

// auditMiddleware runs before the bearer check, so refused calls reach the
// daemon log too. calaos_server polls admin/status without a session: not
// audited.
func (a *AppServer) auditMiddleware(c *fiber.Ctx) error {
	path := utils.CopyString(c.Path())
	session, hasSession := adminSession(c)
	if !isV2Path(path) || (!hasSession && (!isAdminPath(path) || isAdminStatusPath(path))) {
		return c.Next()
	}

	err := c.Next()

	e := admin.AuditEntry{
		Client: auditClient(c),
		Method: c.Method(),
		Path:   path,
		Result: auditResult(c, err),
	}
	if hasSession {
		e.Session = admin.SessionAuditID(session)
	} else if id, ok := c.Locals(localIssuedSession).(string); ok {
		e.Session = id
	}
	// Anything on the host can send these without the bearer: in audit.log
	// they would rotate the real entries away.
	if e.Result == codeBadBearer {
		a.admin.AuditRefused(e)
	} else {
		a.admin.Audit(e)
	}
	return err
}

// auditResult is "ok" or the code the client gets. A returned error is not
// rendered yet: errorHandler maps it the same way.
func auditResult(c *fiber.Ctx, err error) string {
	if err != nil {
		if e := v2Error(err); e != nil {
			return e.code
		}
		var fe *fiber.Error
		if errors.As(err, &fe) {
			return "http_" + strconv.Itoa(fe.Code)
		}
		return codeInternal
	}
	if code, ok := c.Locals(localErrCode).(string); ok {
		return code
	}
	if st := c.Response().StatusCode(); st >= 400 {
		return "http_" + strconv.Itoa(st)
	}
	return "ok"
}
