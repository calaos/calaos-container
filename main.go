package main

import (
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/calaos/calaos-container/app"
	"github.com/calaos/calaos-container/config"
	"github.com/calaos/calaos-container/models"

	logger "github.com/calaos/calaos-container/log"

	"github.com/fatih/color"
	cli "github.com/jawher/mow.cli"
	"github.com/sirupsen/logrus"
)

const (
	DefaultConfigFilename = "/etc/calaos-container.toml"

	CharAbort = "✘"
)

// Exit codes, one per failure class so an operator (or `systemctl status`) can
// tell what went wrong without digging through the logs.
const (
	exitUsage      = 1 // invalid command line
	exitConfig     = 2 // config file missing, unparsable or invalid
	exitAppInit    = 3 // app.NewApp() failed
	exitModelsInit = 4 // models.Init() failed
	exitRuntime    = 5 // the HTTP listener failed while running
)

var (
	errorRed = color.New(color.FgRed).SprintFunc()

	logging *logrus.Entry

	myApp *app.AppServer
)

// exit logs err and terminates the process with the given code. It never
// returns.
//
// cli.Exit panics with an exit code that mow.cli's flow recovers and turns
// into os.Exit(code), so this must only be called from within the cli action.
// Logging must not use Fatal*: logrus would os.Exit(1) itself and the
// requested code would never make it out.
func exit(err error, code int) {
	logging.Errorln(errorRed(CharAbort), err)
	cli.Exit(code)
}

// handleSignals blocks until the daemon is asked to stop (SIGINT/SIGTERM),
// the HTTP listener fails or an upgrade job installed a new calaos-container,
// then shuts everything down cleanly. The listener error, if any, is returned
// so the caller can pick the exit code.
func handleSignals(listenErr <-chan error) error {
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigint)

	return waitAndStop(sigint, listenErr, models.RestartRequested(), func() {
		myApp.Shutdown()
		models.Shutdown()
	})
}

// waitAndStop returns nil after a self-upgrade: the exit status is 0 and
// Restart=always starts the new binary.
func waitAndStop(sigint <-chan os.Signal, listenErr <-chan error, restart <-chan struct{}, stop func()) error {
	var err error

	select {
	case sig := <-sigint:
		logging.Println("Received signal", sig, "- shutting down...")
	case err = <-listenErr:
		// A closed channel yields a nil error: the server stopped on its own
		// without failing, shut the rest down as well.
	case <-restart:
		logging.Println("calaos-container was upgraded - exiting to start the new version")
	}

	stop()

	return err
}

func main() {
	logging = logger.NewLogger("calaos-container")
	runtime.GOMAXPROCS(runtime.NumCPU())

	a := cli.App("calaos-container", "Calaos Container Backend")

	a.Spec = "[-c]"

	var (
		conffile = a.StringOpt("c config", DefaultConfigFilename, "Set config file")
	)

	a.Action = func() {
		var err error

		if err = config.InitConfig(conffile); err != nil {
			exit(err, exitConfig)
		}

		if myApp, err = app.NewApp(); err != nil {
			exit(err, exitAppInit)
		}

		if err = models.Init(); err != nil {
			exit(err, exitModelsInit)
		}

		if err = handleSignals(myApp.Start()); err != nil {
			exit(err, exitRuntime)
		}
	}

	if err := a.Run(os.Args); err != nil {
		// Outside of the cli action the panic thrown by cli.Exit would not be
		// recovered by mow.cli, so exit directly here.
		logging.Errorln(errorRed(CharAbort), err)
		os.Exit(exitUsage)
	}
}
