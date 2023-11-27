package app

import (
	"github.com/calaos/calaos-container/config"
	"github.com/calaos/calaos-container/models"
	"github.com/gofiber/fiber/v2"
)

func (a *AppServer) apiUpdateCheck(c *fiber.Ctx) (err error) {

	err = models.CheckUpdates()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(models.NewVersions)
}

func (a *AppServer) apiUpdateAvail(c *fiber.Ctx) (err error) {
	return c.Status(fiber.StatusOK).JSON(models.NewVersions)
}

func (a *AppServer) apiUpdateImages(c *fiber.Ctx) (err error) {

	m, err := models.LoadFromDisk(config.Config.String("general.version_file"))
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(m)
}

func (a *AppServer) apiUpdateUpgradeAll(c *fiber.Ctx) (err error) {

	err = models.UpgradeAll()
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

	err = models.Upgrade(c.Query("package"))
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
	s, err := models.UpdateStatus()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(s)
}
