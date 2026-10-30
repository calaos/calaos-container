package sysbus

import (
	"context"
	"errors"
	"fmt"
	"sync"

	sd "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
	"github.com/sirupsen/logrus"

	logger "github.com/calaos/calaos-container/log"
)

const (
	timedateName = "org.freedesktop.timedate1"
	timedatePath = "/org/freedesktop/timedate1"

	networkName    = "org.freedesktop.network1"
	networkPath    = "/org/freedesktop/network1"
	networkManager = networkName + ".Manager"

	loginName    = "org.freedesktop.login1"
	loginPath    = "/org/freedesktop/login1"
	loginManager = loginName + ".Manager"

	propertiesGet = "org.freedesktop.DBus.Properties.Get"

	jobMode = "replace"
)

var logging *logrus.Entry = logger.NewLogger("sysbus")

// Bus is the real SystemdBus. Every operation opens its own connection: it
// costs little at this call rate, and survives dbus and systemd restarts with
// no reconnection logic.
type Bus struct{}

var _ SystemdBus = (*Bus)(nil)

// New returns the real bus; nothing connects before the first call.
func New() *Bus { return &Bus{} }

// fail keeps the D-Bus text in the daemon log: the legacy routes hand
// err.Error() to the client.
func fail(op string, err error) error {
	logging.Errorf("%s: %v", op, err)
	return errors.New(op + " failed")
}

// dialSystemd prefers systemd's private socket, as systemctl does for root:
// it needs no dbus-daemon and carries JobRemoved to every client.
func dialSystemd(ctx context.Context) (*sd.Conn, error) {
	if conn, err := sd.NewSystemdConnectionContext(ctx); err == nil {
		return conn, nil
	}
	return sd.NewSystemConnectionContext(ctx)
}

// dialJob returns a connection that outlives ctx, which only bounds the dial:
// a connection bound to the caller's context would close, and lose JobRemoved,
// as soon as the caller cancels it.
func dialJob(ctx context.Context) (*sd.Conn, func(), error) {
	life, cancel := context.WithCancel(context.Background())
	dialed := make(chan struct{})
	defer close(dialed)
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-dialed:
		}
	}()

	conn, err := dialSystemd(life)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	// On the system bus systemd only emits signals for subscribed clients.
	if err := conn.Subscribe(); err != nil {
		conn.Close()
		cancel()
		return nil, nil, err
	}
	return conn, func() {
		conn.Close()
		cancel()
	}, nil
}

type jobStarter func(*sd.Conn, context.Context, string, string, chan<- string) (int, error)

func queue(ctx context.Context, op, unit string, start jobStarter) (*Job, error) {
	conn, release, err := dialJob(ctx)
	if err != nil {
		return nil, fail(op, err)
	}
	// go-systemd sends the result from its signal goroutine, under a lock:
	// the buffer keeps it from blocking when Wait has given up.
	result := make(chan string, 1)
	if _, err := start(conn, ctx, unit, jobMode, result); err != nil {
		release()
		return nil, fail(op, err)
	}
	return NewJob(unit, result, release), nil
}

func (b *Bus) StartUnit(ctx context.Context, unit string) (*Job, error) {
	return queue(ctx, "start "+unit, unit, (*sd.Conn).StartUnitContext)
}

func (b *Bus) StopUnit(ctx context.Context, unit string) (*Job, error) {
	return queue(ctx, "stop "+unit, unit, (*sd.Conn).StopUnitContext)
}

func (b *Bus) RestartUnit(ctx context.Context, unit string) (*Job, error) {
	return queue(ctx, "restart "+unit, unit, (*sd.Conn).RestartUnitContext)
}

func (b *Bus) ReloadOrRestartUnit(ctx context.Context, unit string) (*Job, error) {
	return queue(ctx, "reload-or-restart "+unit, unit, (*sd.Conn).ReloadOrRestartUnitContext)
}

func (b *Bus) UnitProperties(ctx context.Context, units ...string) ([]UnitProperties, error) {
	const op = "read unit state"
	conn, err := dialSystemd(ctx)
	if err != nil {
		return nil, fail(op, err)
	}
	defer conn.Close()

	out := make([]UnitProperties, len(units))
	for i, u := range units {
		raw, err := conn.GetUnitPropertiesContext(ctx, u)
		if err != nil {
			return nil, fail(op, fmt.Errorf("%s: %w", u, err))
		}
		if out[i], err = parseUnitProperties(raw); err != nil {
			return nil, fail(op, fmt.Errorf("%s: %w", u, err))
		}
	}
	return out, nil
}

// parseUnitProperties refuses a reply without LoadState: an empty one would
// read as an installed, unmasked unit.
func parseUnitProperties(raw map[string]interface{}) (UnitProperties, error) {
	var p UnitProperties
	for name, dst := range map[string]*string{
		"LoadState":     &p.LoadState,
		"ActiveState":   &p.ActiveState,
		"SubState":      &p.SubState,
		"UnitFileState": &p.UnitFileState,
	} {
		if v, ok := raw[name].(string); ok {
			*dst = v
		}
	}
	if p.LoadState == "" {
		return UnitProperties{}, errors.New("no LoadState")
	}
	p.ActiveEnterTimestamp, _ = raw["ActiveEnterTimestamp"].(uint64)
	return p, nil
}

func (b *Bus) EnableUnitFiles(ctx context.Context, units []string) error {
	const op = "enable units"
	conn, err := dialSystemd(ctx)
	if err != nil {
		return fail(op, err)
	}
	defer conn.Close()
	if _, _, err := conn.EnableUnitFilesContext(ctx, units, false, false); err != nil {
		return fail(op, err)
	}
	return nil
}

func (b *Bus) DisableUnitFiles(ctx context.Context, units []string) error {
	const op = "disable units"
	conn, err := dialSystemd(ctx)
	if err != nil {
		return fail(op, err)
	}
	defer conn.Close()
	if _, err := conn.DisableUnitFilesContext(ctx, units, false); err != nil {
		return fail(op, err)
	}
	return nil
}

func (b *Bus) Reload(ctx context.Context) error {
	const op = "reload systemd"
	conn, err := dialSystemd(ctx)
	if err != nil {
		return fail(op, err)
	}
	defer conn.Close()
	if err := conn.ReloadContext(ctx); err != nil {
		return fail(op, err)
	}
	return nil
}

// onSystemBus runs fn on a connection to the system bus, which timedated,
// networkd and logind are only reachable through.
func onSystemBus(ctx context.Context, op string, fn func(*dbus.Conn) error) error {
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return fail(op, err)
	}
	defer conn.Close()
	if err := fn(conn); err != nil {
		return fail(op, err)
	}
	return nil
}

func property[T any](ctx context.Context, obj dbus.BusObject, iface, name string) (T, error) {
	var out T
	var v dbus.Variant
	if err := obj.CallWithContext(ctx, propertiesGet, 0, iface, name).Store(&v); err != nil {
		return out, err
	}
	return out, v.Store(&out)
}

// rtcWarning logs an unreadable hardware clock once: GET system/time is polled,
// and a box without a clock stays that way.
var rtcWarning sync.Once

func (b *Bus) Timedate(ctx context.Context) (TimedateState, error) {
	var st TimedateState
	err := onSystemBus(ctx, "read time state", func(conn *dbus.Conn) error {
		obj := conn.Object(timedateName, timedatePath)
		var err error
		if st.Timezone, err = property[string](ctx, obj, timedateName, "Timezone"); err != nil {
			return err
		}
		if st.NTP, err = property[bool](ctx, obj, timedateName, "NTP"); err != nil {
			return err
		}
		if st.NTPSynchronized, err = property[bool](ctx, obj, timedateName, "NTPSynchronized"); err != nil {
			return err
		}
		// Apart from GetAll on purpose: timedated fails this one getter
		// when the hardware clock cannot be read.
		if rtc, err := property[uint64](ctx, obj, timedateName, "RTCTimeUSec"); err != nil {
			rtcWarning.Do(func() { logging.Warnf("reading the hardware clock: %v", err) })
		} else {
			st.RTCTimeUSec = rtc
		}
		return nil
	})
	if err != nil {
		return TimedateState{}, err
	}
	return st, nil
}

func timedateCall(ctx context.Context, op, method string, args ...interface{}) error {
	return onSystemBus(ctx, op, func(conn *dbus.Conn) error {
		return conn.Object(timedateName, timedatePath).CallWithContext(ctx, timedateName+"."+method, 0, args...).Store()
	})
}

func (b *Bus) SetTimezone(ctx context.Context, timezone string) error {
	return timedateCall(ctx, "set timezone", "SetTimezone", timezone, false)
}

func (b *Bus) SetNTP(ctx context.Context, enable bool) error {
	return timedateCall(ctx, "set ntp", "SetNTP", enable, false)
}

func (b *Bus) SetTime(ctx context.Context, usec int64) error {
	return timedateCall(ctx, "set time", "SetTime", usec, false, false)
}

func (b *Bus) ListTimezones(ctx context.Context) ([]string, error) {
	var zones []string
	err := onSystemBus(ctx, "list timezones", func(conn *dbus.Conn) error {
		return conn.Object(timedateName, timedatePath).CallWithContext(ctx, timedateName+".ListTimezones", 0).Store(&zones)
	})
	if err != nil {
		return nil, err
	}
	return zones, nil
}

func (b *Bus) NetworkReload(ctx context.Context) error {
	return onSystemBus(ctx, "reload networkd", func(conn *dbus.Conn) error {
		return conn.Object(networkName, networkPath).CallWithContext(ctx, networkManager+".Reload", 0).Store()
	})
}

func (b *Bus) NetworkReconfigure(ctx context.Context, intf string) error {
	return onSystemBus(ctx, "reconfigure interface", func(conn *dbus.Conn) error {
		mgr := conn.Object(networkName, networkPath)
		var index int32
		var link dbus.ObjectPath
		if err := mgr.CallWithContext(ctx, networkManager+".GetLinkByName", 0, intf).Store(&index, &link); err != nil {
			return err
		}
		return mgr.CallWithContext(ctx, networkManager+".ReconfigureLink", 0, index).Store()
	})
}

func (b *Bus) NetworkDescribe(ctx context.Context) (string, error) {
	var out string
	err := onSystemBus(ctx, "describe network", func(conn *dbus.Conn) error {
		return conn.Object(networkName, networkPath).CallWithContext(ctx, networkManager+".Describe", 0).Store(&out)
	})
	if err != nil {
		return "", err
	}
	return out, nil
}

func (b *Bus) Reboot(ctx context.Context) error {
	return onSystemBus(ctx, "reboot", func(conn *dbus.Conn) error {
		return conn.Object(loginName, loginPath).CallWithContext(ctx, loginManager+".Reboot", 0, false).Store()
	})
}
