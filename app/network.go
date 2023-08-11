package app

import (
	"github.com/calaos/calaos-container/models"
	"github.com/gofiber/fiber/v2"
)

func (a *AppServer) apiNetIntfList(c *fiber.Ctx) (err error) {

	nets, err := models.GetAllNetInterfaces()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(nets)
}
