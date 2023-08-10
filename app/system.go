package app

import (
	"github.com/calaos/calaos-container/models"
	"github.com/gofiber/fiber/v2"
)

func (a *AppServer) apiSystemReboot(c *fiber.Ctx) (err error) {

	_, err = models.RunCommand("systemctl", "reboot")

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

func (a *AppServer) apiSystemRestart(c *fiber.Ctx) (err error) {

	_, err = models.RunCommand("systemctl", "restart", "calaos-home")

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

func (a *AppServer) apiSystemFsStatus(c *fiber.Ctx) (err error) {

	out, err := models.RunCommand("findmnt", "--json", "/")

	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error":  false,
		"msg":    "ok",
		"output": out,
	})
}

func (a *AppServer) apiSystemRollbackSnapshot(c *fiber.Ctx) (err error) {

	_, err = models.RunCommand("calaos_rollback.sh")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return a.apiSystemReboot(c)
}

func (a *AppServer) apiSystemInstallListDevices(c *fiber.Ctx) (err error) {

	out, err := models.RunCommand("lsblk", "--bytes", "--json", "--paths", "--output-all")

	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error":  false,
		"msg":    "ok",
		"output": out,
	})
}

type InstallOpts struct {
	Device string `json:"device"`
}

func (a *AppServer) apiSystemInstallStart(c *fiber.Ctx) (err error) {

	n := new(InstallOpts)
	if err := c.BodyParser(n); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	if n.Device == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": true,
			"msg":   "device arg not set",
		})
	}

	r, err := models.RunCommandReader("calaos_install.sh", n.Device)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.SendStream(r)
}

func (a *AppServer) apiSystemLastInstallStatus(c *fiber.Ctx) (err error) {

}
