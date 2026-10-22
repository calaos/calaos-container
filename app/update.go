package app

import (
	"github.com/gofiber/fiber/v2"
)

func (a *AppServer) apiUpdateCheck(c *fiber.Ctx) (err error) {

	err = checkUpdates()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(getNewVersions())
}

func (a *AppServer) apiUpdateAvail(c *fiber.Ctx) (err error) {
	return c.Status(fiber.StatusOK).JSON(getNewVersions())
}

func (a *AppServer) apiUpdateInstalled(c *fiber.Ctx) (err error) {
	m, err := getInstalledPackages()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(m)
}

func (a *AppServer) apiUpdateUpgradeAll(c *fiber.Ctx) (err error) {

	err = upgradeAll()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"msg": "Upgrade done",
	})
}

func (a *AppServer) apiUpdateUpgrade(c *fiber.Ctx) (err error) {

	err = upgradePackage(c.Query("package"))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"msg": "Upgrade done",
	})
}

func (a *AppServer) apiUpdateStatus(c *fiber.Ctx) (err error) {
	s, err := updateStatus()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(s)
}
