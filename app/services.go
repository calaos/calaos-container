package app

import (
	"context"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/calaos/calaos-container/models"
	"github.com/calaos/calaos-container/sysbus"
)

const (
	servicesCacheTTL = time.Second
	serviceCallLimit = 10 * time.Second
	// A restart is a stop then a start, each bounded by 90 s in systemd by
	// default.
	serviceJobLimit = 5 * time.Minute
	// Log follow polls come every 2 s; a unit is only swapped by a package
	// install.
	unitCacheTTL = time.Minute
)

var (
	serviceGuard = models.ServiceGuard
	// The answer must reach the client before calaos-server, which relays
	// it, receives SIGTERM.
	restartDelay = 500 * time.Millisecond
)

var servicesCache struct {
	mu     sync.Mutex
	at     time.Time
	states []models.ServiceState
}

var unitCache struct {
	mu    sync.Mutex
	units map[string]unitCacheEntry
}

type unitCacheEntry struct {
	unit string
	at   time.Time
}

func resetServicesCache() {
	servicesCache.mu.Lock()
	servicesCache.states = nil
	servicesCache.mu.Unlock()
	unitCache.mu.Lock()
	unitCache.units = nil
	unitCache.mu.Unlock()
}

// resolveUnit returns the installed candidate of a table unit. A failed
// read is not cached and falls back to the unit itself.
func resolveUnit(unit string, bus sysbus.SystemdBus) string {
	if !models.HasUnitFallbacks(unit) {
		return unit
	}
	unitCache.mu.Lock()
	defer unitCache.mu.Unlock()
	if e, ok := unitCache.units[unit]; ok && time.Since(e.at) < unitCacheTTL {
		return e.unit
	}
	ctx, cancel := context.WithTimeout(context.Background(), serviceCallLimit)
	defer cancel()
	resolved, err := models.ResolveUnit(ctx, bus, unit)
	if err != nil {
		logging.Warnf("resolving %s: %v", unit, err)
		return unit
	}
	if unitCache.units == nil {
		unitCache.units = map[string]unitCacheEntry{}
	}
	unitCache.units[unit] = unitCacheEntry{resolved, time.Now()}
	return resolved
}

func serviceStates() ([]models.ServiceState, error) {
	servicesCache.mu.Lock()
	defer servicesCache.mu.Unlock()
	if servicesCache.states != nil && time.Since(servicesCache.at) < servicesCacheTTL {
		return servicesCache.states, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), serviceCallLimit)
	defer cancel()
	states, err := models.ServiceStates(ctx, models.Bus)
	if err != nil {
		return nil, err
	}
	servicesCache.states, servicesCache.at = states, time.Now()
	return states, nil
}

func (a *AppServer) v2Services(c *fiber.Ctx) error {
	states, err := serviceStates()
	if err != nil {
		return errInternal(err)
	}
	return sendOK(c, fiber.StatusOK, fiber.Map{"services": states})
}

func (a *AppServer) v2ServiceRestart(c *fiber.Ctx) error {
	var b struct {
		Unit string `json:"unit"`
	}
	if err := decodeBody(c, &b); err != nil {
		return err
	}
	svc, ok := models.LookupService(b.Unit)
	if !ok {
		return errInvalid("unknown or non-restartable service")
	}
	auditField(c, "unit", svc.ID)
	if !svc.Restartable {
		return errInvalid("unknown or non-restartable service")
	}

	release, err := serviceGuard()
	if err != nil {
		return guardedError(err)
	}
	go restartService(svc, release, restartDelay, models.Bus)

	return sendOK(c, fiber.StatusOK, fiber.Map{"id": svc.ID, "state": "restarting"})
}

// restartService takes its seams as arguments: it outlives the request.
func restartService(svc models.Service, release func(), delay time.Duration, bus sysbus.SystemdBus) {
	job, err := queueRestart(svc, release, delay, bus)
	if err != nil {
		logging.Warnf("restarting %s: %v", svc.ID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), serviceJobLimit)
	defer cancel()
	if err := job.Wait(ctx); err != nil {
		logging.Warnf("restarting %s: %v", svc.ID, err)
	}
	resetServicesCache()
}

// queueRestart hands the guard back once systemd has the job: holding it
// through the job would answer 409 to every other restart meanwhile.
func queueRestart(svc models.Service, release func(), delay time.Duration, bus sysbus.SystemdBus) (*sysbus.Job, error) {
	defer release()
	time.Sleep(delay)

	unit := resolveUnit(svc.Unit, bus)
	ctx, cancel := context.WithTimeout(context.Background(), serviceCallLimit)
	defer cancel()
	job, err := models.QueueRestart(ctx, bus, svc, unit)
	if err != nil {
		return nil, err
	}
	resetServicesCache()
	return job, nil
}
