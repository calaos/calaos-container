package app

import (
	"errors"

	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/models/structs"
	"github.com/gofiber/fiber/v2"
)

// legacyError is the error body of the unprefixed routes. It reflects the
// error text as-is: those bodies are frozen (golden tests).
func legacyError(c *fiber.Ctx, status int, err error) error {
	return c.Status(status).JSON(fiber.Map{
		"error": true,
		"msg":   err.Error(),
	})
}

// upgradeRunning reports an apt run in progress, during which a reboot would
// cut dpkg.
func upgradeRunning() bool {
	st, err := updateStatus()
	return err == nil && st != nil && st.Status == "upgrading"
}

func (a *AppServer) apiUpdateCheck(c *fiber.Ctx) (err error) {
	if err = checkUpdates(); err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}

	return c.Status(fiber.StatusOK).JSON(getNewVersions())
}

func (a *AppServer) apiUpdateAvail(c *fiber.Ctx) (err error) {
	return c.Status(fiber.StatusOK).JSON(getNewVersions())
}

func (a *AppServer) apiUpdateInstalled(c *fiber.Ctx) (err error) {
	m, err := getInstalledPackages()
	if err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}

	return c.Status(fiber.StatusOK).JSON(m)
}

func (a *AppServer) apiUpdateUpgradeAll(c *fiber.Ctx) (err error) {
	if err = upgradeAll(); err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"msg": "Upgrade done",
	})
}

func (a *AppServer) apiUpdateUpgrade(c *fiber.Ctx) (err error) {
	if err = upgradePackage(c.Query("package")); err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"msg": "Upgrade done",
	})
}

func (a *AppServer) apiUpdateStatus(c *fiber.Ctx) (err error) {
	s, err := updateStatus()
	if err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}

	return c.Status(fiber.StatusOK).JSON(s)
}

func (a *AppServer) v2UpdateCheck(c *fiber.Ctx) error {
	if err := checkUpdates(); err != nil {
		if errors.Is(err, models.ErrUpgradeInProgress) {
			return errUpgradeInProgress()
		}
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, getNewVersions())
}

func (a *AppServer) v2UpdateAvailable(c *fiber.Ctx) error {
	return sendOK(c, fiber.StatusOK, getNewVersions())
}

func (a *AppServer) v2UpdateInstalled(c *fiber.Ctx) error {
	m, err := getInstalledPackages()
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, m)
}

// v2UpdateStatusData is the payload of GET /api/v2/update/status. Job stays
// null until upgrades run as jobs.
type v2UpdateStatusData struct {
	Progress *structs.Status `json:"progress"`
	Job      any             `json:"job"`
}

func (a *AppServer) v2UpdateStatus(c *fiber.Ctx) error {
	st, err := updateStatus()
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, v2UpdateStatusData{Progress: st})
}
