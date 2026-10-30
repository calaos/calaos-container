package models

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/calaos/calaos-container/sysbus"
	"github.com/calaos/calaos-container/sysbus/sysbustest"
)

// unitLog keeps the unit names of every read, which the fake only counts.
type unitLog struct {
	*sysbustest.Bus
	mu    sync.Mutex
	units []string
}

func (u *unitLog) UnitProperties(ctx context.Context, units ...string) ([]sysbus.UnitProperties, error) {
	u.mu.Lock()
	u.units = append(u.units, units...)
	u.mu.Unlock()
	return u.Bus.UnitProperties(ctx, units...)
}

// tableUnits is every unit name the static table may put on the bus.
func tableUnits() map[string]bool {
	allowed := map[string]bool{}
	for _, s := range Services {
		for _, u := range UnitCandidates(s.Unit) {
			allowed[u] = true
		}
	}
	return allowed
}

func TestUnitNamesOnTheBusComeFromTheTable(t *testing.T) {
	allowed := tableUnits()
	// Each side of the ntp fallback gets its turn at being the installed one.
	for _, ntp := range []string{"chrony.service", "systemd-timesyncd.service"} {
		t.Run(ntp, func(t *testing.T) {
			ctx := context.Background()
			f := &unitLog{Bus: sysbustest.New()}
			for u := range allowed {
				f.SetUnit(u, loaded)
			}
			for _, c := range UnitCandidates("chrony.service") {
				if c != ntp {
					f.SetUnit(c, notFound)
				}
			}

			if _, err := ServiceStates(ctx, f); err != nil {
				t.Fatal(err)
			}
			for _, s := range Services {
				if !s.Allows(ActionRestart) {
					continue
				}
				unit, err := ResolveUnit(ctx, f, s.Unit)
				if err != nil {
					t.Fatal(err)
				}
				job, err := QueueRestart(ctx, f, s, unit)
				if err != nil {
					t.Fatalf("%s: %v", s.ID, err)
				}
				if err := job.Wait(ctx); err != nil {
					t.Fatalf("%s: %v", s.ID, err)
				}
			}

			for _, u := range f.units {
				if !allowed[u] {
					t.Fatalf("read of %q, not in the table", u)
				}
			}
			calls := f.Calls()
			if len(calls) == 0 {
				t.Fatal("no restart reached the bus")
			}
			for _, c := range calls {
				verb, unit, ok := strings.Cut(c, " ")
				if !ok || !strings.HasSuffix(verb, "Unit") || !allowed[unit] {
					t.Fatalf("call %q, unit not in the table", c)
				}
			}
			if !contains(calls, "RestartUnit "+ntp) {
				t.Fatalf("ntp restart went to %q", calls)
			}
		})
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// A unit that cannot be restarted is refused on the read, before any mutation,
// whatever the other properties say.
func TestQueueRestartRefusalPrecedesMutation(t *testing.T) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(7)) //nolint:gosec // fixed seed keeps the property test reproducible
	loadStates := []string{"loaded", "not-found", "masked", "error", "bad-setting", ""}
	fileStates := []string{"enabled", "disabled", "masked", "masked-runtime", "static", "generated", ""}
	for i := 0; i < 200; i++ {
		p := sysbus.UnitProperties{
			LoadState:     loadStates[rng.Intn(len(loadStates))],
			UnitFileState: fileStates[rng.Intn(len(fileStates))],
			ActiveState:   "inactive",
		}
		refused := p.LoadState == "not-found" || p.LoadState == "masked" ||
			p.UnitFileState == "masked" || p.UnitFileState == "masked-runtime"
		f := sysbustest.New()
		f.SetUnit("x.service", p)
		_, err := QueueRestart(ctx, f, Service{ID: "x"}, "x.service")
		if refused != errors.Is(err, ErrUnitUnavailable) {
			t.Fatalf("%+v: err %v", p, err)
		}
		if refused && len(f.Calls()) != 0 {
			t.Fatalf("%+v: mutation before the refusal: %q", p, f.Calls())
		}
		if !refused && (err != nil || len(f.Calls()) != 1) {
			t.Fatalf("%+v: err %v calls %q", p, err, f.Calls())
		}
	}
}

// A mutation the bus refuses is no job: the caller never gets a handle to
// wait on.
func TestQueueRestartBusRefusal(t *testing.T) {
	for verb, id := range map[string]string{"RestartUnit": "x", "ReloadOrRestartUnit": "ssh"} {
		f := sysbustest.New()
		f.SetUnit("x.service", loaded)
		f.Fail(verb, errors.New("boom"))
		job, err := QueueRestart(context.Background(), f, Service{ID: id}, "x.service")
		if err == nil || job != nil {
			t.Fatalf("%s: job %v err %v", verb, job, err)
		}
	}
}

// brokenDir returns a path under a regular file: nothing can be created there.
func brokenDir(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(file, "network")
}

func TestNetworkWriteFailureNeverReloads(t *testing.T) {
	t.Run("configure", func(t *testing.T) {
		_, _, nc := useNetDirs(t)
		NetworkEtcDir = brokenDir(t)
		if err := ConfigureNetInterface("eth0", staticConfig); err == nil {
			t.Fatal("write into a broken directory succeeded")
		}
		if got := nc.Take(); len(got) != 0 {
			t.Fatalf("network calls ran: %v", got)
		}
	})
	t.Run("confirm", func(t *testing.T) {
		_, _, nc := useNetDirs(t)
		if err := WriteNetworkTrial("eth0", staticConfig); err != nil {
			t.Fatal(err)
		}
		NetworkEtcDir = brokenDir(t)
		if err := ConfirmNetworkTrial("eth0"); err == nil {
			t.Fatal("confirm into a broken directory succeeded")
		}
		if _, err := os.Stat(NetworkTrialPath("eth0")); err != nil {
			t.Fatalf("the trial went away with the failed confirm: %v", err)
		}
		if got := nc.Take(); len(got) != 0 {
			t.Fatalf("network calls ran: %v", got)
		}
	})
	t.Run("trial", func(t *testing.T) {
		_, _, nc := useNetDirs(t)
		NetworkRunDir = brokenDir(t)
		if err := WriteNetworkTrial("eth0", staticConfig); err == nil {
			t.Fatal("trial into a broken directory succeeded")
		}
		if got := nc.Take(); len(got) != 0 {
			t.Fatalf("network calls ran: %v", got)
		}
	})
}

// A failed reload stops there: the interface is not reconfigured on a
// configuration networkd did not take.
func TestNetworkReloadFailureSkipsReconfigure(t *testing.T) {
	_, _, nc := useNetDirs(t)
	nc.Fail("NetworkReload", errors.New("boom"))
	if err := ConfigureNetInterface("eth0", staticConfig); err == nil || !strings.Contains(err.Error(), "reload") {
		t.Fatalf("configure: %v", err)
	}
	if got := nc.Take(); !reflect.DeepEqual(got, []string{"NetworkReload"}) {
		t.Fatalf("network calls %v", got)
	}
	if err := WriteNetworkTrial("eth0", staticConfig); err != nil {
		t.Fatal(err)
	}
	if err := ApplyNetworkTrial("eth0"); err == nil {
		t.Fatal("apply with a failed reload succeeded")
	}
	if got := nc.Take(); !reflect.DeepEqual(got, []string{"NetworkReload"}) {
		t.Fatalf("network calls %v", got)
	}

	nc.Fail("NetworkReload", nil)
	nc.Fail("NetworkReconfigure eth0", errors.New("boom"))
	if err := ApplyNetworkTrial("eth0"); err == nil || !strings.Contains(err.Error(), "reconfigure") {
		t.Fatalf("apply with a failed reconfigure: %v", err)
	}
}

// Whatever the interface name, the only file written is the one named after
// it, in the one directory meant for it, and nothing else of the tree moves.
func TestNetworkWritesStayOnTheirOwnFile(t *testing.T) {
	rng := rand.New(rand.NewSource(11)) //nolint:gosec // fixed seed keeps the property test reproducible
	const alphabet = "abcdeth0123.-_/\\ \n*%@:"
	names := []string{"eth0", "wlan1", "en*", "..", ".", "a/b", "../etc", "-all", "apply", "eth0\n", "", "e t", "x" + strings.Repeat("y", 40)}
	for i := 0; i < 80; i++ {
		b := make([]byte, 1+rng.Intn(10))
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		names = append(names, string(b))
	}

	for _, intf := range names {
		etc, _, nc := useNetDirs(t)
		root := filepath.Dir(etc)
		before := treeFiles(t, root)

		werr := WriteNetworkTrial(intf, staticConfig)
		cerr := ConfigureNetInterface(intf, staticConfig)
		if (werr == nil) != (cerr == nil) {
			t.Fatalf("%q: trial %v, configure %v", intf, werr, cerr)
		}
		got := treeFiles(t, root)
		if werr != nil {
			if !reflect.DeepEqual(got, before) || len(nc.Take()) != 0 {
				t.Fatalf("%q refused but the tree or the bus moved: %v", intf, got)
			}
			continue
		}
		want := append([]string{}, before...)
		want = append(want, filepath.Join("etc", "calaos_"+intf+".network"), filepath.Join("run", "00-calaos-trial-"+intf+".network"))
		if !sameSet(got, want) {
			t.Fatalf("%q: tree %v, want %v", intf, got, want)
		}
		if got := nc.Take(); !reflect.DeepEqual(got, []string{"NetworkReload", "NetworkReconfigure " + intf}) {
			t.Fatalf("%q: network calls %v", intf, got)
		}
	}
}

func treeFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameSet(a, b []string) bool {
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}
