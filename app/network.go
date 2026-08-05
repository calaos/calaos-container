package app

import (
	"fmt"

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

	// The interface name is interpolated into a path under
	// /etc/systemd/network/ and passed to networkctl: check its syntax before
	// doing anything else.
	if err := models.ValidateInterfaceName(intf); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	var netConf structs.NetInterface
	if err := c.BodyParser(&netConf); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	// Every value that ends up in the generated unit file is validated before
	// the file is created.
	if err := models.ValidateNetInterfaceConfig(netConf); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	// A syntactically valid name is not enough: only interfaces that really
	// exist can be configured.
	exists, err := models.InterfaceExists(intf)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}
	if !exists {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": true,
			"msg":   fmt.Sprintf("invalid interface: %q does not exist", intf),
		})
	}

	err = models.ConfigureNetInterface(intf, netConf)
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
