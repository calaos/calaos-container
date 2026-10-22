package app

import (
	"crypto/subtle"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
)

const (
	defaultTokenFile = "/run/calaos/calaos-ct.token"
)

var (
	// tokenStr holds the cached, trimmed contents of the token file. It is
	// only meaningful when tokenValid is true: an empty/invalid token must
	// never be used to authenticate a request. Guarded by tokenMu.
	tokenStr string

	// tokenFile is the path to the file holding the API token. It is a
	// package variable so tests can point it at a temporary file.
	tokenFile = defaultTokenFile

	// tokenMu guards all of the cache state below, which is read and
	// lazily refreshed by concurrent Fiber handlers.
	tokenMu sync.RWMutex

	// tokenValid is true only if tokenStr currently holds a non-empty
	// token successfully loaded from tokenLoadPath. When false, every
	// request must be rejected (fail closed).
	tokenValid bool

	// tokenLoadPath/tokenModTime/tokenSize identify the file state the
	// cache was last loaded from, so a per-request stat() can detect
	// rotation (or a change of tokenFile in tests) and trigger a reload.
	tokenLoadPath string
	tokenModTime  time.Time
	tokenSize     int64
)

func NewTokenMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		var token string

		//get token
		headerValue := c.Get("authorization")
		if headerValue != "" {
			split := strings.SplitN(headerValue, " ", 2)

			if len(split) == 2 && strings.ToLower(split[0]) == "bearer" {
				token = split[1]
			}
		}

		// Published kiosks open the legacy /api/events with ?token= since
		// they cannot set a header there. /api/v2 never had a query token.
		v2 := isV2Path(c.Path())
		if token == "" && !v2 {
			token = c.Query("token")
		}
		c.Locals("token", token)

		expected, ok := currentToken()
		if !ok {
			// Fail closed: no valid token is available (file missing,
			// unreadable, or empty after trimming). Reject every request
			// rather than leaking OS error details to the client.
			if v2 {
				return sendAPIError(c, errBadBearer())
			}
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": true,
				"msg":   "unauthorized",
			})
		}

		//Check token validity (constant-time to avoid a timing side-channel)
		if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			if v2 {
				return sendAPIError(c, errBadBearer())
			}
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": true,
				"msg":   "wrong token",
			})
		}

		return c.Next()
	}
}

// currentToken returns the API token currently expected of clients, reading
// and caching it from tokenFile. The cache is invalidated and reloaded
// whenever the file's mtime or size changes (or the file identified by
// tokenFile changes), so rotating the token on disk is picked up without a
// daemon restart -- a stat() per request is cheap given this API's traffic.
//
// The second return value is false whenever no valid, non-empty token is
// currently available (file missing, unreadable, or empty/whitespace after
// trimming); callers must then reject every request. This is what keeps an
// empty token file from ever authenticating a request with no Authorization
// header.
func currentToken() (string, bool) {
	path := tokenFile

	info, err := os.Stat(path)
	if err != nil {
		logging.Warnf("token file %s unavailable: %s", path, err)
		tokenMu.Lock()
		tokenValid = false
		tokenMu.Unlock()
		return "", false
	}

	tokenMu.RLock()
	fresh := tokenValid && tokenLoadPath == path && tokenModTime.Equal(info.ModTime()) && tokenSize == info.Size()
	cached := tokenStr
	tokenMu.RUnlock()

	if fresh {
		return cached, true
	}

	content, err := os.ReadFile(path)
	if err != nil {
		logging.Warnf("error reading token file %s: %s", path, err)
		tokenMu.Lock()
		tokenValid = false
		tokenMu.Unlock()
		return "", false
	}
	trimmed := strings.TrimSpace(string(content))

	tokenMu.Lock()
	defer tokenMu.Unlock()
	tokenLoadPath = path
	tokenModTime = info.ModTime()
	tokenSize = info.Size()

	if trimmed == "" {
		logging.Warnf("token file %s is empty; rejecting all requests until a token is provisioned", path)
		tokenStr = ""
		tokenValid = false
		return "", false
	}

	tokenStr = trimmed
	tokenValid = true
	return tokenStr, true
}

func errBadBearer() *apiError {
	return newAPIError(fiber.StatusUnauthorized, codeBadBearer, "unauthorized")
}
