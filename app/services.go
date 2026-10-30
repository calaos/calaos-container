package app

import (
	"context"
	"errors"
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

// v2ServiceAction is one of the five action routes. The order of the checks
// is the contract of docs/API.md: unit= stays out of the audit line of a
// refused pair, and nothing reaches systemd before the guard.
func (a *AppServer) v2ServiceAction(verb string) fiber.Handler {
	refusal := "action not allowed for this service"
	if verb == models.ActionRestart {
		refusal = "unknown or non-restartable service"
	}
	return func(c *fiber.Ctx) error {
		var b struct {
			Unit string `json:"unit"`
		}
		if err := decodeBody(c, &b); err != nil {
			return err
		}
		svc, ok := models.LookupService(b.Unit)
		if !ok || !svc.Allows(verb) {
			return errInvalid(refusal)
		}
		auditField(c, "unit", svc.ID)

		release, err := serviceGuard()
		if err != nil {
			return guardedError(err)
		}
		if verb == models.ActionRestart {
			if err := a.checkRestart(svc); err != nil {
				release()
				return err
			}
			go restartService(svc, release, restartDelay, models.Bus)
		} else {
			defer release()
			if err := runServiceAction(svc, verb, models.Bus); err != nil {
				return err
			}
		}
		return sendOK(c, fiber.StatusOK, fiber.Map{"id": svc.ID, "state": serviceActionStates[verb]})
	}
}

var serviceActionStates = map[string]string{
	models.ActionRestart: "restarting",
	models.ActionStart:   "starting",
	models.ActionStop:    "stopping",
	models.ActionEnable:  "enabling",
	models.ActionDisable: "disabling",
}

// checkUnit reads the unit afresh: the 1 s cache could still show a unit
// masked or removed a moment ago as available.
func checkUnit(unit string, bus sysbus.SystemdBus) error {
	ctx, cancel := context.WithTimeout(context.Background(), serviceCallLimit)
	defer cancel()
	switch err := models.CheckUnit(ctx, bus, unit); {
	case err == nil:
		return nil
	case errors.Is(err, models.ErrUnitNotInstalled):
		return newAPIError(fiber.StatusConflict, codeServiceUnavailable, "the unit is not installed")
	case errors.Is(err, models.ErrUnitMasked):
		return newAPIError(fiber.StatusConflict, codeServiceUnavailable, "the unit is masked")
	default:
		return errInternal(err)
	}
}

func (a *AppServer) checkRestart(svc models.Service) error {
	if err := checkUnit(resolveUnit(svc.Unit, models.Bus), models.Bus); err != nil {
		return err
	}
	if svc.ID != "calaos-container" {
		return nil
	}
	a.net.mu.Lock()
	defer a.net.mu.Unlock()
	if p := a.netLiveLocked(); p != nil {
		return a.errNetPendingLocked(p)
	}
	return nil
}

// runServiceAction hands the job to the background once systemd has it: the
// answer says nothing about the outcome, and the guard must not be held
// through a job that lasts up to serviceJobLimit.
func runServiceAction(svc models.Service, verb string, bus sysbus.SystemdBus) error {
	unit := resolveUnit(svc.Unit, bus)
	if err := checkUnit(unit, bus); err != nil {
		return err
	}
	defer resetServicesCache()
	job, err := models.QueueAction(bus, svc, unit, verb, serviceCallLimit)
	if err != nil {
		return errInternal(err)
	}
	go waitServiceJob(svc, verb, job)
	return nil
}

func waitServiceJob(svc models.Service, verb string, job *sysbus.Job) {
	ctx, cancel := context.WithTimeout(context.Background(), serviceJobLimit)
	defer cancel()
	if err := job.Wait(ctx); err != nil {
		logging.Warnf("%s %s: %v", serviceActionStates[verb], svc.ID, err)
	}
	resetServicesCache()
}

// restartService takes its seams as arguments: it outlives the request.
func restartService(svc models.Service, release func(), delay time.Duration, bus sysbus.SystemdBus) {
	job, err := queueRestart(svc, release, delay, bus)
	if err != nil {
		logging.Warnf("%s %s: %v", serviceActionStates[models.ActionRestart], svc.ID, err)
		return
	}
	waitServiceJob(svc, models.ActionRestart, job)
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
