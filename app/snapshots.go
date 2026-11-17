package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

// Inside a snapshot tree. Without the overlay hook a snapshot boots
// read-only and most services fail. Without the unit (enabled by the package)
// nothing clears next_entry once booted, as GRUB cannot write its grubenv on
// btrfs: the box would come back to the snapshot on every reboot.
const (
	overlayHookRel = "usr/share/initramfs-tools/scripts/local-bottom/grub-btrfs-overlay"
	oneShotUnitRel = "etc/systemd/system/sysinit.target.wants/calaos-snapshot-oneshot.service"
)

// Test seams: the files read and the deferred reboot.
var (
	procCmdlinePath  = "/proc/cmdline"
	grubCfgPath      = "/boot/grub/grub.cfg"
	grubBtrfsCfgPath = "/boot/grub/grub-btrfs.cfg"
	// In subvolume @/boot/grub, shared by the system and every snapshot.
	grubEnvPath = "/boot/grub/grubenv"
	// On the FAT ESP, which GRUB can write: grub/41_calaos-snapshot-boot reads
	// and clears calaos_oneshot there.
	espMountPath  = "/efi"
	espEnvPath    = "/efi/calaos/grubenv"
	snapshotsRoot = "/.snapshots"
	// apt takes its pre/post snapshots only when both exist (80snapper is a
	// conffile of the snapper package, and does nothing without the config).
	snapperRootConfigPath = "/etc/snapper/configs/root"
	aptSnapperHookPath    = "/etc/apt/apt.conf.d/80snapper"
	snapAfterFunc = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	snapLocation  = time.Local
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
	Booted       *snapshotInfo  `json:"booted"`
	Snapshots    []snapshotInfo `json:"snapshots"`
	AptSnapshots bool           `json:"apt_snapshots"`
}

func aptSnapshotsEnabled() bool {
	for _, p := range []string{snapperRootConfigPath, aptSnapperHookPath} {
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return false
		}
	}
	return true
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

// queryTimeout bounds the read-only probes (snapper, findmnt): a snapper
// stuck on a lock or a slow btrfs must not pin a request goroutine, and
// repeated GETs would pile them up.
var queryTimeout = 10 * time.Second

func queryCommand(command string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	return runCommandCtx(ctx, command, args...)
}

// readRescueStatus applies the kiosk rule: the overlay the initramfs hook
// mounts is named rootfs, and the snapshot comes from rootflags.
func readRescueStatus() (rescueStatus, error) {
	var st rescueStatus
	out, err := queryCommand("findmnt", "--json", "/")
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
	out, err := queryCommand("snapper", "--no-dbus", "-c", "root", "--jsonout", "list")
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

// bootTargets are the snapshots a one-shot boot may target, and how to arm it.
type bootTargets struct {
	paths  map[int]string // grub path of each bootable snapshot
	tooOld map[int]bool   // in the menu, but missing what a safe boot needs
	known  map[int]bool   // every snapshot snapper lists, before the cap
	esp    bool
}

// espOneShot reports whether grub.cfg reads the one-shot from the ESP. Older
// grub.cfg files, BIOS boxes and boxes without a FAT ESP use next_entry.
func espOneShot() bool {
	cfg, err := os.ReadFile(grubCfgPath)
	if err != nil || !strings.Contains(string(cfg), "calaos_oneshot") {
		return false
	}
	out, err := queryCommand("findmnt", "-n", "-o", "FSTYPE", espMountPath)
	return err == nil && strings.TrimSpace(out) == "vfat"
}

// resolveInTree resolves rel inside tree as the snapshot's own root would:
// an absolute link target is re-rooted at tree, and a path that climbs above
// tree is refused. Following a link with the host's resolver would read the
// running system's /usr instead of the snapshot's.
func resolveInTree(tree, rel string, followLast bool) (string, error) {
	const maxLinks = 40
	links := 0
	pending := strings.Split(rel, "/")
	var done []string
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(done) == 0 {
				return "", errors.New("path leaves the snapshot tree")
			}
			done = done[:len(done)-1]
			continue
		}
		cur := filepath.Join(append([]string{tree}, append(done, part)...)...)
		fi, err := os.Lstat(cur)
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink != 0 && (followLast || len(pending) > 0) {
			if links++; links > maxLinks {
				return "", errors.New("too many symbolic links")
			}
			target, err := os.Readlink(cur)
			if err != nil {
				return "", err
			}
			if strings.HasPrefix(target, "/") {
				done = nil
			}
			pending = append(strings.Split(target, "/"), pending...)
			continue
		}
		done = append(done, part)
	}
	return filepath.Join(append([]string{tree}, done...)...), nil
}

// snapshotTooOld checks the snapshot's own tree: it boots its own kernel,
// initramfs and units, not the running system's.
func snapshotTooOld(id int, esp bool) bool {
	tree := filepath.Join(snapshotsRoot, strconv.Itoa(id), "snapshot")
	// A directory stood there before the initramfs install was fixed.
	hook, err := resolveInTree(tree, overlayHookRel, false)
	if err != nil {
		return true
	}
	if fi, err := os.Lstat(hook); err != nil || !fi.Mode().IsRegular() {
		return true
	}
	if esp {
		return false
	}
	// The enabling link must lead to an existing unit file of the snapshot.
	unit, err := resolveInTree(tree, oneShotUnitRel, true)
	if err != nil {
		return true
	}
	fi, err := os.Stat(unit)
	return err != nil || !fi.Mode().IsRegular()
}

// loadSnapshots returns the snapper list, newest first and capped, with the
// snapshots a boot may target.
func loadSnapshots(st rescueStatus) (snapshotList, bootTargets, error) {
	targets := bootTargets{paths: map[int]string{}, tooOld: map[int]bool{}, known: map[int]bool{}}
	raw, err := listSnapper()
	if err != nil {
		return snapshotList{}, targets, err
	}
	// Booting a snapshot is only armed from the normal system: its grub.cfg
	// carries the one-shot snippet, and from the rescue the way out is a
	// rollback or a plain reboot.
	if !st.Rescue {
		menu, err := grubSnapshotEntries()
		if err != nil {
			logging.Errorf("snapshot boot menu unreadable, nothing is bootable: %v", err)
		} else {
			targets.esp = espOneShot()
			for id, path := range menu {
				if snapshotTooOld(id, targets.esp) {
					targets.tooOld[id] = true
				} else {
					targets.paths[id] = path
				}
			}
		}
	}

	list := snapshotList{Snapshots: []snapshotInfo{}}
	for _, e := range raw {
		if e.Number <= 0 {
			continue
		}
		targets.known[e.Number] = true
		current := e.Active
		if st.Rescue {
			current = st.Snapshot != nil && *st.Snapshot == e.Number
		}
		_, bootable := targets.paths[e.Number]
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
	return list, targets, nil
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
	list.AptSnapshots = aptSnapshotsEnabled()
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
		if errors.As(err, &typeErr) && typeErr.Field == "id" {
			return invalid
		}
		return errInvalid("the body must be a JSON object")
	}
	if req.ID == nil || *req.ID <= 0 {
		return invalid
	}
	id := *req.ID
	auditField(c, "snapshot", strconv.Itoa(id))

	st, err := readRescueStatus()
	if err != nil {
		return errInternal(err)
	}
	_, targets, err := loadSnapshots(st)
	if err != nil {
		return errRescueUnavailable(err)
	}
	if !targets.known[id] {
		return invalid
	}
	if targets.tooOld[id] {
		return newAPIError(fiber.StatusConflict, codeSnapshotTooOld, "this snapshot predates single-use snapshot boots")
	}
	path, ok := targets.paths[id]
	if !ok {
		return newAPIError(fiber.StatusConflict, codeNotBootable, "this snapshot is not in the boot menu")
	}

	err = rebootGuard(func() error {
		if err := armOneShot(path, targets.esp); err != nil {
			return err
		}
		scheduleReboot(func() {
			// Left armed, the snapshot would boot on the next manual reboot.
			disarmOneShot(targets.esp)
		})
		return nil
	})
	if err != nil {
		return guardedError(err)
	}
	return sendOK(c, fiber.StatusOK, nil)
}

func armOneShot(path string, esp bool) error {
	if !esp {
		_, err := runCommand("grub-reboot", path)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(espEnvPath), 0o750); err != nil {
		return err
	}
	_, err := runCommand("grub-editenv", espEnvPath, "set", "calaos_oneshot="+path)
	return err
}

func disarmOneShot(esp bool) {
	file, name := grubEnvPath, "next_entry"
	if esp {
		file, name = espEnvPath, "calaos_oneshot"
	}
	if _, err := runCommand("grub-editenv", file, "unset", name); err != nil {
		logging.Errorf("clearing %s: %v", name, err)
	}
}

// ClearBootOneShot drops a snapshot boot still armed when the daemon starts:
// it was consumed by this boot, or it outlived a failed reboot. GRUB clears
// next_entry itself only off btrfs, and the ESP one only when it can write it.
func ClearBootOneShot() {
	clearEnvVar(grubEnvPath, "next_entry")
	clearEnvVar(espEnvPath, "calaos_oneshot")
}

func clearEnvVar(file, name string) {
	data, err := os.ReadFile(file) //nolint:gosec // fixed grubenv paths, never from a request
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, name+"="); ok && v != "" {
			if _, err := runCommand("grub-editenv", file, "unset", name); err != nil {
				logging.Errorf("clearing %s in %s: %v", name, file, err)
			}
			return
		}
	}
}

// inRescue fails open: a box whose mount table cannot be read is far more
// likely a normal one, and refusing would block its updates for good.
func inRescue() bool {
	st, err := readRescueStatus()
	if err != nil {
		logging.Errorf("reading the rescue status: %v", err)
	}
	return st.Rescue
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
