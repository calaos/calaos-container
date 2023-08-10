package app

import (
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	fiberLog "github.com/gofiber/fiber/v2/middleware/logger"

	"github.com/calaos/calaos-container/config"
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
		appFiber: fiber.New(fiber.Config{
			ServerHeader:          "Greenhouse (Linux)",
			ReadTimeout:           time.Second * 20,
			AppName:               "Greenhouse",
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
	a.appFiber.Shutdown()
	a.wgDone.Wait()
}
