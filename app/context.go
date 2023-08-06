package app

import (
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
)

const (
	TOKEN_FILE = "/run/calaos/calaos-ct.token"
)

var (
	tokenStr string
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
		c.Locals("token", token)

		if tokenStr == "" {
			content, err := os.ReadFile(TOKEN_FILE)
			if err != nil {
				logging.Errorf("Error reading file %s: %s", TOKEN_FILE, err)
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
