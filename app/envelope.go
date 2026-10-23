package app

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/sirupsen/logrus"
)

const apiV2Prefix = "/api/v2"

// Error codes of the /api/v2 envelope (docs/API.md, error vocabulary).
const (
	codeBadBearer         = "bad_bearer"
	codeBadSession        = "bad_session"
	codeBadPassword       = "bad_password"
	codeNotAllowed        = "not_allowed"
	codePasswordTooShort  = "password_too_short"
	codeInvalidRequest    = "invalid_request"
	codeNotFound          = "not_found"
	codePackageNotFound   = "package_not_found"
	codeMethodNotAllowed  = "method_not_allowed"
	codeAdminNotSet       = "admin_not_set"
	codeAdminAlreadySet   = "admin_already_set"
	codeUpgradeInProgress = "upgrade_in_progress"
	codeUpgradeRequired   = "upgrade_required"
	codeRateLimited       = "rate_limited"
	codeInternal          = "internal"
)

type okEnvelope struct {
	Error bool   `json:"error"`
	Msg   string `json:"msg"`
	Data  any    `json:"data"`
}

type errEnvelope struct {
	Error bool   `json:"error"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
	Data  any    `json:"data,omitempty"`
}

// apiError is a /api/v2 error. msg and data go to the client; cause is only
// logged, so internal error text never leaves the daemon.
type apiError struct {
	status     int
	code       string
	msg        string
	data       any
	cause      error
	retryAfter int // seconds, sent as Retry-After when set
}

func (e *apiError) Error() string {
	if e.cause != nil {
		return e.code + ": " + e.msg + ": " + e.cause.Error()
	}
	return e.code + ": " + e.msg
}

func newAPIError(status int, code, msg string) *apiError {
	return &apiError{status: status, code: code, msg: msg}
}

func errInternal(cause error) *apiError {
	return &apiError{status: fiber.StatusInternalServerError, code: codeInternal, msg: "internal error", cause: cause}
}

func errInvalid(msg string) *apiError {
	return newAPIError(fiber.StatusBadRequest, codeInvalidRequest, msg)
}

// errUpgradeInProgress carries the running job, if the lock holder is one.
func errUpgradeInProgress() *apiError {
	e := newAPIError(fiber.StatusConflict, codeUpgradeInProgress, "an upgrade is already in progress")
	if id := runningJobID(); id != "" {
		e.data = fiber.Map{"job_id": id}
	}
	return e
}

func sendOK(c *fiber.Ctx, status int, data any) error {
	return c.Status(status).JSON(okEnvelope{Msg: "ok", Data: data})
}

func sendAPIError(c *fiber.Ctx, e *apiError) error {
	c.Locals(localErrCode, e.code)
	if e.retryAfter > 0 {
		c.Set(fiber.HeaderRetryAfter, strconv.Itoa(e.retryAfter))
	}
	if e.cause != nil {
		logging.WithFields(logrus.Fields{
			"method": c.Method(),
			"path":   c.Path(),
			"code":   e.code,
		}).Errorf("request failed: %v", e.cause)
	}
	return c.Status(e.status).JSON(errEnvelope{Error: true, Code: e.code, Msg: e.msg, Data: e.data})
}

// isV2Path matches the way fiber routes: case-insensitive, trailing slash
// optional.
func isV2Path(p string) bool {
	return strings.HasPrefix(strings.ToLower(p)+"/", apiV2Prefix+"/")
}

// errorHandler envelopes errors on /api/v2 only. Legacy paths keep fiber's
// default plain-text answers, which the golden tests pin.
func errorHandler(c *fiber.Ctx, err error) error {
	if !isV2Path(c.Path()) {
		return fiber.DefaultErrorHandler(c, err)
	}

	if e := v2Error(err); e != nil {
		return sendAPIError(c, e)
	}
	return fiber.DefaultErrorHandler(c, err)
}

// v2Error is the /api/v2 error a handler error turns into, or nil when the
// server's own answer is kept: transport-level errors (413, 431, timeouts).
func v2Error(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}

	var fe *fiber.Error
	if errors.As(err, &fe) {
		switch fe.Code {
		case fiber.StatusNotFound:
			return newAPIError(fe.Code, codeNotFound, "not found")
		case fiber.StatusMethodNotAllowed:
			return newAPIError(fe.Code, codeMethodNotAllowed, "method not allowed")
		case fiber.StatusUpgradeRequired:
			return newAPIError(fe.Code, codeUpgradeRequired, "websocket upgrade required")
		}
		return nil
	}

	return errInternal(err)
}
