package app

import (
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
)

const (
	defaultTokenFile = "/run/calaos/calaos-ct.token"
)

var (
	tokenStr string

	// tokenFile is the path to the file holding the API token. It is a
	// package variable so tests can point it at a temporary file.
	tokenFile = defaultTokenFile
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

		// Browser WebSocket clients cannot set custom headers, so allow the
		// token to be passed as a query parameter when the Authorization
		// header is absent.
		if token == "" {
			token = c.Query("token")
		}
		c.Locals("token", token)

		if tokenStr == "" {
			content, err := os.ReadFile(tokenFile)
			if err != nil {
				logging.Errorf("Error reading file %s: %s", tokenFile, err)
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
					"error": true,
					"msg":   err.Error(),
				})
			}
			tokenStr = strings.TrimSpace(string(content))
		}

		//Check token validity
		if token != tokenStr {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": true,
				"msg":   "wrong token",
			})
		}

		return c.Next()
	}
}
