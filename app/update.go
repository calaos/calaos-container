package app

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

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

func (a *AppServer) apiUpdateUpgradeAll(c *fiber.Ctx) error {
	return a.legacyUpgrade(c, models.JobKindUpgradeAll, "")
}

// apiUpdateUpgrade refuses a name outside the package rule with the body of
// an unknown package: no such name has a pending update anyway.
func (a *AppServer) apiUpdateUpgrade(c *fiber.Ctx) error {
	pkg := c.Query("package")
	if !validPackageName(pkg) {
		return legacyError(c, fiber.StatusInternalServerError, models.ErrPackageNotFound)
	}
	return a.legacyUpgrade(c, models.JobKindUpgrade, pkg)
}

// legacyUpgrade answers once apt has ended: old kiosks take any 2xx for a
// finished upgrade. The job completes before the answer, so a kiosk posting
// the next package of its queue right away never finds the lock held; a
// self-upgrade exit still waits for the answer (graceful server shutdown).
func (a *AppServer) legacyUpgrade(c *fiber.Ctx, kind, pkg string) error {
	run, err := startUpgradeJob(kind, pkg)
	if err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}
	err = run.Run()
	if run.Job().DaemonRestart {
		// A pipelined answer is only flushed with the next one, which this
		// daemon will not serve.
		c.Context().SetConnectionClose()
	}
	run.Complete()
	if err != nil {
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
			return errUpgradeInProgress(err)
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

type v2UpdateStatusData struct {
	Progress *structs.Status `json:"progress"`
	Job      *models.Job     `json:"job"`
}

func (a *AppServer) v2UpdateStatus(c *fiber.Ctx) error {
	st, err := updateStatus()
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, v2UpdateStatusData{Progress: st, Job: currentJob()})
}

const maxPackageNameLen = 128

var packageNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+-]*$`)

// validPackageName is the rule calaos_server applies too (docs/API.md).
func validPackageName(p string) bool {
	return len(p) <= maxPackageNameLen && packageNameRe.MatchString(p) && !strings.Contains(p, "..")
}

func (a *AppServer) v2UpdateUpgradeAll(c *fiber.Ctx) error {
	return startV2Job(c, models.JobKindUpgradeAll, "")
}

func (a *AppServer) v2UpdateUpgrade(c *fiber.Ctx) error {
	var body struct {
		Package *string `json:"package"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return errInvalid("invalid JSON body")
	}
	if body.Package == nil {
		return errInvalid("missing package")
	}
	if !validPackageName(*body.Package) {
		return errInvalid("invalid package name")
	}
	return startV2Job(c, models.JobKindUpgrade, *body.Package)
}

func startV2Job(c *fiber.Ctx, kind, pkg string) error {
	run, err := startUpgradeJob(kind, pkg)
	if err != nil {
		switch {
		case errors.Is(err, models.ErrUpgradeInProgress):
			return errUpgradeInProgress(err)
		case errors.Is(err, models.ErrPackageNotFound):
			return newAPIError(fiber.StatusNotFound, codePackageNotFound, "no pending update for this package")
		}
		return errInternal(err)
	}
	job := run.Job()
	go func() {
		_ = run.Run() // the job records the outcome
		run.Complete()
	}()
	return c.Status(fiber.StatusAccepted).JSON(okEnvelope{Msg: "accepted", Data: job})
}
