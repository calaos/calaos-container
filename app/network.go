package app

import (
	"errors"
	"fmt"

	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/models/structs"

	"github.com/gofiber/fiber/v2"
)

func (a *AppServer) apiNetIntfList(c *fiber.Ctx) (err error) {
	nets, err := getNetInterfaces()
	if err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
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
	// /etc/systemd/network/ and passed to networkd: check its syntax before
	// doing anything else.
	if err := models.ValidateInterfaceName(intf); err != nil {
		return legacyError(c, fiber.StatusBadRequest, err)
	}

	var netConf structs.NetInterface
	if err := c.BodyParser(&netConf); err != nil {
		return legacyError(c, fiber.StatusBadRequest, err)
	}

	// Every value that ends up in the generated unit file is validated before
	// the file is created.
	if err := models.ValidateNetInterfaceConfig(netConf); err != nil {
		return legacyError(c, fiber.StatusBadRequest, err)
	}

	// A syntactically valid name is not enough: only interfaces that really
	// exist can be configured.
	exists, err := interfaceExists(intf)
	if err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}
	if !exists {
		return legacyError(c, fiber.StatusBadRequest, fmt.Errorf("invalid interface: %q does not exist", intf))
	}

	if err = configureNetInterface(intf, netConf); err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}

	return legacyOK(c)
}

func (a *AppServer) v2NetList(c *fiber.Ctx) error {
	nets, err := getNetInterfaces()
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, nets)
}

// validationMsg passes through the text of models.ValidationError, which only
// describes the client's own input; anything else stays generic.
func validationMsg(err error) string {
	var ve *models.ValidationError
	if errors.As(err, &ve) {
		return ve.Error()
	}
	return "invalid network configuration"
}

func (a *AppServer) v2NetConfigure(c *fiber.Ctx) error {
	intf := c.Params("intf")

	if err := models.ValidateInterfaceName(intf); err != nil {
		return errInvalid(validationMsg(err))
	}

	var netConf structs.NetInterface
	if err := c.BodyParser(&netConf); err != nil {
		return errInvalid("invalid JSON body")
	}

	if err := models.ValidateNetInterfaceConfig(netConf); err != nil {
		return errInvalid(validationMsg(err))
	}

	exists, err := interfaceExists(intf)
	if err != nil {
		return errInternal(err)
	}
	if !exists {
		return errInvalid(fmt.Sprintf("invalid interface: %q does not exist", intf))
	}

	if err := configureNetInterface(intf, netConf); err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, nil)
}
