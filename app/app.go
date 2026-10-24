package app

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	fiberLog "github.com/gofiber/fiber/v2/middleware/logger"

	"github.com/calaos/calaos-container/app/admin"
	"github.com/calaos/calaos-container/app/console"
	"github.com/calaos/calaos-container/config"
	"github.com/calaos/calaos-container/events"
	logger "github.com/calaos/calaos-container/log"
	"github.com/sirupsen/logrus"
)

const (
	// maxBodySize caps the size of a request body. No endpoint takes a large
	// upload (the installer streams data *out*, not in), so 1 MiB is plenty
	// and keeps a single request from forcing a huge allocation.
	maxBodySize = 1 * 1024 * 1024

	// readTimeout is the deadline for reading a full request.
	readTimeout = 20 * time.Second

	// idleTimeout closes keep-alive connections that sit idle between
	// requests. Hijacked WebSocket connections (/api/events) are not affected.
	idleTimeout = 75 * time.Second

	// defaultAddr mirrors the config default and is only used as a fallback
	// when deriving the CORS origins.
	defaultAddr = "127.0.0.1"
)

type AppServer struct {
	wgDone sync.WaitGroup

	appFiber *fiber.App

	// hub is the event bus streamed over GET /api/events. It defaults to the
	// package-level events.Default hub; tests may override it.
	hub *events.Hub

	sessions sessionValidator

	admin *admin.Manager

	now         func() time.Time
	consoles    *console.Manager
	stopConsole context.CancelFunc
	consoleDone chan struct{}

	// wsConns counts the event streams, so Shutdown lets them flush the
	// last events before the process exits. No Add once wsClosing is set.
	wsMu      sync.Mutex
	wsClosing bool
	wsConns   sync.WaitGroup
}

var logging *logrus.Entry

func init() {
	logging = logger.NewLogger("app")
}

// Init the app
func NewApp() (a *AppServer, err error) {
	logging.Infoln("Init server")

	// Derived before anything else: the cors middleware panics on a malformed
	// origin, so a bad address/override must fail here as a plain error.
	allowOrigins, err := corsAllowOrigins(
		config.Config.String("general.address"),
		config.Config.Int("general.port"),
		config.Config.String("general.cors_origins"))
	if err != nil {
		return nil, err
	}

	a = &AppServer{
		hub: events.Default,
		appFiber: fiber.New(fiber.Config{
			ServerHeader:          "Calaos Container (Linux)",
			ReadTimeout:           readTimeout,
			IdleTimeout:           idleTimeout,
			AppName:               "Calaos Container",
			DisableStartupMessage: true,
			EnablePrintRoutes:     false,
			BodyLimit:             maxBodySize,
			ErrorHandler:          errorHandler,
		}),
	}

	// Reads a.hub at publish time: tests swap the hub after NewApp.
	a.admin = admin.New(admin.Options{
		DataDir:    adminDataDir,
		RuntimeDir: adminRuntimeDir,
		Hash:       adminHashParams,
		Now:        adminNow,
		Publish:    func(evType string, data any) { publishAdmin(a.hub, evType, data) },
		// Set before any request is served.
		SessionsEnded: func(hash, reason string) {
			if a.consoles != nil {
				a.consoles.SessionsEnded(hash, reason)
			}
		},
	})
	a.sessions = a.admin
	a.now = adminNow
	if a.now == nil {
		a.now = time.Now
	}
	a.consoles = console.New(console.Options{
		Dir:    filepath.Join(adminRuntimeDir, "console"),
		Now:    a.now,
		Closed: a.consoleClosed,
	})

	a.appFiber.
		Use(fiberLog.New(fiberLog.Config{})).
		Use(a.auditMiddleware).
		Use(NewTokenMiddleware())

	a.appFiber.Use(cors.New(cors.Config{
		AllowOrigins: allowOrigins,
	}))

	a.appFiber.Hooks().OnShutdown(func() error {
		a.wgDone.Done()
		return nil
	})

	//API
	api := a.appFiber.Group("/api")

	api.Post("/system/reboot", func(c *fiber.Ctx) error {
		return a.apiSystemReboot(c)
	})

	api.Post("/system/restart", func(c *fiber.Ctx) error {
		return a.apiSystemRestart(c)
	})

	api.Get("/system/fs_status", func(c *fiber.Ctx) error {
		return a.apiSystemFsStatus(c)
	})

	api.Post("/system/rollback_snapshot", func(c *fiber.Ctx) error {
		return a.apiSystemRollbackSnapshot(c)
	})

	api.Get("/system/install/list_devices", func(c *fiber.Ctx) error {
		return a.apiSystemInstallListDevices(c)
	})

	api.Post("/system/install/start", func(c *fiber.Ctx) error {
		return a.apiSystemInstallStart(c)
	})

	api.Get("/system/install/status", func(c *fiber.Ctx) error {
		return a.apiSystemLastInstallStatus(c)
	})

	api.Get("/system/info", func(c *fiber.Ctx) error {
		return a.apiSystemInfo(c)
	})

	api.Get("/network/list", func(c *fiber.Ctx) error {
		return a.apiNetIntfList(c)
	})

	api.Post("/network/:intf", func(c *fiber.Ctx) error {
		return a.apiNetIntfConfigure(c)
	})

	//Force an update check
	api.Get("/update/check", func(c *fiber.Ctx) error {
		return a.apiUpdateCheck(c)
	})

	//Get available updates
	api.Get("/update/available", func(c *fiber.Ctx) error {
		return a.apiUpdateAvail(c)
	})

	//List installed calaos-* packages
	api.Get("/update/installed", func(c *fiber.Ctx) error {
		return a.apiUpdateInstalled(c)
	})

	api.Post("/update/upgrade-all", func(c *fiber.Ctx) error {
		return a.apiUpdateUpgradeAll(c)
	})

	api.Post("/update/upgrade", func(c *fiber.Ctx) error {
		return a.apiUpdateUpgrade(c)
	})

	api.Get("/update/status", func(c *fiber.Ctx) error {
		return a.apiUpdateStatus(c)
	})

	a.registerEvents(api)

	a.registerV2(api.Group("/v2"))

	return
}

// registerV2 serves the versioned API (docs/API.md). Every route is either
// sessionAllowed (calaos_server may relay it) or bearerOnly.
func (a *AppServer) registerV2(v2 fiber.Router) {
	s := a.sessionAllowed

	v2.Get("/update/check", s, a.v2UpdateCheck)
	v2.Get("/update/available", s, a.v2UpdateAvailable)
	v2.Get("/update/installed", s, a.v2UpdateInstalled)
	v2.Get("/update/status", s, a.v2UpdateStatus)
	v2.Post("/update/upgrade-all", s, a.v2UpdateUpgradeAll)
	v2.Post("/update/upgrade", s, a.v2UpdateUpgrade)

	v2.Post("/system/reboot", s, a.v2SystemReboot)
	v2.Post("/system/restart", s, a.v2SystemRestart)
	v2.Get("/system/info", s, a.v2SystemInfo)
	v2.Get("/system/fs_status", s, a.v2SystemFsStatus)
	v2.Post("/system/rollback_snapshot", bearerOnly, a.v2SystemRollbackSnapshot)
	v2.Get("/system/install/list_devices", bearerOnly, a.v2InstallListDevices)
	v2.Post("/system/install/start", bearerOnly, a.v2InstallStart)
	v2.Get("/system/install/status", bearerOnly, a.v2InstallStatus)

	v2.Get("/network/list", bearerOnly, a.v2NetList)
	v2.Post("/network/:intf", bearerOnly, a.v2NetConfigure)

	a.registerAdmin(v2)

	a.registerEvents(v2, s)
}

// registerEvents serves the event bus over WebSocket. The global Bearer
// middleware already authenticated the HTTP upgrade request; guards run next,
// then plain GET requests that are not a WebSocket upgrade get 426.
func (a *AppServer) registerEvents(r fiber.Router, guards ...fiber.Handler) {
	args := []interface{}{"/events"}
	for _, g := range guards {
		args = append(args, g)
	}
	args = append(args, fiber.Handler(func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	}))
	r.Use(args...)
	r.Get("/events", websocket.New(a.apiEvents))
}

// Start runs the HTTP server in a background goroutine.
//
// It returns a channel reporting a listener failure to the caller: the error
// must be handled by the main goroutine (which can still run Shutdown) instead
// of killing the process from the goroutine. The channel is closed when the
// server stops, so a clean Shutdown yields a nil error.
func (a *AppServer) Start() <-chan error {
	addr := net.JoinHostPort(
		config.Config.String("general.address"),
		strconv.Itoa(config.Config.Int("general.port")))

	logging.Infoln("\u21D2 Server listening on", addr)

	a.wgDone.Add(1)

	ctx, cancel := context.WithCancel(context.Background())
	a.stopConsole = cancel
	a.consoleDone = make(chan struct{})
	go func() {
		defer close(a.consoleDone)
		a.consoles.Run(ctx)
	}()

	listenErr := make(chan error, 1)
	go func() {
		defer close(listenErr)
		if err := a.appFiber.Listen(addr); err != nil {
			listenErr <- fmt.Errorf("http server failed to listen on %s: %w", addr, err)
		}
	}()

	return listenErr
}

// corsAllowOrigins builds the CORS allow-list for the API.
//
// override (general.cors_origins) wins when set, so a UI served from another
// host can be allowed explicitly. Otherwise the list is derived from the
// address the daemon listens on: both the bare origin (a UI served on port 80)
// and the API origin itself, instead of a hardcoded literal that silently
// diverges from general.address.
func corsAllowOrigins(addr string, port int, override string) (string, error) {
	origins := strings.TrimSpace(override)

	if origins == "" {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			// Config validation rejects an empty address; this only keeps a
			// caller that skipped it (tests) from producing a bogus origin.
			addr = defaultAddr
		}

		origins = httpOrigin(addr, "")
		if port > 0 && port <= 65535 {
			origins += "," + httpOrigin(addr, strconv.Itoa(port))
		}
	}

	if err := validateOrigins(origins); err != nil {
		return "", err
	}

	return origins, nil
}

// httpOrigin formats host (and optional port) as an http:// origin, bracketing
// IPv6 literals.
func httpOrigin(host, port string) string {
	if port != "" {
		return "http://" + net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return "http://[" + host + "]"
	}
	return "http://" + host
}

// validateOrigins applies the same checks as fiber's cors middleware, which
// panics on a malformed origin. Failing here turns an operator typo into a
// startup error instead of a panic.
func validateOrigins(origins string) error {
	if origins == "*" {
		return nil
	}

	for _, origin := range strings.Split(origins, ",") {
		o := strings.TrimSpace(origin)

		// The middleware supports a "scheme://*.domain" subdomain wildcard;
		// drop the "*" the same way it does before validating.
		if i := strings.Index(o, "://*."); i != -1 {
			o = strings.TrimSpace(o[:i+3] + o[i+4:])
		}

		u, err := url.Parse(o)
		if err != nil {
			return fmt.Errorf("invalid CORS origin %q: %w", origin, err)
		}
		if u.Scheme == "" || u.Host == "" || strings.Contains(u.Host, "*") ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("invalid CORS origin %q: expected scheme://host", origin)
		}
	}

	return nil
}

// Stop the app
func (a *AppServer) Shutdown() {
	a.wsMu.Lock()
	a.wsClosing = true
	a.wsMu.Unlock()

	// Close the event hub first: hijacked WebSocket connections are not
	// closed by fiber's Shutdown, so closing the subscriber channels is what
	// makes the WS handlers return.
	a.hub.Close()
	_ = a.appFiber.Shutdown()
	a.wgDone.Wait()

	if a.stopConsole != nil {
		a.stopConsole()
		<-a.consoleDone
	}

	flushed := make(chan struct{})
	go func() {
		a.wsConns.Wait()
		close(flushed)
	}()
	select {
	case <-flushed:
	case <-time.After(wsWriteWait):
		logging.Warnln("event streams still open at shutdown")
	}
}
