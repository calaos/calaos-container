package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/calaos/calaos-container/models"
)

// grub.cfg names the kernel by its path inside the default subvolume
// (/@/.snapshots/N/snapshot/boot/...). A rollback that left it untouched keeps
// booting the old root's kernel, until snapper deletes that snapshot and the
// box no longer boots. These repair it at startup.
//
// Like calaos_rollback, they take the first entry for the default one, as
// Calaos' GRUB_DEFAULT=saved does without a saved entry. A mismatch update-grub
// cannot fix makes every start run it again.
var (
	// calaos_rollback writes it once it regenerated grub.cfg from a chroot,
	// without the grub-btrfs submenu: the native update-grub brings it back.
	grubRefreshMarker = "/var/lib/calaos-container/grub-refresh"
	// Same bound as update_grub_timeout in calaos_rollback.
	grubUpdateTimeout  = 10 * time.Minute
	grubRepairLockWait = 30 * time.Minute
)

type grubState struct {
	Overlay       bool
	Cfg           []byte // nil when the box has no grub.cfg
	DefaultSubvol string // "@/.snapshots/N/snapshot", "" when unknown
	RootSubvol    string // the mounted root, "/@/.snapshots/N/snapshot"
	Marker        bool
}

// grubRepairReason says why grub.cfg must be regenerated, "" when it must not.
// update-grub writes the paths of the mounted root: it only runs when that
// root is the default subvolume.
func grubRepairReason(st grubState) string {
	if st.Overlay || st.Cfg == nil || st.DefaultSubvol == "" || st.RootSubvol != "/"+st.DefaultSubvol {
		return ""
	}
	if why := grubCfgMismatch(st.Cfg, st.DefaultSubvol); why != "" {
		return why
	}
	if st.Marker {
		return "refresh requested by calaos_rollback"
	}
	return ""
}

// grubCfgMismatch tells why the default entry of cfg does not boot the kernel
// of subvol, "" when it does.
func grubCfgMismatch(cfg []byte, subvol string) string {
	paths := grubDefaultEntryPaths(cfg)
	if len(paths) == 0 {
		return "grub.cfg has no boot entry"
	}
	prefix := "/" + subvol + "/boot/"
	for _, p := range paths {
		if !strings.HasPrefix(p, prefix) {
			return fmt.Sprintf("the default entry loads %s, outside %s", p, subvol)
		}
	}
	return ""
}

// grubDefaultEntryPaths returns the kernel and initrd paths of the first boot
// entry, the one set default="0" picks. Same parsing as calaos_rollback.
func grubDefaultEntryPaths(cfg []byte) []string {
	var paths []string
	sc := bufio.NewScanner(bytes.NewReader(cfg))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "linux" && len(f) > 1:
			if paths != nil {
				return paths
			}
			paths = []string{f[1]}
		case f[0] == "initrd" && paths != nil:
			return append(paths, f[1:]...)
		case f[0] == "}" && paths != nil:
			return paths
		}
	}
	if sc.Err() != nil {
		// The entry may be cut before its initrd line: fail closed.
		return nil
	}
	return paths
}

// parseDefaultSubvol reads `btrfs subvolume get-default`; the top level
// ("ID 5 (FS_TREE)") has no path and gives "".
func parseDefaultSubvol(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	_, path, ok := strings.Cut(line, " path ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(path)
}

// subvolOfSource reads the subvolume of a findmnt source, "/dev/sda2[/@/x]".
func subvolOfSource(source string) string {
	_, rest, ok := strings.Cut(source, "[")
	if !ok {
		return ""
	}
	sub, _, _ := strings.Cut(rest, "]")
	return sub
}

// RepairGrubCfg regenerates grub.cfg when its default entry does not boot the
// default subvolume, or when calaos_rollback asked for it. It never blocks
// startup and fails closed: when the state cannot be read it does nothing.
func RepairGrubCfg() {
	st, err := readGrubState()
	if err != nil {
		logging.Errorf("grub.cfg check skipped: %v", err)
		return
	}
	if st.Cfg != nil && !st.Overlay && st.DefaultSubvol != "" && st.RootSubvol != "/"+st.DefaultSubvol {
		logging.Warnf("grub.cfg check skipped: root %q is not the default subvolume %q", st.RootSubvol, st.DefaultSubvol)
		return
	}
	why := grubRepairReason(st)
	if why == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), grubRepairLockWait)
	defer cancel()
	err = models.WithUpgradeLock(ctx, func() {
		logging.Warnf("regenerating grub.cfg for %s: %s", st.DefaultSubvol, why)
		if err := regenerateGrubCfg(st.DefaultSubvol); err != nil {
			logging.Errorf("grub.cfg repair failed: %v", err)
			return
		}
		if err := os.Remove(grubRefreshMarker); err != nil && !errors.Is(err, os.ErrNotExist) {
			logging.Errorf("removing %s: %v", grubRefreshMarker, err)
		}
		logging.Infof("grub.cfg regenerated for %s", st.DefaultSubvol)
	})
	if err != nil {
		logging.Errorf("grub.cfg repair skipped, upgrade lock busy: %v", err)
	}
}

func regenerateGrubCfg(subvol string) error {
	ctx, cancel := context.WithTimeout(context.Background(), grubUpdateTimeout)
	defer cancel()
	if _, err := runCommandCtx(ctx, "update-grub"); err != nil {
		return fmt.Errorf("update-grub: %w", err)
	}
	cfg, err := os.ReadFile(grubCfgPath)
	if err != nil {
		return err
	}
	if why := grubCfgMismatch(cfg, subvol); why != "" {
		return errors.New(why)
	}
	return nil
}

func readGrubState() (grubState, error) {
	var st grubState
	cfg, err := os.ReadFile(grubCfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	rs, err := readRescueStatus()
	if err != nil {
		return st, err
	}
	st.Cfg, st.Overlay, st.RootSubvol = cfg, rs.Overlay, rs.rootSubvol
	if st.Overlay {
		return st, nil
	}
	out, err := queryCommand("btrfs", "subvolume", "get-default", snapshotsRoot)
	if err != nil {
		return st, fmt.Errorf("reading the default subvolume: %w", err)
	}
	st.DefaultSubvol = parseDefaultSubvol(out)
	if _, err := os.Stat(grubRefreshMarker); err == nil {
		st.Marker = true
	}
	return st, nil
}
