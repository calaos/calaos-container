package app

import (
	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/models/structs"

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

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error":  false,
		"msg":    "ok",
		"output": nets,
	})
}

func (a *AppServer) apiNetIntfConfigure(c *fiber.Ctx) (err error) {
	intf := c.Params("intf")
	var net structs.NetInterface
	if err := c.BodyParser(&net); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	err = models.ConfigureNetInterface(intf, net)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error": false,
		"msg":   "ok",
	})
}
