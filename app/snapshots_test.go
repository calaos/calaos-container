package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const snapperCmd = "snapper --no-dbus -c root --jsonout list"

const snapperOut = `{"root":[
 {"number":0,"default":false,"active":false,"type":"single","pre-number":null,"date":"","cleanup":"","description":"current"},
 {"number":1,"default":true,"active":true,"type":"single","pre-number":null,"date":"2026-11-01 08:00:00","cleanup":"","description":"first root filesystem"},
 {"number":3,"default":false,"active":false,"type":"pre","pre-number":null,"date":"2026-11-17 10:00:00","cleanup":"number","description":"apt"},
 {"number":4,"default":false,"active":false,"type":"post","pre-number":3,"date":"2026-11-17 10:05:00","cleanup":"number","description":"apt"},
 {"number":5,"default":false,"active":false,"type":"single","pre-number":null,"date":"bogus","cleanup":"","description":"a > b"}
]}`

const grubCfgWithMenu = `### BEGIN /etc/grub.d/41_snapshots-btrfs ###
if [ ! -e "${prefix}/grub-btrfs.cfg" ]; then
echo ""
else
submenu 'Calaos-OS snapshots' {
    configfile "${prefix}/grub-btrfs.cfg"
}
fi
`

// Shaped like grub-btrfs 4.12 output; snapshot 4 is missing from the menu.
const grubBtrfsCfg = `submenu '| 2026-11-17 10:05:00 | @/.snapshots/5/snapshot | single | a > b |' {
    submenu '---> 2026-11-17 10:05:00 | @/.snapshots/5/snapshot | single | a > b <---' { echo }

    menuentry '  "vmlinuz-6.1.0-27-amd64" & "initrd.img-6.1.0-27-amd64"' --class snapshots --class gnu-linux "$menuentry_id_option" 'gnulinux-snapshots-uuid' {
        echo 'Loading Snapshot: "2026-11-17 10:05:00" "@/.snapshots/5/snapshot"'
        linux "/@/.snapshots/5/snapshot/boot/vmlinuz-6.1.0-27-amd64" root=UUID=x ro quiet rootflags=subvol="@/.snapshots/5/snapshot"
        initrd "/@/.snapshots/5/snapshot/boot/initrd.img-6.1.0-27-amd64"
    }
    menuentry '  "vmlinuz-6.1.0-26-amd64" & "initrd.img-6.1.0-26-amd64"' --class snapshots "$menuentry_id_option" 'gnulinux-snapshots-uuid' {
        linux "/@/.snapshots/5/snapshot/boot/vmlinuz-6.1.0-26-amd64" root=UUID=x ro rootflags=subvol="@/.snapshots/5/snapshot"
    }
}
submenu '| 2026-11-17 10:00:00 | @/.snapshots/3/snapshot | pre    | apt |' {
    submenu '---> 2026-11-17 10:00:00 | @/.snapshots/3/snapshot | pre | apt <---' { echo }

    menuentry '  "vmlinuz-6.1.0-27-amd64" & "initrd.img-6.1.0-27-amd64"' --class snapshots "$menuentry_id_option" 'gnulinux-snapshots-uuid' {
        linux "/@/.snapshots/3/snapshot/boot/vmlinuz-6.1.0-27-amd64" root=UUID=x ro rootflags=subvol="@/.snapshots/3/snapshot"
    }
}
`

const snap5Path = `Calaos-OS snapshots>| 2026-11-17 10:05:00 | @/.snapshots/5/snapshot | single | a >> b |>  "vmlinuz-6.1.0-27-amd64" & "initrd.img-6.1.0-27-amd64"`

const (
	findmntBtrfs  = `{"filesystems":[{"target":"/","source":"/dev/sda2[/@/.snapshots/1/snapshot]","fstype":"btrfs","options":"rw"}]}`
	findmntRescue = `{"filesystems":[{"target":"/","source":"rootfs","fstype":"overlay","options":"rw"}]}`
	cmdlineNormal = "BOOT_IMAGE=/@/.snapshots/1/snapshot/boot/vmlinuz root=UUID=x ro quiet\n"
	cmdlineSnap5  = "BOOT_IMAGE=/@/.snapshots/5/snapshot/boot/vmlinuz root=UUID=x ro rootflags=subvol=@/.snapshots/5/snapshot\n"
)

// snapEnv points the snapshot routes at files of a temporary directory and
// records the commands run and the deferred reboots queued.
type snapEnv struct {
	dir      string
	ran      []string
	deferred []func()
}

func (e *snapEnv) write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(e.dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// snapTree builds snapshot id's tree, with or without the overlay hook and
// the enabled one-shot unit; an existing tree is replaced.
func (e *snapEnv) snapTree(t *testing.T, id int, hook, unit bool) string {
	t.Helper()
	tree := filepath.Join(snapshotsRoot, fmt.Sprint(id), "snapshot")
	if err := os.RemoveAll(tree); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Dir(overlayHookRel), filepath.Dir(oneShotUnitRel)} {
		if err := os.MkdirAll(filepath.Join(tree, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if hook {
		if err := os.WriteFile(filepath.Join(tree, overlayHookRel), []byte("#!/bin/sh\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// An absolute link: it names the snapshot's own unit file, not the
	// running system's.
	if unit {
		unitDir := filepath.Join(tree, "lib/systemd/system")
		if err := os.MkdirAll(unitDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(unitDir, "calaos-snapshot-oneshot.service"), []byte("[Unit]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/lib/systemd/system/calaos-snapshot-oneshot.service", filepath.Join(tree, oneShotUnitRel)); err != nil {
			t.Fatal(err)
		}
	}
	return tree
}

func (e *snapEnv) fire() {
	for _, f := range e.deferred {
		f()
	}
	e.deferred = nil
}

// newSnapEnv installs s, then the snapshot fixtures: a normal (non-rescue)
// boot with the grub-btrfs menu present.
func newSnapEnv(t *testing.T, s *stubBackend) *snapEnv {
	t.Helper()
	e := &snapEnv{dir: t.TempDir()}
	s.install(t)
	stubRun := runCommand
	runCommand = func(command string, args ...string) (string, error) {
		e.ran = append(e.ran, strings.Join(append([]string{command}, args...), " "))
		return stubRun(command, args...)
	}

	prev := []string{procCmdlinePath, grubCfgPath, grubBtrfsCfgPath, grubEnvPath, espMountPath, espEnvPath, snapshotsRoot, snapperRootConfigPath, aptSnapperHookPath}
	prevLoc, prevAfter := snapLocation, snapAfterFunc
	t.Cleanup(func() {
		procCmdlinePath, grubCfgPath, grubBtrfsCfgPath = prev[0], prev[1], prev[2]
		grubEnvPath, espMountPath, espEnvPath, snapshotsRoot = prev[3], prev[4], prev[5], prev[6]
		snapperRootConfigPath, aptSnapperHookPath = prev[7], prev[8]
		snapLocation, snapAfterFunc = prevLoc, prevAfter
	})
	procCmdlinePath = e.write(t, "cmdline", cmdlineNormal)
	grubCfgPath = e.write(t, "grub.cfg", grubCfgWithMenu)
	grubBtrfsCfgPath = e.write(t, "grub-btrfs.cfg", grubBtrfsCfg)
	grubEnvPath = filepath.Join(e.dir, "grubenv")
	espMountPath = "/efi"
	espEnvPath = filepath.Join(e.dir, "esp", "calaos", "grubenv")
	snapshotsRoot = filepath.Join(e.dir, "snapshots")
	snapperRootConfigPath = e.write(t, "snapper-root", "NUMBER_LIMIT=\"10\"\n")
	aptSnapperHookPath = e.write(t, "80snapper", "// hook\n")
	for _, id := range []int{3, 5} {
		e.snapTree(t, id, true, true)
	}
	snapLocation = time.UTC
	snapAfterFunc = func(d time.Duration, f func()) {
		if d <= 0 {
			t.Errorf("reboot not deferred: %v", d)
		}
		e.deferred = append(e.deferred, f)
	}
	return e
}

func normalBackend(extra map[string]stubResult) *stubBackend {
	cmds := map[string]stubResult{
		"findmnt --json /": {out: findmntBtrfs},
		snapperCmd:         {out: snapperOut},
		"systemctl reboot": {},
	}
	for k, v := range extra {
		cmds[k] = v
	}
	return &stubBackend{commands: cmds}
}

func rescueBackend(extra map[string]stubResult) *stubBackend {
	s := normalBackend(extra)
	if _, set := extra["findmnt --json /"]; !set {
		s.commands["findmnt --json /"] = stubResult{out: findmntRescue}
	}
	return s
}

func snapDo(t *testing.T, tc httpCase) (int, v2Reply) {
	t.Helper()
	a := newHandlerTestApp(t)
	a.sessions = acceptSessions{ok: "live"}
	status, r, _ := v2Do2(t, a, tc)
	return status, r
}

func decodeList(t *testing.T, r v2Reply) snapshotList {
	t.Helper()
	var l snapshotList
	if err := json.Unmarshal(r.Data, &l); err != nil {
		t.Fatalf("data %s: %v", r.Data, err)
	}
	return l
}

func TestSnapshotsList(t *testing.T) {
	newSnapEnv(t, normalBackend(nil))
	status, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/snapshots"})
	if status != http.StatusOK {
		t.Fatalf("status %d %+v", status, r)
	}
	l := decodeList(t, r)

	var ids []int
	for _, s := range l.Snapshots {
		ids = append(ids, s.ID)
	}
	if fmt.Sprint(ids) != "[5 4 3 1]" {
		t.Fatalf("ids %v, want newest first without 0", ids)
	}
	if l.Booted == nil || l.Booted.ID != 1 || !l.Booted.Current || !l.Booted.Default {
		t.Fatalf("booted %+v", l.Booted)
	}
	byID := map[int]snapshotInfo{}
	for _, s := range l.Snapshots {
		byID[s.ID] = s
	}
	for id, want := range map[int]bool{5: true, 4: false, 3: true, 1: false} {
		if byID[id].Bootable != want {
			t.Errorf("snapshot %d bootable %v, want %v", id, byID[id].Bootable, want)
		}
	}
	if byID[3].Current || byID[3].Default {
		t.Errorf("snapshot 3 %+v", byID[3])
	}
	if byID[4].PreID == nil || *byID[4].PreID != 3 || byID[4].Type != "post" || byID[3].PreID != nil {
		t.Errorf("pre_id: 4=%+v 3=%+v", byID[4], byID[3])
	}
	if byID[3].Date != "2026-11-17T10:00:00Z" || byID[5].Date != "" {
		t.Errorf("dates %q %q", byID[3].Date, byID[5].Date)
	}
	if byID[5].Description != "a > b" || byID[3].Cleanup != "number" {
		t.Errorf("fields %+v %+v", byID[5], byID[3])
	}
}

func TestSnapshotsAptSnapshots(t *testing.T) {
	get := func(t *testing.T) snapshotList {
		status, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/snapshots"})
		if status != http.StatusOK {
			t.Fatalf("status %d %+v", status, r)
		}
		return decodeList(t, r)
	}
	t.Run("config and hook", func(t *testing.T) {
		newSnapEnv(t, normalBackend(nil))
		if !get(t).AptSnapshots {
			t.Fatal("apt_snapshots false with config and hook present")
		}
	})
	t.Run("hook missing", func(t *testing.T) {
		newSnapEnv(t, normalBackend(nil))
		aptSnapperHookPath = filepath.Join(t.TempDir(), "absent")
		if get(t).AptSnapshots {
			t.Fatal("apt_snapshots true without 80snapper")
		}
	})
	t.Run("config missing", func(t *testing.T) {
		newSnapEnv(t, normalBackend(nil))
		snapperRootConfigPath = filepath.Join(t.TempDir(), "absent")
		if get(t).AptSnapshots {
			t.Fatal("apt_snapshots true without the root config")
		}
	})
	t.Run("json field", func(t *testing.T) {
		newSnapEnv(t, normalBackend(nil))
		_, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/snapshots"})
		if !strings.Contains(string(r.Data), `"apt_snapshots":true`) {
			t.Fatalf("data %s", r.Data)
		}
	})
}

func TestSnapshotsListCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"root":[`)
	for i := 0; i <= 250; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"number":%d,"type":"single","date":"2026-11-17 10:00:00","active":%v}`, i, i == 1)
	}
	b.WriteString("]}")
	newSnapEnv(t, normalBackend(map[string]stubResult{snapperCmd: {out: b.String()}}))

	_, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/snapshots"})
	l := decodeList(t, r)
	if len(l.Snapshots) != maxSnapshotList || l.Snapshots[0].ID != 250 || l.Snapshots[maxSnapshotList-1].ID != 51 {
		t.Fatalf("%d snapshots, first %d", len(l.Snapshots), l.Snapshots[0].ID)
	}
	// The booted snapshot is reported even when the cap drops it.
	if l.Booted == nil || l.Booted.ID != 1 {
		t.Fatalf("booted %+v", l.Booted)
	}
}

func TestSnapshotsBootableNeedsMenu(t *testing.T) {
	for name, setup := range map[string]func(e *snapEnv, t *testing.T){
		"no submenu in grub.cfg": func(e *snapEnv, t *testing.T) { e.write(t, "grub.cfg", "menuentry 'Calaos-OS' {}\n") },
		"no grub-btrfs.cfg":      func(e *snapEnv, t *testing.T) { grubBtrfsCfgPath = filepath.Join(e.dir, "absent") },
		"no grub.cfg":            func(e *snapEnv, t *testing.T) { grubCfgPath = filepath.Join(e.dir, "absent") },
	} {
		t.Run(name, func(t *testing.T) {
			e := newSnapEnv(t, normalBackend(nil))
			setup(e, t)
			_, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/snapshots"})
			for _, s := range decodeList(t, r).Snapshots {
				if s.Bootable {
					t.Errorf("snapshot %d bootable", s.ID)
				}
			}
		})
	}
}

func TestSnapshotsListErrors(t *testing.T) {
	newSnapEnv(t, normalBackend(map[string]stubResult{snapperCmd: {err: errors.New("no config")}}))
	status, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/snapshots"})
	if status != http.StatusServiceUnavailable || r.Code != codeRescueUnavailable {
		t.Fatalf("got %d %+v", status, r)
	}

	newSnapEnv(t, normalBackend(map[string]stubResult{snapperCmd: {out: "not json"}}))
	if status, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/snapshots"}); status != http.StatusServiceUnavailable {
		t.Fatalf("bad json: %d %+v", status, r)
	}
}

func TestSnapshotsListInRescue(t *testing.T) {
	e := newSnapEnv(t, rescueBackend(nil))
	e.write(t, "cmdline", cmdlineSnap5)
	_, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/snapshots"})
	l := decodeList(t, r)
	if l.Booted == nil || l.Booted.ID != 5 {
		t.Fatalf("booted %+v", l.Booted)
	}
	for _, s := range l.Snapshots {
		if s.Bootable || s.Current != (s.ID == 5) {
			t.Errorf("snapshot %+v", s)
		}
	}
}

func TestSnapshotsRescueStatus(t *testing.T) {
	cases := []struct {
		name, findmnt, cmdline, want string
	}{
		{"normal", findmntBtrfs, cmdlineNormal, `{"rescue":false,"snapshot":null,"overlay":false}`},
		{"rescue", findmntRescue, cmdlineSnap5, `{"rescue":true,"snapshot":5,"overlay":true}`},
		{"snapshot without overlay", findmntBtrfs, cmdlineSnap5, `{"rescue":false,"snapshot":5,"overlay":false}`},
		{"foreign overlay", `{"filesystems":[{"source":"overlay","fstype":"overlay"}]}`, cmdlineNormal, `{"rescue":false,"snapshot":null,"overlay":true}`},
		{"subvol outside rootflags", findmntRescue, "root=x ro subvol=@/.snapshots/7/snapshot\n", `{"rescue":true,"snapshot":null,"overlay":true}`},
		{"rootflags with other options", findmntRescue, "rootflags=compress=zstd,subvol=@/.snapshots/12/snapshot ro\n", `{"rescue":true,"snapshot":12,"overlay":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newSnapEnv(t, normalBackend(map[string]stubResult{"findmnt --json /": {out: tc.findmnt}}))
			e.write(t, "cmdline", tc.cmdline)
			status, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/rescue"})
			if status != http.StatusOK || string(r.Data) != tc.want {
				t.Fatalf("got %d %s, want %s", status, r.Data, tc.want)
			}
		})
	}

	newSnapEnv(t, normalBackend(map[string]stubResult{"findmnt --json /": {err: errors.New("boom")}}))
	if status, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/rescue"}); status != http.StatusInternalServerError || r.Code != codeInternal {
		t.Fatalf("findmnt failure: %d %+v", status, r)
	}
}

func bootCase(body string) httpCase {
	return httpCase{method: "POST", path: "/api/v2/system/snapshots/boot", ctype: "application/json", body: body,
		headers: map[string]string{headerAdminSession: "live"}}
}

func TestSnapshotBootRefusals(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		rescue bool
		setup  func(e *snapEnv, t *testing.T)
		status int
		code   string
	}{
		{name: "unknown id", body: `{"id":99}`, status: 400, code: codeInvalidSnapshot},
		{name: "snapshot 0", body: `{"id":0}`, status: 400, code: codeInvalidSnapshot},
		{name: "negative", body: `{"id":-3}`, status: 400, code: codeInvalidSnapshot},
		{name: "missing id", body: `{}`, status: 400, code: codeInvalidSnapshot},
		{name: "string id", body: `{"id":"5"}`, status: 400, code: codeInvalidSnapshot},
		{name: "float id", body: `{"id":5.5}`, status: 400, code: codeInvalidSnapshot},
		{name: "shell in id", body: `{"id":"5; reboot"}`, status: 400, code: codeInvalidSnapshot},
		{name: "not json", body: `id=5`, status: 400, code: codeInvalidRequest},
		{name: "array body", body: `[]`, status: 400, code: codeInvalidRequest},
		{name: "number body", body: `5`, status: 400, code: codeInvalidRequest},
		{name: "string body", body: `"x"`, status: 400, code: codeInvalidRequest},
		{name: "not bootable", body: `{"id":4}`, status: 409, code: codeNotBootable},
		{name: "from rescue", body: `{"id":5}`, rescue: true, status: 409, code: codeNotBootable},
		{name: "no overlay hook in the snapshot", body: `{"id":5}`, status: 409, code: codeSnapshotTooOld,
			setup: func(e *snapEnv, t *testing.T) { e.snapTree(t, 5, false, true) }},
		{name: "pre-fix hook directory", body: `{"id":5}`, status: 409, code: codeSnapshotTooOld,
			setup: func(e *snapEnv, t *testing.T) {
				tree := e.snapTree(t, 5, false, true)
				if err := os.MkdirAll(filepath.Join(tree, overlayHookRel, "grub-btrfs-overlay"), 0o750); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "no one-shot unit in the snapshot", body: `{"id":5}`, status: 409, code: codeSnapshotTooOld,
			setup: func(e *snapEnv, t *testing.T) { e.snapTree(t, 5, true, false) }},
		{name: "no snapshot tree", body: `{"id":5}`, status: 409, code: codeSnapshotTooOld,
			setup: func(e *snapEnv, t *testing.T) { snapshotsRoot = filepath.Join(e.dir, "absent") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := normalBackend(nil)
			if tc.rescue {
				s = rescueBackend(nil)
			}
			e := newSnapEnv(t, s)
			if tc.setup != nil {
				tc.setup(e, t)
			}
			status, r := snapDo(t, bootCase(tc.body))
			if status != tc.status || r.Code != tc.code {
				t.Fatalf("got %d %+v, want %d %s", status, r, tc.status, tc.code)
			}
			for _, c := range e.ran {
				if strings.HasPrefix(c, "grub-reboot") || strings.HasPrefix(c, "grub-editenv") || c == "systemctl reboot" {
					t.Fatalf("ran %q", c)
				}
			}
			if len(e.deferred) != 0 {
				t.Fatal("reboot queued")
			}
		})
	}
}

func TestSnapshotBootUpgradeInProgress(t *testing.T) {
	s := normalBackend(map[string]stubResult{"grub-reboot " + snap5Path: {}})
	s.rebootBlocked = true
	e := newSnapEnv(t, s)
	status, r := snapDo(t, bootCase(`{"id":5}`))
	if status != http.StatusConflict || r.Code != codeUpgradeInProgress {
		t.Fatalf("got %d %+v", status, r)
	}
	for _, c := range e.ran {
		if strings.HasPrefix(c, "grub-reboot") {
			t.Fatalf("grub-reboot ran behind a refused guard")
		}
	}
	if len(e.deferred) != 0 {
		t.Fatal("reboot queued")
	}
}

func TestSnapshotBootSuccess(t *testing.T) {
	e := newSnapEnv(t, normalBackend(map[string]stubResult{"grub-reboot " + snap5Path: {}}))
	guards := 0
	inner := rebootGuard
	rebootGuard = func(fn func() error) error {
		guards++
		return inner(fn)
	}

	status, r := snapDo(t, bootCase(`{"id":5}`))
	if status != http.StatusOK || string(r.Data) != "null" || guards != 1 {
		t.Fatalf("got %d %+v, %d guards", status, r, guards)
	}
	if last := e.ran[len(e.ran)-1]; last != "grub-reboot "+snap5Path {
		t.Fatalf("last command %q", last)
	}
	if len(e.deferred) != 1 {
		t.Fatalf("%d deferred reboots", len(e.deferred))
	}
	e.fire()
	if last := e.ran[len(e.ran)-1]; last != "systemctl reboot" {
		t.Fatalf("deferred reboot ran %q", last)
	}
}

func TestSnapshotBootGrubRebootFails(t *testing.T) {
	e := newSnapEnv(t, normalBackend(map[string]stubResult{"grub-reboot " + snap5Path: {err: errors.New("grubenv")}}))
	status, r := snapDo(t, bootCase(`{"id":5}`))
	if status != http.StatusInternalServerError || r.Code != codeInternal || len(e.deferred) != 0 {
		t.Fatalf("got %d %+v, %d deferred", status, r, len(e.deferred))
	}
}

// A reboot that fails after grub-reboot must not leave next_entry behind for
// the next manual reboot.
func TestSnapshotBootDeferredRebootFails(t *testing.T) {
	s := normalBackend(map[string]stubResult{
		"grub-reboot " + snap5Path: {},
		"systemctl reboot":         {err: errors.New("logind")},
	})
	e := newSnapEnv(t, s)
	unset := "grub-editenv " + grubEnvPath + " unset next_entry"
	s.commands[unset] = stubResult{}
	if status, r := snapDo(t, bootCase(`{"id":5}`)); status != http.StatusOK {
		t.Fatalf("got %d %+v", status, r)
	}
	e.fire()
	if last := e.ran[len(e.ran)-1]; last != unset {
		t.Fatalf("after a failed reboot ran %v", e.ran)
	}
}

func rollbackCase() httpCase {
	return httpCase{method: "POST", path: "/api/v2/system/snapshots/rollback",
		headers: map[string]string{headerAdminSession: "live"}}
}

func TestSnapshotRollback(t *testing.T) {
	t.Run("not in rescue", func(t *testing.T) {
		e := newSnapEnv(t, normalBackend(map[string]stubResult{"calaos_rollback": {}}))
		e.write(t, "cmdline", cmdlineSnap5)
		status, r := snapDo(t, rollbackCase())
		if status != http.StatusConflict || r.Code != codeNotInRescue || len(e.deferred) != 0 {
			t.Fatalf("got %d %+v", status, r)
		}
	})
	t.Run("overlay without snapshot", func(t *testing.T) {
		e := newSnapEnv(t, rescueBackend(map[string]stubResult{"calaos_rollback": {}}))
		status, r := snapDo(t, rollbackCase())
		if status != http.StatusConflict || r.Code != codeNotInRescue || len(e.ran) != 1 {
			t.Fatalf("got %d %+v, ran %v", status, r, e.ran)
		}
	})
	t.Run("upgrade in progress", func(t *testing.T) {
		s := rescueBackend(map[string]stubResult{"calaos_rollback": {}})
		s.rebootBlocked = true
		e := newSnapEnv(t, s)
		e.write(t, "cmdline", cmdlineSnap5)
		status, r := snapDo(t, rollbackCase())
		if status != http.StatusConflict || r.Code != codeUpgradeInProgress {
			t.Fatalf("got %d %+v", status, r)
		}
		if strings.Contains(strings.Join(e.ran, ";"), "calaos_rollback") || len(e.deferred) != 0 {
			t.Fatalf("ran %v", e.ran)
		}
	})
	t.Run("rollback fails", func(t *testing.T) {
		e := newSnapEnv(t, rescueBackend(map[string]stubResult{"calaos_rollback": {err: errors.New("snapper")}}))
		e.write(t, "cmdline", cmdlineSnap5)
		status, r := snapDo(t, rollbackCase())
		if status != http.StatusInternalServerError || len(e.deferred) != 0 {
			t.Fatalf("got %d %+v", status, r)
		}
	})
	t.Run("success", func(t *testing.T) {
		e := newSnapEnv(t, rescueBackend(map[string]stubResult{"calaos_rollback": {}}))
		e.write(t, "cmdline", cmdlineSnap5)
		status, r := snapDo(t, rollbackCase())
		if status != http.StatusOK || string(r.Data) != "null" || len(e.deferred) != 1 {
			t.Fatalf("got %d %+v, %d deferred", status, r, len(e.deferred))
		}
		if strings.Contains(strings.Join(e.ran, ";"), "systemctl reboot") {
			t.Fatal("reboot before the response")
		}
		e.fire()
		if got := strings.Join(e.ran, ";"); !strings.HasSuffix(got, "calaos_rollback;systemctl reboot") {
			t.Fatalf("ran %v", got)
		}
	})
}

func TestSnapshotRoutesAuth(t *testing.T) {
	routes := []struct {
		tc      httpCase
		backend func() *stubBackend
		cmdline string
		// sessionOnly routes refuse the bearer alone: calaos_server relays
		// them, and the daemon is where the admin session is enforced.
		sessionOnly bool
	}{
		{httpCase{method: "GET", path: "/api/v2/system/snapshots"}, func() *stubBackend { return normalBackend(nil) }, cmdlineNormal, false},
		{httpCase{method: "GET", path: "/api/v2/system/rescue"}, func() *stubBackend { return normalBackend(nil) }, cmdlineNormal, false},
		{bootCase(`{"id":5}`), func() *stubBackend { return normalBackend(map[string]stubResult{"grub-reboot " + snap5Path: {}}) }, cmdlineNormal, true},
		{rollbackCase(), func() *stubBackend { return rescueBackend(map[string]stubResult{"calaos_rollback": {}}) }, cmdlineSnap5, true},
	}
	for _, rt := range routes {
		for _, ac := range []struct {
			name, auth, session string
			status              int
			code                string
		}{
			{"no bearer", "-", "", 401, codeBadBearer},
			{"bad bearer", "Bearer nope", "", 401, codeBadBearer},
			{"bad session", "", "stale", 401, codeBadSession},
			{"live session", "", "live", 200, ""},
			{"bearer alone", "", "", 200, ""},
		} {
			if ac.name == "bearer alone" && rt.sessionOnly {
				ac.status, ac.code = 401, codeBadSession
			}
			t.Run(rt.tc.path+" "+ac.name, func(t *testing.T) {
				e := newSnapEnv(t, rt.backend())
				e.write(t, "cmdline", rt.cmdline)
				tc := rt.tc
				tc.auth = ac.auth
				tc.headers = nil
				if ac.session != "" {
					tc.headers = map[string]string{headerAdminSession: ac.session}
				}
				status, r := snapDo(t, tc)
				if status != ac.status || r.Code != ac.code {
					t.Fatalf("got %d %+v, want %d %s", status, r, ac.status, ac.code)
				}
				if ac.status != 200 && (len(e.ran) != 0 || len(e.deferred) != 0) {
					t.Fatalf("refused call ran %v", e.ran)
				}
			})
		}
	}
}

func TestGrubPathEscapes(t *testing.T) {
	if got := grubPath("a>b", "c", ">>"); got != "a>>b>c>>>>>" {
		t.Fatalf("got %q", got)
	}
}

func TestSnapshotsListTooOld(t *testing.T) {
	e := newSnapEnv(t, normalBackend(nil))
	e.snapTree(t, 3, true, false)
	_, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/snapshots"})
	for _, s := range decodeList(t, r).Snapshots {
		if s.Bootable != (s.ID == 5) {
			t.Errorf("snapshot %d bootable %v", s.ID, s.Bootable)
		}
	}
}

const grubCfgESP = grubCfgWithMenu + `### BEGIN /etc/grub.d/41_calaos-snapshot-boot ###
load_env --file "(${calaos_esp})/calaos/grubenv" calaos_oneshot
`

const findmntESP = "findmnt -n -o FSTYPE /efi"

// With the ESP snippet in grub.cfg and a FAT /efi, GRUB clears the one-shot
// itself: no next_entry, and no unit needed in the snapshot.
func TestSnapshotBootESP(t *testing.T) {
	s := normalBackend(map[string]stubResult{findmntESP: {out: "vfat\n"}, "systemctl reboot": {err: errors.New("logind")}})
	e := newSnapEnv(t, s)
	e.write(t, "grub.cfg", grubCfgESP)
	e.snapTree(t, 5, true, false)
	set := "grub-editenv " + espEnvPath + " set calaos_oneshot=" + snap5Path
	unset := "grub-editenv " + espEnvPath + " unset calaos_oneshot"
	s.commands[set] = stubResult{}
	s.commands[unset] = stubResult{}

	if status, r := snapDo(t, bootCase(`{"id":5}`)); status != http.StatusOK {
		t.Fatalf("got %d %+v", status, r)
	}
	if last := e.ran[len(e.ran)-1]; last != set {
		t.Fatalf("ran %v", e.ran)
	}
	if fi, err := os.Stat(filepath.Dir(espEnvPath)); err != nil || !fi.IsDir() {
		t.Fatalf("ESP directory not created: %v", err)
	}
	e.fire()
	if last := e.ran[len(e.ran)-1]; last != unset {
		t.Fatalf("after a failed reboot ran %v", e.ran)
	}
	for _, c := range e.ran {
		if strings.HasPrefix(c, "grub-reboot") || strings.Contains(c, "next_entry") {
			t.Fatalf("ran %q in ESP mode", c)
		}
	}

	// The overlay hook is still needed.
	e.snapTree(t, 5, false, false)
	if status, r := snapDo(t, bootCase(`{"id":5}`)); status != http.StatusConflict || r.Code != codeSnapshotTooOld {
		t.Fatalf("no hook: %d %+v", status, r)
	}
}

func TestSnapshotBootModeFallback(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg     string
		findmnt *stubResult
	}{
		"ESP not FAT":        {grubCfgESP, &stubResult{out: "btrfs\n"}},
		"ESP not mounted":    {grubCfgESP, &stubResult{err: errors.New("exit 1")}},
		"grub.cfg not fresh": {grubCfgWithMenu, nil},
	} {
		t.Run(name, func(t *testing.T) {
			s := normalBackend(map[string]stubResult{"grub-reboot " + snap5Path: {}})
			if tc.findmnt != nil {
				s.commands[findmntESP] = *tc.findmnt
			}
			e := newSnapEnv(t, s)
			e.write(t, "grub.cfg", tc.cfg)
			if status, r := snapDo(t, bootCase(`{"id":5}`)); status != http.StatusOK {
				t.Fatalf("got %d %+v", status, r)
			}
			if last := e.ran[len(e.ran)-1]; last != "grub-reboot "+snap5Path {
				t.Fatalf("ran %v", e.ran)
			}
			if tc.findmnt == nil && strings.Contains(strings.Join(e.ran, ";"), findmntESP) {
				t.Fatal("probed the ESP without the snippet")
			}

			// next_entry relies on the unit of the snapshot.
			e.snapTree(t, 5, true, false)
			if status, r := snapDo(t, bootCase(`{"id":5}`)); status != http.StatusConflict || r.Code != codeSnapshotTooOld {
				t.Fatalf("no unit: %d %+v", status, r)
			}
		})
	}
}

func TestClearBootOneShot(t *testing.T) {
	const header = "# GRUB Environment Block\n"
	cases := []struct {
		name, grubenv, espenv string
		want                  []string
	}{
		{"both armed", header + "saved_entry=0\nnext_entry=Calaos-OS snapshots>x\n", header + "calaos_oneshot=y\n",
			[]string{"grub-editenv GRUBENV unset next_entry", "grub-editenv ESPENV unset calaos_oneshot"}},
		{"already cleared", header + "next_entry=\n", header, nil},
		{"next_entry prefix only", header + "next_entry_x=1\n", "", nil},
		{"no files", "", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := normalBackend(nil)
			e := newSnapEnv(t, s)
			for _, f := range []struct{ path, content string }{{grubEnvPath, tc.grubenv}, {espEnvPath, tc.espenv}} {
				if f.content == "" {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(f.path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.path, []byte(f.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var want []string
			for _, w := range tc.want {
				w = strings.NewReplacer("GRUBENV", grubEnvPath, "ESPENV", espEnvPath).Replace(w)
				s.commands[w] = stubResult{}
				want = append(want, w)
			}
			ClearBootOneShot()
			if fmt.Sprint(e.ran) != fmt.Sprint(want) {
				t.Fatalf("ran %v, want %v", e.ran, want)
			}
		})
	}
}

// From a snapshot, packages would land in the overlay and update-grub would
// rewrite the shared grub.cfg: every upgrade route refuses before any job.
func TestUpgradeRefusedInRescue(t *testing.T) {
	routes := []struct {
		tc     httpCase
		legacy bool
	}{
		{httpCase{method: "POST", path: "/api/v2/update/upgrade-all"}, false},
		{httpCase{method: "POST", path: "/api/v2/update/upgrade", ctype: "application/json", body: `{"package":"calaos-server"}`}, false},
		{httpCase{method: "POST", path: "/api/update/upgrade-all"}, true},
		{httpCase{method: "POST", path: "/api/update/upgrade?package=calaos-server"}, true},
	}
	for _, rt := range routes {
		for name, findmnt := range map[string]stubResult{
			"rescue":          {out: findmntRescue},
			"normal":          {out: findmntBtrfs},
			"findmnt failure": {err: errors.New("boom")},
			"foreign overlay": {out: `{"filesystems":[{"source":"overlay","fstype":"overlay"}]}`},
		} {
			t.Run(rt.tc.path+" "+name, func(t *testing.T) {
				newSnapEnv(t, normalBackend(map[string]stubResult{"findmnt --json /": findmnt}))
				started := 0
				inner := startUpgradeJob
				startUpgradeJob = func(kind, pkg string) (upgradeRun, error) {
					started++
					return inner(kind, pkg)
				}
				a := newHandlerTestApp(t)
				resp, body := doCase(t, a, rt.tc)
				refused := name == "rescue"
				if refused != (started == 0) {
					t.Fatalf("%d jobs started, status %d %s", started, resp.StatusCode, body)
				}
				switch {
				case !refused:
					if resp.StatusCode >= 300 {
						t.Fatalf("status %d %s", resp.StatusCode, body)
					}
				case rt.legacy:
					if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), errUpgradeInRescue.Error()) {
						t.Fatalf("legacy refusal %d %s", resp.StatusCode, body)
					}
				default:
					var r v2Reply
					if err := json.Unmarshal(body, &r); err != nil || resp.StatusCode != http.StatusConflict || r.Code != codeInRescue {
						t.Fatalf("v2 refusal %d %s", resp.StatusCode, body)
					}
				}
			})
		}
	}
}

// A link in the middle of the path must be read inside the snapshot, never on
// the running system.
func TestSnapshotTooOldTreeLinks(t *testing.T) {
	link := func(t *testing.T, oldname, newname string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(newname), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(oldname, newname); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		build   func(t *testing.T, e *snapEnv, tree, live string)
		tooOld  bool
		esp     bool
		comment string
	}{
		{name: "usr links to the live root", tooOld: true, esp: true,
			build: func(t *testing.T, e *snapEnv, tree, live string) {
				hook := filepath.Join(live, overlayHookRel)
				if err := os.MkdirAll(filepath.Dir(hook), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(filepath.Join(tree, "usr")); err != nil {
					t.Fatal(err)
				}
				link(t, filepath.Join(live, "usr"), filepath.Join(tree, "usr"))
			}},
		{name: "merged-usr link stays in the tree", tooOld: false,
			build: func(t *testing.T, e *snapEnv, tree, live string) {
				// lib -> usr/lib, as on Debian, with the unit under usr/lib.
				if err := os.RemoveAll(filepath.Join(tree, "lib")); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(tree, "usr/lib/systemd/system"), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(tree, "usr/lib/systemd/system/calaos-snapshot-oneshot.service"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				link(t, "usr/lib", filepath.Join(tree, "lib"))
			}},
		{name: "unit link dangling", tooOld: true,
			build: func(t *testing.T, e *snapEnv, tree, live string) {
				if err := os.Remove(filepath.Join(tree, "lib/systemd/system/calaos-snapshot-oneshot.service")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "unit link dangling is fine on the ESP", tooOld: false, esp: true,
			build: func(t *testing.T, e *snapEnv, tree, live string) {
				if err := os.Remove(filepath.Join(tree, "lib/systemd/system/calaos-snapshot-oneshot.service")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "unit link climbs above the tree", tooOld: true,
			build: func(t *testing.T, e *snapEnv, tree, live string) {
				u := filepath.Join(tree, oneShotUnitRel)
				if err := os.Remove(u); err != nil {
					t.Fatal(err)
				}
				link(t, "../../../../../../../../../../etc/hostname", u)
			}},
		{name: "link loop", tooOld: true,
			build: func(t *testing.T, e *snapEnv, tree, live string) {
				u := filepath.Join(tree, oneShotUnitRel)
				if err := os.Remove(u); err != nil {
					t.Fatal(err)
				}
				link(t, filepath.Base(u), u)
			}},
		{name: "hook itself a link", tooOld: true, esp: true,
			build: func(t *testing.T, e *snapEnv, tree, live string) {
				h := filepath.Join(tree, overlayHookRel)
				if err := os.Rename(h, h+".real"); err != nil {
					t.Fatal(err)
				}
				link(t, filepath.Base(h)+".real", h)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newSnapEnv(t, normalBackend(nil))
			tree := e.snapTree(t, 5, true, true)
			tc.build(t, e, tree, filepath.Join(e.dir, "live"))
			if got := snapshotTooOld(5, tc.esp); got != tc.tooOld {
				t.Fatalf("snapshotTooOld = %v, want %v", got, tc.tooOld)
			}
		})
	}
}

func TestSnapshotProbesAreBounded(t *testing.T) {
	prev := queryTimeout
	queryTimeout = 20 * time.Millisecond
	t.Cleanup(func() { queryTimeout = prev })
	s := normalBackend(nil)
	newSnapEnv(t, s)
	var hadDeadline bool
	runCommandCtx = func(ctx context.Context, command string, args ...string) (string, error) {
		_, hadDeadline = ctx.Deadline()
		<-ctx.Done()
		return "", ctx.Err()
	}
	status, r := snapDo(t, httpCase{method: "GET", path: "/api/v2/system/snapshots"})
	if !hadDeadline || status != 500 {
		t.Fatalf("deadline %v, got %d %+v", hadDeadline, status, r)
	}
}

// An id snapper lists but the 200-entry cap drops is not "unknown".
func TestSnapshotBootIDBeyondCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"root":[`)
	for i := 0; i <= 250; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"number":%d,"type":"single","date":"2026-11-17 10:00:00"}`, i)
	}
	b.WriteString("]}")
	newSnapEnv(t, normalBackend(map[string]stubResult{snapperCmd: {out: b.String()}}))
	status, r := snapDo(t, bootCase(`{"id":1}`))
	if status != 409 || r.Code != codeNotBootable {
		t.Fatalf("got %d %+v", status, r)
	}
}
