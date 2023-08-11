package app

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

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

	var parsedData map[string]interface{}
	if err = json.Unmarshal([]byte(out), &parsedData); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error":  false,
		"msg":    "ok",
		"output": parsedData,
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

	var parsedData map[string]interface{}
	if err = json.Unmarshal([]byte(out), &parsedData); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error":  false,
		"msg":    "ok",
		"output": parsedData,
	})
}

type InstallOpts struct {
	Device string `json:"device"`
}

const (
	INSTALL_LOG_FILE       = "/run/calaos/calaos_install.log"
	INSTALL_EXIT_CODE_FILE = "/run/calaos/calaos_install.code"
)

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

	r, err := models.RunCommandReader("calaos_install.sh", INSTALL_LOG_FILE, INSTALL_EXIT_CODE_FILE, n.Device)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.SendStream(r)
}

func (a *AppServer) apiSystemLastInstallStatus(c *fiber.Ctx) (err error) {
	data, err := os.ReadFile(INSTALL_EXIT_CODE_FILE)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	exitCode, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	data, err = os.ReadFile(INSTALL_LOG_FILE)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error":     false,
		"msg":       "ok",
		"exit_code": exitCode,
		"log":       string(data),
	})
}

func (a *AppServer) apiSystemInfo(c *fiber.Ctx) (err error) {
	info, err := models.GetSystemInfo()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error":  false,
		"msg":    "ok",
		"output": info,
	})
}
