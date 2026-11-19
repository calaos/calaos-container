package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	subvol40  = "@/.snapshots/40/snapshot"
	getDef40  = "ID 307 gen 300 top level 257 path @/.snapshots/40/snapshot\n"
	findmnt40 = `{"filesystems":[{"target":"/","source":"/dev/sda2[/@/.snapshots/40/snapshot]","fstype":"btrfs"}]}`
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "grubcfg", name)) //nolint:gosec // fixture names are constants
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGrubDefaultEntryPaths(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want []string
	}{
		{"current", string(readFixture(t, "current.cfg")), []string{
			"/@/.snapshots/40/snapshot/boot/vmlinuz-6.1.0-56-amd64",
			"/@/.snapshots/40/snapshot/boot/initrd.img-6.1.0-56-amd64",
		}},
		{"stale", string(readFixture(t, "stale.cfg")), []string{
			"/@/.snapshots/31/snapshot/boot/vmlinuz-6.1.0-56-amd64",
			"/@/.snapshots/31/snapshot/boot/initrd.img-6.1.0-56-amd64",
		}},
		{"several initrds", "menuentry 'a' {\n\tlinux /k root=x\n\tinitrd /u /i\n}\n", []string{"/k", "/u", "/i"}},
		{"no initrd", "menuentry 'a' {\n\tlinux /k\n}\nmenuentry 'b' {\n\tlinux /k2\n\tinitrd /i2\n}\n", []string{"/k"}},
		{"no entry", "set default=0\n", nil},
		{"linux without argument", "\tlinux\n\tlinux /k\n\tinitrd /i\n", []string{"/k", "/i"}},
		{"line too long", "\tlinux /k\n" + strings.Repeat("x", 2<<20) + "\n\tinitrd /i\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := grubDefaultEntryPaths([]byte(tt.cfg)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// The script must give up on update-grub, undo its mounts and say why, before
// the daemon kills it. The snapper rollback alone had 5 minutes.
func TestRollbackTimeoutCoversUpdateGrub(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "scripts", "calaos_rollback"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^update_grub_timeout="([0-9]+[sm])"$`).FindSubmatch(script)
	if m == nil {
		t.Fatal("update_grub_timeout not found in calaos_rollback")
	}
	inScript, err := time.ParseDuration(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	if inScript != grubUpdateTimeout {
		t.Fatalf("calaos_rollback bounds update-grub to %v, the daemon to %v", inScript, grubUpdateTimeout)
	}
	const killGrace = 30 * time.Second // timeout --kill-after
	if need := inScript + killGrace + 5*time.Minute; rollbackTimeout < need {
		t.Fatalf("rollbackTimeout %v, want at least %v", rollbackTimeout, need)
	}
}

func TestParseDefaultSubvol(t *testing.T) {
	for out, want := range map[string]string{
		getDef40:           subvol40,
		"ID 5 (FS_TREE)\n": "",
		"":                 "",
	} {
		if got := parseDefaultSubvol(out); got != want {
			t.Errorf("%q: got %q, want %q", out, got, want)
		}
	}
}

func TestSubvolOfSource(t *testing.T) {
	for src, want := range map[string]string{
		"/dev/sda2[/@/.snapshots/40/snapshot]": "/" + subvol40,
		"/dev/sda2":                            "",
		"rootfs":                               "",
	} {
		if got := subvolOfSource(src); got != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}

func TestGrubRepairReason(t *testing.T) {
	current, stale := readFixture(t, "current.cfg"), readFixture(t, "stale.cfg")
	ok := grubState{Cfg: current, DefaultSubvol: subvol40, RootSubvol: "/" + subvol40}
	with := func(f func(*grubState)) grubState { st := ok; f(&st); return st }
	tests := []struct {
		name string
		st   grubState
		want string // substring, "" for no repair
	}{
		{"up to date", ok, ""},
		{"stale", with(func(s *grubState) { s.Cfg = stale }), "loads /@/.snapshots/31/snapshot/boot/vmlinuz"},
		{"no entry", with(func(s *grubState) { s.Cfg = []byte("set default=0\n") }), "no boot entry"},
		{"marker", with(func(s *grubState) { s.Marker = true }), "requested by calaos_rollback"},
		// "/@/" prefixes every snapshot path: only "/@/boot/" is the kernel of @.
		{"default is @", with(func(s *grubState) { s.DefaultSubvol, s.RootSubvol = "@", "/@" }), "outside @"},
		{"overlay", with(func(s *grubState) { s.Cfg, s.Overlay = stale, true }), ""},
		{"no grub.cfg", with(func(s *grubState) { s.Cfg, s.Marker = nil, true }), ""},
		{"default unknown", with(func(s *grubState) { s.Cfg, s.DefaultSubvol = stale, "" }), ""},
		{"root is not the default", with(func(s *grubState) { s.Cfg, s.RootSubvol = stale, "/@/.snapshots/31/snapshot" }), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grubRepairReason(tt.st)
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

type grubRepairEnv struct {
	ran     []string
	results map[string]stubResult
	// written to grub.cfg by update-grub, nil to leave it untouched
	regenerated []byte
}

func newGrubRepairEnv(t *testing.T, cfg []byte, marker bool) *grubRepairEnv {
	t.Helper()
	dir := t.TempDir()
	e := &grubRepairEnv{results: map[string]stubResult{
		"findmnt --json /": {out: findmnt40},
	}}
	prevRun, prevCfg, prevMarker := runCommandCtx, grubCfgPath, grubRefreshMarker
	prevCmdline, prevRoot := procCmdlinePath, snapshotsRoot
	t.Cleanup(func() {
		runCommandCtx, grubCfgPath, grubRefreshMarker = prevRun, prevCfg, prevMarker
		procCmdlinePath, snapshotsRoot = prevCmdline, prevRoot
	})
	grubCfgPath = filepath.Join(dir, "grub.cfg")
	grubRefreshMarker = filepath.Join(dir, "grub-refresh")
	procCmdlinePath = filepath.Join(dir, "cmdline")
	snapshotsRoot = "/.snapshots"
	e.results["btrfs subvolume get-default /.snapshots"] = stubResult{out: getDef40}
	writeFile(t, procCmdlinePath, []byte("BOOT_IMAGE=/vmlinuz root=UUID=x ro quiet\n"))
	if cfg != nil {
		writeFile(t, grubCfgPath, cfg)
	}
	if marker {
		writeFile(t, grubRefreshMarker, nil)
	}
	runCommandCtx = func(_ context.Context, command string, args ...string) (string, error) {
		cmd := strings.Join(append([]string{command}, args...), " ")
		e.ran = append(e.ran, cmd)
		if cmd == "update-grub" && e.regenerated != nil {
			writeFile(t, grubCfgPath, e.regenerated)
		}
		r := e.results[cmd]
		return r.out, r.err
	}
	return e
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *grubRepairEnv) updateGrubRan() bool { return slices.Contains(e.ran, "update-grub") }

func markerExists(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(grubRefreshMarker)
	return err == nil
}

func TestRepairGrubCfgRegeneratesStaleCfg(t *testing.T) {
	e := newGrubRepairEnv(t, readFixture(t, "stale.cfg"), true)
	e.regenerated = readFixture(t, "current.cfg")
	RepairGrubCfg()
	if !e.updateGrubRan() {
		t.Fatalf("update-grub not run: %q", e.ran)
	}
	if markerExists(t) {
		t.Fatal("marker kept after a successful repair")
	}
}

func TestRepairGrubCfgMarkerOnly(t *testing.T) {
	e := newGrubRepairEnv(t, readFixture(t, "current.cfg"), true)
	e.regenerated = readFixture(t, "current.cfg")
	RepairGrubCfg()
	if !e.updateGrubRan() || markerExists(t) {
		t.Fatalf("ran %q, marker kept %v", e.ran, markerExists(t))
	}
}

// A repair that does not fix grub.cfg keeps the marker: the next start retries.
func TestRepairGrubCfgKeepsMarkerOnFailure(t *testing.T) {
	for name, setup := range map[string]func(*grubRepairEnv){
		"still stale":        func(e *grubRepairEnv) { e.regenerated = readFixture(t, "stale.cfg") },
		"update-grub failed": func(e *grubRepairEnv) { e.results["update-grub"] = stubResult{err: errors.New("exit 1")} },
	} {
		t.Run(name, func(t *testing.T) {
			e := newGrubRepairEnv(t, readFixture(t, "stale.cfg"), true)
			setup(e)
			RepairGrubCfg()
			if !e.updateGrubRan() || !markerExists(t) {
				t.Fatalf("ran %q, marker kept %v", e.ran, markerExists(t))
			}
		})
	}
}

func TestRepairGrubCfgLeavesAlone(t *testing.T) {
	tests := []struct {
		name  string
		cfg   []byte
		setup func(*grubRepairEnv)
	}{
		{"up to date", readFixture(t, "current.cfg"), func(e *grubRepairEnv) { _ = os.Remove(grubRefreshMarker) }},
		{"no grub.cfg", nil, nil},
		{"overlay", readFixture(t, "stale.cfg"), func(e *grubRepairEnv) {
			e.results["findmnt --json /"] = stubResult{out: findmntRescue}
		}},
		{"findmnt fails", readFixture(t, "stale.cfg"), func(e *grubRepairEnv) {
			e.results["findmnt --json /"] = stubResult{err: errors.New("exit 1")}
		}},
		{"get-default fails", readFixture(t, "stale.cfg"), func(e *grubRepairEnv) {
			e.results["btrfs subvolume get-default /.snapshots"] = stubResult{err: errors.New("exit 1")}
		}},
		{"default is the top level", readFixture(t, "stale.cfg"), func(e *grubRepairEnv) {
			e.results["btrfs subvolume get-default /.snapshots"] = stubResult{out: "ID 5 (FS_TREE)\n"}
		}},
		{"root is not the default", readFixture(t, "stale.cfg"), func(e *grubRepairEnv) {
			e.results["findmnt --json /"] = stubResult{out: strings.Replace(findmnt40, "/40/", "/31/", 1)}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newGrubRepairEnv(t, tt.cfg, true)
			if tt.setup != nil {
				tt.setup(e)
			}
			RepairGrubCfg()
			if e.updateGrubRan() {
				t.Fatalf("update-grub ran: %q", e.ran)
			}
			if tt.name != "up to date" && !markerExists(t) {
				t.Fatal("marker removed without a repair")
			}
		})
	}
}
