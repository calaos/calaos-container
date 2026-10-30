package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/calaos/calaos-container/models"
	"github.com/gofiber/fiber/v2"
)

func legacyOK(c *fiber.Ctx) error {
	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error": false,
		"msg":   "ok",
	})
}

func reboot() error {
	ctx, cancel := context.WithTimeout(context.Background(), serviceCallLimit)
	defer cancel()
	return models.Bus.Reboot(ctx)
}

// restartHome waits for the job, so that a failed restart reaches the client.
func restartHome() error {
	svc, ok := models.LookupService("calaos-home")
	if !ok {
		return errors.New("calaos-home is not in the service table")
	}
	ctx, cancel := context.WithTimeout(context.Background(), serviceJobLimit)
	defer cancel()
	job, err := models.QueueRestart(ctx, models.Bus, svc, svc.Unit)
	if err != nil {
		return err
	}
	return job.Wait(ctx)
}

// apiSystemReboot answers 409 during an upgrade, the one deliberate change to
// a legacy route: a reboot there would cut dpkg, and old kiosks handle any
// non-2xx the same way.
func (a *AppServer) apiSystemReboot(c *fiber.Ctx) error {
	if err := rebootGuard(reboot); err != nil {
		if errors.Is(err, models.ErrUpgradeInProgress) {
			return legacyError(c, fiber.StatusConflict, err)
		}
		return legacyError(c, fiber.StatusInternalServerError, err)
	}
	return legacyOK(c)
}

func (a *AppServer) apiSystemRestart(c *fiber.Ctx) error {
	if err := restartHome(); err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}
	return legacyOK(c)
}

// runJSONCommand runs a command printing a JSON object and decodes it.
func runJSONCommand(name string, args ...string) (map[string]interface{}, error) {
	out, err := runCommand(name, args...)
	if err != nil {
		return nil, err
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (a *AppServer) apiSystemFsStatus(c *fiber.Ctx) error {
	parsed, err := runJSONCommand("findmnt", "--json", "/")
	if err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error":  false,
		"msg":    "ok",
		"output": parsed,
	})
}

// apiSystemRollbackSnapshot keeps its legacy behaviour during an upgrade (no
// 409), an accepted risk until old kiosks are gone.
func (a *AppServer) apiSystemRollbackSnapshot(c *fiber.Ctx) error {
	if _, err := runCommand("calaos_rollback"); err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}
	if err := reboot(); err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}
	return legacyOK(c)
}

func (a *AppServer) apiSystemInstallListDevices(c *fiber.Ctx) error {
	parsed, err := runJSONCommand("lsblk", lsblkInventoryArgs...)
	if err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error":  false,
		"msg":    "ok",
		"output": parsed,
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
	out, err := runCommand("lsblk", lsblkInventoryArgs...)
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

func (a *AppServer) apiSystemInstallStart(c *fiber.Ctx) error {
	n := new(InstallOpts)
	if err := c.BodyParser(n); err != nil {
		return legacyError(c, fiber.StatusBadRequest, err)
	}

	devices, err := getBlockDeviceInventory()
	if err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}

	if verr := validateInstallDevice(*n, devices); verr != nil {
		return legacyError(c, fiber.StatusBadRequest, verr)
	}

	if err := streamInstall(c, n.Device); err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}
	return nil
}

// streamInstall starts the installer on device and streams its log as the
// response body.
func streamInstall(c *fiber.Ctx, device string) error {
	// The installer is started detached: it writes its output to
	// installLogFile and its exit status to installExitCodeFile on its
	// own, and runs to completion whatever happens to this request.
	proc, err := startLoggedCommand(models.LoggedCommand{
		Name:         "calaos_install",
		Args:         []string{device},
		LogFile:      installLogFile,
		ExitCodeFile: installExitCodeFile,
	})
	if err != nil {
		return err
	}
	setInstallProc(proc)

	// Streaming is a read-only view on that log file. Capture what the tail
	// needs *before* returning: the body stream writer runs after the handler
	// is done, when the fiber/fasthttp context must no longer be touched.
	// c.Context().Done() is a plain channel owned by the fasthttp server (it
	// is closed on server shutdown, not on client disconnect), so it is safe
	// to keep; a disconnected client is detected by the write to w failing.
	serverDone := c.Context().Done()
	installDone := proc.Done()
	logFile := installLogFile

	c.Set(fiber.HeaderContentType, fiber.MIMETextPlainCharsetUTF8)
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		ctx, cancel := contextFromDone(serverDone)
		defer cancel()

		// Whatever ends the tail — client gone, server shutting down, install
		// finished — only this stream is affected, never the installer.
		if terr := models.TailFile(ctx, logFile, w, models.TailOptions{Until: installDone}); terr != nil {
			logging.Debugf("install stream for pid %d ended: %v", proc.Pid(), terr)
		}
	})

	return nil
}

// contextFromDone adapts a bare done channel to a context.Context, so a
// cancellation signal that is not already a context (fasthttp's
// RequestCtx.Done()) can bound context-aware helpers. The returned cancel
// function must be called to release the watching goroutine.
func contextFromDone(done <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if done == nil {
		return ctx, cancel
	}

	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()

	return ctx, cancel
}

func (a *AppServer) apiSystemLastInstallStatus(c *fiber.Ctx) error {
	data, err := os.ReadFile(installExitCodeFile)
	if err != nil {
		return legacyError(c, fiber.StatusBadRequest, err)
	}

	exitCode, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return legacyError(c, fiber.StatusBadRequest, err)
	}

	data, err = os.ReadFile(installLogFile)
	if err != nil {
		return legacyError(c, fiber.StatusBadRequest, err)
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error":     false,
		"msg":       "ok",
		"exit_code": exitCode,
		"log":       string(data),
	})
}

func (a *AppServer) apiSystemInfo(c *fiber.Ctx) error {
	info, err := getSystemInfo()
	if err != nil {
		return legacyError(c, fiber.StatusInternalServerError, err)
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"error":  false,
		"msg":    "ok",
		"output": info,
	})
}

// guardedError maps a RebootGuard failure to the v2 vocabulary.
func guardedError(err error) error {
	if errors.Is(err, models.ErrUpgradeInProgress) {
		return errUpgradeInProgress(err)
	}
	return errInternal(err)
}

func (a *AppServer) v2SystemReboot(c *fiber.Ctx) error {
	if err := rebootGuard(reboot); err != nil {
		return guardedError(err)
	}
	return sendOK(c, fiber.StatusOK, nil)
}

func (a *AppServer) v2SystemRestart(c *fiber.Ctx) error {
	if err := restartHome(); err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, nil)
}

func (a *AppServer) v2SystemInfo(c *fiber.Ctx) error {
	info, err := getSystemInfo()
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, info)
}

func (a *AppServer) v2SystemFsStatus(c *fiber.Ctx) error {
	parsed, err := runJSONCommand("findmnt", "--json", "/")
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, parsed)
}

// v2SystemRollbackSnapshot guards rollback and reboot together, so an
// upgrade can never start between them.
func (a *AppServer) v2SystemRollbackSnapshot(c *fiber.Ctx) error {
	err := rebootGuard(func() error {
		if _, err := runCommand("calaos_rollback"); err != nil {
			return err
		}
		return reboot()
	})
	if err != nil {
		return guardedError(err)
	}
	return sendOK(c, fiber.StatusOK, nil)
}

func (a *AppServer) v2InstallListDevices(c *fiber.Ctx) error {
	parsed, err := runJSONCommand("lsblk", lsblkInventoryArgs...)
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, parsed)
}

// v2InstallStart returns the validateInstallDevice text as msg: it only
// describes the requested device.
func (a *AppServer) v2InstallStart(c *fiber.Ctx) error {
	n := new(InstallOpts)
	if err := c.BodyParser(n); err != nil {
		return errInvalid("invalid JSON body")
	}

	devices, err := getBlockDeviceInventory()
	if err != nil {
		return errInternal(err)
	}

	if verr := validateInstallDevice(*n, devices); verr != nil {
		return errInvalid(verr.Error())
	}

	if err := streamInstall(c, n.Device); err != nil {
		return errInternal(err)
	}
	return nil
}

var (
	installMu   sync.Mutex
	installProc models.Process
)

func setInstallProc(p models.Process) {
	installMu.Lock()
	installProc = p
	installMu.Unlock()
}

// installRunning reports an installer started by this daemon and still alive.
func installRunning() bool {
	installMu.Lock()
	p := installProc
	installMu.Unlock()
	if p == nil {
		return false
	}
	select {
	case <-p.Done():
		return false
	default:
		return true
	}
}

// v2InstallStatus derives the state from the files the installer writes: the
// log is created at start, the exit code at the end. A log without exit code
// and without a live process is an installer that failed to start or outlived
// a daemon restart: it never writes its code, so it reads as finished, -1.
func (a *AppServer) v2InstallStatus(c *fiber.Ctx) error {
	code, codeErr := os.ReadFile(installExitCodeFile)
	if codeErr != nil && !errors.Is(codeErr, fs.ErrNotExist) {
		return errInternal(codeErr)
	}

	log, logErr := os.ReadFile(installLogFile)
	if logErr != nil && !errors.Is(logErr, fs.ErrNotExist) {
		return errInternal(logErr)
	}

	if codeErr != nil {
		switch {
		case logErr != nil:
			return sendOK(c, fiber.StatusOK, fiber.Map{"status": "never_run"})
		case installRunning():
			return sendOK(c, fiber.StatusOK, fiber.Map{"status": "running"})
		default:
			return sendOK(c, fiber.StatusOK, fiber.Map{"status": "finished", "exit_code": -1, "log": string(log)})
		}
	}

	exitCode, err := strconv.Atoi(strings.TrimSpace(string(code)))
	if err != nil {
		return errInternal(fmt.Errorf("parse %s: %w", installExitCodeFile, err))
	}

	return sendOK(c, fiber.StatusOK, fiber.Map{
		"status":    "finished",
		"exit_code": exitCode,
		"log":       string(log),
	})
}
