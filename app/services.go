package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/calaos/calaos-container/models"
)

const (
	servicesCacheTTL = time.Second
	serviceCmdLimit  = 10 * time.Second
)

var (
	serviceGuard      = models.ServiceGuard
	runServiceCommand = models.RunCommandContext
	// The answer must reach the client before calaos-server, which relays
	// it, receives SIGTERM.
	restartDelay = 500 * time.Millisecond
)

var servicesCache struct {
	mu     sync.Mutex
	at     time.Time
	states []models.ServiceState
}

func resetServicesCache() {
	servicesCache.mu.Lock()
	servicesCache.states = nil
	servicesCache.mu.Unlock()
}

func serviceStates() ([]models.ServiceState, error) {
	servicesCache.mu.Lock()
	defer servicesCache.mu.Unlock()
	if servicesCache.states != nil && time.Since(servicesCache.at) < servicesCacheTTL {
		return servicesCache.states, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), serviceCmdLimit)
	defer cancel()
	out, err := runServiceCommand(ctx, "systemctl", models.ServiceStateArgs()...)
	if err != nil {
		return nil, err
	}
	states, err := models.ParseServiceStates(out)
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
	go restartService(svc, release, restartDelay, runServiceCommand)

	return sendOK(c, fiber.StatusOK, fiber.Map{"id": svc.ID, "state": "restarting"})
}

// restartService takes its seams as arguments: it outlives the request.
func restartService(svc models.Service, release func(), delay time.Duration,
	run func(context.Context, string, ...string) (string, error)) {
	defer release()
	time.Sleep(delay)

	ctx, cancel := context.WithTimeout(context.Background(), serviceCmdLimit)
	defer cancel()
	args := svc.RestartArgs()
	if _, err := run(ctx, "systemctl", args...); err != nil {
		logging.Warnf("systemctl %s: %v", strings.Join(args, " "), err)
		return
	}
	resetServicesCache()
}
