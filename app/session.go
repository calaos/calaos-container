package app

import "github.com/gofiber/fiber/v2"

const headerAdminSession = "X-Calaos-Admin-Session"

// sessionValidator decides whether an admin session is live.
type sessionValidator interface {
	Valid(session string) bool
}

// adminSession reports the X-Calaos-Admin-Session header. A header sent with
// an empty value still counts as present, so it can never fall back to the
// bearer-only path.
func adminSession(c *fiber.Ctx) (string, bool) {
	v := c.Request().Header.Peek(headerAdminSession)
	if v == nil {
		return "", false
	}
	return string(v), true
}

// sessionAllowed guards the routes calaos_server may relay: a session, when
// sent, must be live. Requests without the header are authorized by the
// bearer alone (CLI path).
func (a *AppServer) sessionAllowed(c *fiber.Ctx) error {
	s, present := adminSession(c)
	if !present {
		return c.Next()
	}
	if !a.sessions.Valid(s) {
		return errBadSession()
	}
	return c.Next()
}

// bearerOnly refuses any session-bearing request without looking the session
// up: calaos_server never relays these routes.
func bearerOnly(c *fiber.Ctx) error {
	if _, present := adminSession(c); present {
		return errNotAllowed()
	}
	return c.Next()
}

func errBadSession() *apiError {
	return newAPIError(fiber.StatusUnauthorized, codeBadSession, "admin session expired or invalid")
}

func errNotAllowed() *apiError {
	return newAPIError(fiber.StatusForbidden, codeNotAllowed, "not allowed with an admin session")
}
