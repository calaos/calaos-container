package app

import (
	"encoding/json"
	"fmt"
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

	_, err = models.RunCommand("calaos_rollback")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	return a.apiSystemReboot(c)
}

func (a *AppServer) apiSystemInstallListDevices(c *fiber.Ctx) (err error) {

	out, err := models.RunCommand("lsblk", lsblkInventoryArgs...)

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

// lsblkInventoryArgs is the exact lsblk invocation used to build the block
// device inventory. It is shared between apiSystemInstallListDevices (which
// returns the raw output-all JSON to API callers) and
// getBlockDeviceInventory (which parses a typed subset of it to validate
// install targets) so both code paths always see the same device list.
var lsblkInventoryArgs = []string{"--bytes", "--json", "--paths", "--output-all"}

// lsblkDevice is a typed subset of the fields lsblk --json --output-all
// produces for each block device: its device path, its type
// (disk/part/rom/loop/...), where it (or its descendants, e.g. partitions)
// are mounted, and its children so a whole disk can be checked for a root
// filesystem living on one of its partitions.
type lsblkDevice struct {
	Name        string        `json:"name"`
	Path        string        `json:"path"`
	Type        string        `json:"type"`
	Mountpoint  *string       `json:"mountpoint"`
	Mountpoints []*string     `json:"mountpoints"`
	Children    []lsblkDevice `json:"children,omitempty"`
}

type lsblkInventory struct {
	BlockDevices []lsblkDevice `json:"blockdevices"`
}

// getBlockDeviceInventory runs the same lsblk inventory command used by
// GET /system/install/list_devices and parses it into the typed device tree
// used to validate install targets.
func getBlockDeviceInventory() ([]lsblkDevice, error) {
	out, err := models.RunCommand("lsblk", lsblkInventoryArgs...)
	if err != nil {
		return nil, err
	}

	var inv lsblkInventory
	if err := json.Unmarshal([]byte(out), &inv); err != nil {
		return nil, err
	}

	return inv.BlockDevices, nil
}

// findLsblkDevice searches the device tree (top-level disks and,
// recursively, their children such as partitions) for the entry whose path
// matches. lsblk with --paths reports full /dev paths in both "name" and
// "path".
func findLsblkDevice(devices []lsblkDevice, path string) *lsblkDevice {
	for i := range devices {
		if devices[i].Path == path || devices[i].Name == path {
			return &devices[i]
		}
		if found := findLsblkDevice(devices[i].Children, path); found != nil {
			return found
		}
	}
	return nil
}

// deviceHostsMountpoint reports whether the device itself, or any of its
// descendants (partitions, and filesystems layered on top of them), is
// mounted at target. This is how the disk hosting "/" is identified even
// when the root filesystem actually lives on a partition of that disk
// (e.g. btrfs-on-partition), by walking the lsblk parent/child tree rather
// than relying on the caller to resolve PKNAME chains.
func deviceHostsMountpoint(d lsblkDevice, target string) bool {
	if d.Mountpoint != nil && *d.Mountpoint == target {
		return true
	}
	for _, mp := range d.Mountpoints {
		if mp != nil && *mp == target {
			return true
		}
	}
	for _, child := range d.Children {
		if deviceHostsMountpoint(child, target) {
			return true
		}
	}
	return false
}

// validateInstallDevice validates an install request against the block
// device inventory before the (irreversible) installer is ever launched:
// the device must exist in the inventory, be a whole disk (not a partition,
// loop or rom device), must not host the running root filesystem, and the
// caller must have explicitly confirmed the request. Kept independent of
// the fiber handler so it can be table-tested against canned lsblk-style
// inventories.
func validateInstallDevice(opts InstallOpts, devices []lsblkDevice) error {
	if opts.Device == "" {
		return fmt.Errorf("device arg not set")
	}

	dev := findLsblkDevice(devices, opts.Device)
	if dev == nil {
		return fmt.Errorf("unknown device %q: not present in the block device inventory (see GET /system/install/list_devices)", opts.Device)
	}

	if dev.Type != "disk" {
		return fmt.Errorf("device %q is not a whole disk (type=%q); only type=disk devices can be used as an install target", opts.Device, dev.Type)
	}

	if deviceHostsMountpoint(*dev, "/") {
		return fmt.Errorf("device %q hosts the root filesystem and cannot be used as an install target", opts.Device)
	}

	if !opts.Confirm {
		return fmt.Errorf(`explicit confirmation required: set "confirm": true in the request body to proceed with this destructive operation`)
	}

	return nil
}

type InstallOpts struct {
	Device  string `json:"device"`
	Confirm bool   `json:"confirm"`
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

	devices, err := getBlockDeviceInventory()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": true,
			"msg":   err.Error(),
		})
	}

	if verr := validateInstallDevice(*n, devices); verr != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": true,
			"msg":   verr.Error(),
		})
	}

	r, err := models.RunCommandReader("calaos_install", INSTALL_LOG_FILE, INSTALL_EXIT_CODE_FILE, n.Device)
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
