package models

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/calaos/calaos-container/models/structs"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildSystemVersions(t *testing.T) {
	dir := t.TempDir()
	src := VersionSources{
		DebianVersion: writeFile(t, dir, "debian_version", "12.12\n"),
		KernelRelease: writeFile(t, dir, "osrelease", "6.12.48-amd64\n"),
	}
	installed := structs.ImageMap{
		"calaos-server":    {Name: "calaos-server", Version: "4.8.1"},
		"calaos-container": {Name: "calaos-container", Version: "1.9.1"},
		"calaos-haproxy":   {Name: "calaos-haproxy", Version: "2.0"},
		"calaos":           {Name: "calaos", Version: "2026.11"},
	}

	got := BuildSystemVersions(src, "2026.11.1\n", installed)

	if got.CalaosOS != "2026.11.1" || got.Debian != "12.12" || got.Kernel != "6.12.48-amd64" || got.Container != "1.9.1" {
		t.Fatalf("unexpected versions: %+v", got)
	}
	want := []string{"calaos-container", "calaos-haproxy", "calaos-server"}
	if len(got.Packages) != len(want) {
		t.Fatalf("packages: %+v", got.Packages)
	}
	for i, n := range want {
		if got.Packages[i].Name != n {
			t.Fatalf("packages not sorted: %+v", got.Packages)
		}
	}
}

func TestBuildSystemVersionsMissingSources(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	got := BuildSystemVersions(VersionSources{DebianVersion: missing, KernelRelease: missing}, "", structs.ImageMap{})

	if got.CalaosOS != "" || got.Debian != "" || got.Kernel != "" || got.Container != "" {
		t.Fatalf("missing sources must give empty fields: %+v", got)
	}
	if got.Packages == nil || len(got.Packages) != 0 {
		t.Fatalf("packages must be an empty list, got %#v", got.Packages)
	}
}

func TestCalaosOSVersion(t *testing.T) {
	for in, want := range map[string]string{
		"":              "",
		"1.2.3":         "",
		"12":            "",
		"12.12":         "",
		"2026.11":       "2026.11",
		"2026.11.1":     "2026.11.1",
		"2026.12~dev.1": "2026.12~dev.1",
		"2026.11\n":     "2026.11",
		"2026.11-1":     "",
		"2026.1":        "",
		"v2026.11":      "",
	} {
		if got := CalaosOSVersion(in); got != want {
			t.Errorf("CalaosOSVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
