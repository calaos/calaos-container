package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

// Snapshot routes: listing snapper snapshots, booting one once through
// grub-btrfs, and the rescue mode such a boot lands in (docs/API.md).

const (
	// GRUB_BTRFS_SUBMENUNAME in grub-btrfs/config.
	grubSnapshotsMenu = "Calaos-OS snapshots"
	maxSnapshotList   = 200
	snapRebootDelay   = 2 * time.Second
)

// Test seams: the files read and the deferred reboot.
var (
	procCmdlinePath  = "/proc/cmdline"
	grubCfgPath      = "/boot/grub/grub.cfg"
	grubBtrfsCfgPath = "/boot/grub/grub-btrfs.cfg"
	// Without the overlay hook a snapshot boots read-only and most services
	// fail, so booting one is refused.
	overlayHookPath = "/usr/share/initramfs-tools/scripts/local-bottom/grub-btrfs-overlay"
	snapAfterFunc   = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	snapLocation    = time.Local
)

// Same pattern as calaos_snapshot_number_from_subvol (scripts/lib).
var snapshotSubvolRe = regexp.MustCompile(`@/\.snapshots/([0-9]+)/snapshot`)

type snapshotInfo struct {
	ID          int    `json:"id"`
	Date        string `json:"date"`
	Type        string `json:"type"`
	PreID       *int   `json:"pre_id"`
	Description string `json:"description"`
	Cleanup     string `json:"cleanup"`
	Current     bool   `json:"current"`
	Default     bool   `json:"default"`
	Bootable    bool   `json:"bootable"`
}

type snapshotList struct {
	Booted    *snapshotInfo  `json:"booted"`
	Snapshots []snapshotInfo `json:"snapshots"`
}

type rescueStatus struct {
	Rescue   bool `json:"rescue"`
	Snapshot *int `json:"snapshot"`
	Overlay  bool `json:"overlay"`
}

func errRescueUnavailable(cause error) *apiError {
	e := newAPIError(fiber.StatusServiceUnavailable, codeRescueUnavailable, "snapshots are not available on this system")
	e.cause = cause
	return e
}

// readRescueStatus applies the kiosk rule: the overlay the initramfs hook
// mounts is named rootfs, and the snapshot comes from rootflags.
func readRescueStatus() (rescueStatus, error) {
	var st rescueStatus
	out, err := runCommand("findmnt", "--json", "/")
	if err != nil {
		return st, err
	}
	var fm struct {
		Filesystems []struct {
			Source string `json:"source"`
			FsType string `json:"fstype"`
		} `json:"filesystems"`
	}
	if err := json.Unmarshal([]byte(out), &fm); err != nil {
		return st, err
	}
	if len(fm.Filesystems) == 0 {
		return st, errors.New("findmnt returned no filesystem for /")
	}
	root := fm.Filesystems[0]
	st.Overlay = root.FsType == "overlay"
	st.Rescue = st.Overlay && root.Source == "rootfs"

	cmdline, err := os.ReadFile(procCmdlinePath)
	if err != nil {
		return st, err
	}
	st.Snapshot = snapshotFromCmdline(string(cmdline))
	return st, nil
}

func snapshotFromCmdline(cmdline string) *int {
	for _, tok := range strings.Fields(cmdline) {
		flags, ok := strings.CutPrefix(tok, "rootflags=")
		if !ok {
			continue
		}
		if m := snapshotSubvolRe.FindStringSubmatch(flags); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				return &n
			}
		}
	}
	return nil
}

type snapperEntry struct {
	Number      int    `json:"number"`
	Default     bool   `json:"default"`
	Active      bool   `json:"active"`
	Type        string `json:"type"`
	PreNumber   *int   `json:"pre-number"`
	Date        string `json:"date"`
	Cleanup     string `json:"cleanup"`
	Description string `json:"description"`
}

func listSnapper() ([]snapperEntry, error) {
	out, err := runCommand("snapper", "--no-dbus", "-c", "root", "--jsonout", "list")
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Root []snapperEntry `json:"root"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, err
	}
	return parsed.Root, nil
}

// snapperDate turns snapper's local "YYYY-MM-DD HH:MM:SS" into RFC 3339; an
// unexpected format gives "" rather than a wrong date.
func snapperDate(s string) string {
	t, err := time.ParseInLocation(time.DateTime, s, snapLocation)
	if err != nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// grubSnapshotEntries maps each snapshot of the grub-btrfs menu to the GRUB
// path of its first kernel entry. Every entry shares one --id, so the path is
// built from titles: "<menu>><snapshot submenu>><kernel entry>".
func grubSnapshotEntries() (map[int]string, error) {
	cfg, err := os.ReadFile(grubCfgPath)
	if err != nil {
		return nil, err
	}
	entries := map[int]string{}
	if !strings.Contains(string(cfg), "submenu '"+grubSnapshotsMenu+"'") {
		return entries, nil
	}

	f, err := os.Open(grubBtrfsCfgPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var submenu, entry string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "submenu '"):
			// The column header "submenu '...' { echo }" fits on one line.
			if strings.HasSuffix(line, "{") {
				submenu, entry = singleQuoted(line), ""
			}
		case strings.HasPrefix(line, "menuentry '"):
			if submenu != "" {
				entry = singleQuoted(line)
			}
		case strings.HasPrefix(line, "linux "):
			if submenu == "" || entry == "" {
				continue
			}
			m := snapshotSubvolRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			if _, seen := entries[n]; !seen {
				entries[n] = grubPath(grubSnapshotsMenu, submenu, entry)
			}
			entry = ""
		}
	}
	return entries, sc.Err()
}

// singleQuoted returns the first '...' of line. grub-btrfs strips quotes
// from titles, so no escaped quote can occur.
func singleQuoted(line string) string {
	_, rest, ok := strings.Cut(line, "'")
	if !ok {
		return ""
	}
	title, _, ok := strings.Cut(rest, "'")
	if !ok {
		return ""
	}
	return title
}

// grubPath joins menu titles with '>', which GRUB reads as a level
// separator; a literal '>' is written '>>'.
func grubPath(titles ...string) string {
	for i, t := range titles {
		titles[i] = strings.ReplaceAll(t, ">", ">>")
	}
	return strings.Join(titles, ">")
}

// loadSnapshots returns the snapper list, newest first and capped, with the
// grub paths of the bootable ones.
func loadSnapshots(st rescueStatus) (snapshotList, map[int]string, error) {
	raw, err := listSnapper()
	if err != nil {
		return snapshotList{}, nil, err
	}
	// From the rescue overlay, grub-reboot would write a grubenv that does
	// not survive the reboot: nothing is bootable there.
	paths := map[int]string{}
	if !st.Rescue {
		if paths, err = grubSnapshotEntries(); err != nil {
			paths = map[int]string{}
		}
	}

	list := snapshotList{Snapshots: []snapshotInfo{}}
	for _, e := range raw {
		if e.Number <= 0 {
			continue
		}
		current := e.Active
		if st.Rescue {
			current = st.Snapshot != nil && *st.Snapshot == e.Number
		}
		_, bootable := paths[e.Number]
		info := snapshotInfo{
			ID:          e.Number,
			Date:        snapperDate(e.Date),
			Type:        e.Type,
			PreID:       e.PreNumber,
			Description: e.Description,
			Cleanup:     e.Cleanup,
			Current:     current,
			Default:     e.Default,
			Bootable:    bootable,
		}
		if current {
			b := info
			list.Booted = &b
		}
		list.Snapshots = append(list.Snapshots, info)
	}
	sort.Slice(list.Snapshots, func(i, j int) bool { return list.Snapshots[i].ID > list.Snapshots[j].ID })
	if len(list.Snapshots) > maxSnapshotList {
		list.Snapshots = list.Snapshots[:maxSnapshotList]
	}
	return list, paths, nil
}

func (a *AppServer) v2SystemSnapshots(c *fiber.Ctx) error {
	st, err := readRescueStatus()
	if err != nil {
		return errInternal(err)
	}
	list, _, err := loadSnapshots(st)
	if err != nil {
		return errRescueUnavailable(err)
	}
	return sendOK(c, fiber.StatusOK, list)
}

func (a *AppServer) v2SystemRescue(c *fiber.Ctx) error {
	st, err := readRescueStatus()
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, st)
}

// scheduleReboot lets the response leave before the system goes down. A
// failed reboot keeps the upgrade guard held, as the system was told to stop.
func scheduleReboot(onFail func()) {
	snapAfterFunc(snapRebootDelay, func() {
		if err := reboot(); err != nil {
			logging.Errorf("deferred reboot failed: %v", err)
			if onFail != nil {
				onFail()
			}
		}
	})
}

func (a *AppServer) v2SystemSnapshotBoot(c *fiber.Ctx) error {
	invalid := newAPIError(fiber.StatusBadRequest, codeInvalidSnapshot, "unknown snapshot")
	var req struct {
		ID *int `json:"id"`
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return invalid
		}
		return errInvalid("the body must be a JSON object")
	}
	if req.ID == nil || *req.ID <= 0 {
		return invalid
	}
	id := *req.ID
	auditField(c, "snapshot", strconv.Itoa(id))

	if _, err := os.Stat(overlayHookPath); err != nil {
		return errRescueUnavailable(err)
	}
	st, err := readRescueStatus()
	if err != nil {
		return errInternal(err)
	}
	list, paths, err := loadSnapshots(st)
	if err != nil {
		return errRescueUnavailable(err)
	}
	known := false
	for _, s := range list.Snapshots {
		known = known || s.ID == id
	}
	if !known {
		return invalid
	}
	path, ok := paths[id]
	if !ok {
		return newAPIError(fiber.StatusConflict, codeNotBootable, "this snapshot is not in the boot menu")
	}

	err = rebootGuard(func() error {
		if _, err := runCommand("grub-reboot", path); err != nil {
			return err
		}
		scheduleReboot(func() {
			// Left set, next_entry would boot the snapshot on the next
			// manual reboot.
			if _, err := runCommand("grub-editenv", "-", "unset", "next_entry"); err != nil {
				logging.Errorf("clearing next_entry: %v", err)
			}
		})
		return nil
	})
	if err != nil {
		return guardedError(err)
	}
	return sendOK(c, fiber.StatusOK, nil)
}

func (a *AppServer) v2SystemSnapshotRollback(c *fiber.Ctx) error {
	st, err := readRescueStatus()
	if err != nil {
		return errInternal(err)
	}
	if !st.Rescue || st.Snapshot == nil {
		return newAPIError(fiber.StatusConflict, codeNotInRescue, "the system is not running from a snapshot")
	}
	auditField(c, "snapshot", strconv.Itoa(*st.Snapshot))

	err = rebootGuard(func() error {
		if _, err := runCommand("calaos_rollback"); err != nil {
			return err
		}
		scheduleReboot(nil)
		return nil
	})
	if err != nil {
		return guardedError(err)
	}
	return sendOK(c, fiber.StatusOK, nil)
}
