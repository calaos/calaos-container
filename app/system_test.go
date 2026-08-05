package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// canned lsblk-style inventory modeling a typical target machine:
//
//	/dev/sda   - whole disk, unpartitioned, unused                (valid target)
//	/dev/sdb   - whole disk, single partition sdb1 mounted at "/" (the root disk, via child)
//	  /dev/sdb1  - partition of sdb, mounted at "/"
//	/dev/sdc   - whole disk, mounted at "/" directly, no partition table (whole-disk root, no child)
//	/dev/sdd   - whole disk, swap                                  (valid target)
//	/dev/sr0   - CD-ROM drive                                      (type=rom, rejected)
//	/dev/loop0 - loop device                                       (type=loop, rejected)
func cannedInventory() []lsblkDevice {
	root := "/"
	swap := "[SWAP]"
	return []lsblkDevice{
		{Name: "/dev/sda", Path: "/dev/sda", Type: "disk"},
		{
			Name: "/dev/sdb", Path: "/dev/sdb", Type: "disk",
			Children: []lsblkDevice{
				{Name: "/dev/sdb1", Path: "/dev/sdb1", Type: "part", Mountpoint: &root, Mountpoints: []*string{&root}},
			},
		},
		{Name: "/dev/sdc", Path: "/dev/sdc", Type: "disk", Mountpoint: &root, Mountpoints: []*string{&root}},
		{Name: "/dev/sdd", Path: "/dev/sdd", Type: "disk", Mountpoint: &swap, Mountpoints: []*string{&swap}},
		{Name: "/dev/sr0", Path: "/dev/sr0", Type: "rom"},
		{Name: "/dev/loop0", Path: "/dev/loop0", Type: "loop"},
	}
}

func TestValidateInstallDevice(t *testing.T) {
	devices := cannedInventory()

	tests := []struct {
		name    string
		opts    InstallOpts
		wantErr bool
		errSub  string
	}{
		{
			name: "valid whole disk with confirm passes",
			opts: InstallOpts{Device: "/dev/sda", Confirm: true},
		},
		{
			name: "valid whole disk (swap) with confirm passes",
			opts: InstallOpts{Device: "/dev/sdd", Confirm: true},
		},
		{
			name:    "empty device rejected",
			opts:    InstallOpts{Device: "", Confirm: true},
			wantErr: true,
			errSub:  "device arg not set",
		},
		{
			name:    "unknown device rejected",
			opts:    InstallOpts{Device: "/dev/sdz", Confirm: true},
			wantErr: true,
			errSub:  "unknown device",
		},
		{
			name:    "partition rejected",
			opts:    InstallOpts{Device: "/dev/sdb1", Confirm: true},
			wantErr: true,
			errSub:  "not a whole disk",
		},
		{
			name:    "cdrom (type=rom) rejected",
			opts:    InstallOpts{Device: "/dev/sr0", Confirm: true},
			wantErr: true,
			errSub:  "not a whole disk",
		},
		{
			name:    "loop device rejected",
			opts:    InstallOpts{Device: "/dev/loop0", Confirm: true},
			wantErr: true,
			errSub:  "not a whole disk",
		},
		{
			name:    "root disk rejected (root fs on child partition)",
			opts:    InstallOpts{Device: "/dev/sdb", Confirm: true},
			wantErr: true,
			errSub:  "hosts the root filesystem",
		},
		{
			name:    "root disk rejected (whole-disk root, no partition table)",
			opts:    InstallOpts{Device: "/dev/sdc", Confirm: true},
			wantErr: true,
			errSub:  "hosts the root filesystem",
		},
		{
			name:    "missing confirm rejected on otherwise-valid device",
			opts:    InstallOpts{Device: "/dev/sda", Confirm: false},
			wantErr: true,
			errSub:  "confirm",
		},
		{
			name:    "confirm false explicit rejected",
			opts:    InstallOpts{Device: "/dev/sdd", Confirm: false},
			wantErr: true,
			errSub:  "confirm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInstallDevice(tt.opts, devices)

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.errSub)
			}
			if !strings.Contains(err.Error(), tt.errSub) {
				t.Fatalf("expected error to contain %q, got %q", tt.errSub, err.Error())
			}
		})
	}
}

// TestParseLsblkInventoryJSON guards the assumption the validation logic
// relies on: that lsblk --json --paths --output-all produces the field
// names ("name", "path", "type", "mountpoint", "mountpoints", "children")
// this package's lsblkDevice struct expects, nested by parent/child so a
// partition's root disk can be found by walking down from each top-level
// disk rather than needing to resolve PKNAME chains.
func TestParseLsblkInventoryJSON(t *testing.T) {
	const sample = `{
	  "blockdevices": [
	    {"name":"/dev/sda","path":"/dev/sda","type":"disk","mountpoint":null,"mountpoints":[null],
	     "children":[
	        {"name":"/dev/sda1","path":"/dev/sda1","type":"part","mountpoint":"/boot","mountpoints":["/boot"]},
	        {"name":"/dev/sda2","path":"/dev/sda2","type":"part","mountpoint":"/","mountpoints":["/"]}
	     ]
	    },
	    {"name":"/dev/sdb","path":"/dev/sdb","type":"disk","mountpoint":null,"mountpoints":[null]},
	    {"name":"/dev/loop0","path":"/dev/loop0","type":"loop","mountpoint":"/snap/core/1","mountpoints":["/snap/core/1"]}
	  ]
	}`

	var inv lsblkInventory
	if err := json.Unmarshal([]byte(sample), &inv); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(inv.BlockDevices) != 3 {
		t.Fatalf("expected 3 top-level devices, got %d", len(inv.BlockDevices))
	}

	sda := findLsblkDevice(inv.BlockDevices, "/dev/sda")
	if sda == nil || sda.Type != "disk" {
		t.Fatalf("expected to find /dev/sda as disk, got %+v", sda)
	}
	if !deviceHostsMountpoint(*sda, "/") {
		t.Fatalf("expected /dev/sda to host root via child /dev/sda2")
	}

	sdb := findLsblkDevice(inv.BlockDevices, "/dev/sdb")
	if sdb == nil {
		t.Fatalf("expected to find /dev/sdb")
	}
	if deviceHostsMountpoint(*sdb, "/") {
		t.Fatalf("expected /dev/sdb to not host root")
	}

	part := findLsblkDevice(inv.BlockDevices, "/dev/sda2")
	if part == nil || part.Type != "part" {
		t.Fatalf("expected to find /dev/sda2 as partition, got %+v", part)
	}

	if findLsblkDevice(inv.BlockDevices, "/dev/nonexistent") != nil {
		t.Fatalf("expected /dev/nonexistent to not be found")
	}
}
