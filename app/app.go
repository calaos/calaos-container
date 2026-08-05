package app

import (
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	fiberLog "github.com/gofiber/fiber/v2/middleware/logger"

	"github.com/calaos/calaos-container/config"
	"github.com/calaos/calaos-container/events"
	logger "github.com/calaos/calaos-container/log"
	"github.com/sirupsen/logrus"
)

const (
	maxFileSize = 1 * 1024 * 1024 * 1024
)

type AppServer struct {
	quitHeartbeat chan interface{}
	wgDone        sync.WaitGroup

	appFiber *fiber.App

	// hub is the event bus streamed over GET /api/events. It defaults to the
	// package-level events.Default hub; tests may override it.
	hub *events.Hub
}

var logging *logrus.Entry

func init() {
	logging = logger.NewLogger("app")
}

// Init the app
func NewApp() (a *AppServer, err error) {
	logging.Infoln("Init server")

	a = &AppServer{
		quitHeartbeat: make(chan interface{}),
		hub:           events.Default,
		appFiber: fiber.New(fiber.Config{
			ServerHeader:          "Calaos Container (Linux)",
			ReadTimeout:           time.Second * 20,
			AppName:               "Calaos Container",
			DisableStartupMessage: true,
			EnablePrintRoutes:     false,
			BodyLimit:             maxFileSize,
		}),
	}

	a.appFiber.
		Use(fiberLog.New(fiberLog.Config{})).
		Use(NewTokenMiddleware())

	a.appFiber.Use(cors.New(cors.Config{
		AllowOrigins: "http://127.0.0.1",
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

	//Get currently installed images
	api.Get("/update/images", func(c *fiber.Ctx) error {
		return a.apiUpdateImages(c)
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

	// Generic event bus over WebSocket. The global Bearer middleware already
	// authenticated the HTTP upgrade request (header or ?token=). Reject plain
	// GET requests that are not a WebSocket upgrade with 426.
	api.Use("/events", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})
	api.Get("/events", websocket.New(a.apiEvents))

	return
}

// Run the app
func (a *AppServer) Start() {
	addr := config.Config.String("general.address") + ":" + strconv.Itoa(config.Config.Int("general.port"))

	logging.Infoln("\u21D2 Server listening on", addr)

	go func() {
		if err := a.appFiber.Listen(addr); err != nil {
			logging.Fatalf("Failed to listen http server: %v", err)
		}
	}()
	a.wgDone.Add(1)
}

// Stop the app
func (a *AppServer) Shutdown() {
	close(a.quitHeartbeat)
	// Close the event hub first: hijacked WebSocket connections are not
	// closed by fiber's Shutdown, so closing the subscriber channels is what
	// makes the WS handlers return.
	a.hub.Close()
	_ = a.appFiber.Shutdown()
	a.wgDone.Wait()
}
