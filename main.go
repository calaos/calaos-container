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
	//	"github.com/containers/podman/v4/pkg/bindings"
	//	"github.com/containers/podman/v4/pkg/bindings/images"
	//
	// "github.com/coreos/go-systemd/v22/dbus"
	// godbus "github.com/godbus/dbus/v5"
)

/*
 Podman API Docs:
 https://pkg.go.dev/github.com/containers/podman/v4@v4.5.1/pkg/bindings#section-readme
*/
/*
func main() {
	conn, err := bindings.NewConnection(context.Background(), "unix://run/podman/podman.sock")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	_, err = images.Pull(conn, "ghcr.io/calaos/calaos_home", nil)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
*/

const (
	DefaultConfigFilename = "/etc/calaos-container.toml"

	CharStar     = "\u2737"
	CharAbort    = "\u2718"
	CharCheck    = "\u2714"
	CharWarning  = "\u26A0"
	CharArrow    = "\u2012\u25b6"
	CharVertLine = "\u2502"
)

var (
	blue       = color.New(color.FgBlue).SprintFunc()
	errorRed   = color.New(color.FgRed).SprintFunc()
	errorBgRed = color.New(color.BgRed, color.FgBlack).SprintFunc()
	green      = color.New(color.FgGreen).SprintFunc()
	cyan       = color.New(color.FgCyan).SprintFunc()
	bgCyan     = color.New(color.FgWhite).SprintFunc()

	logging *logrus.Entry

	myApp *app.AppServer
)

func exit(err error, exit int) {
	logging.Fatalln(errorRed(CharAbort), err)
	cli.Exit(exit)
}

func handleSignals() {
	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	<-sigint

	logging.Println("Shuting down...")
	myApp.Shutdown()
	models.Shutdown()
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
			exit(err, 1)
		}

		if myApp, err = app.NewApp(); err != nil {
			exit(err, 1)
		}

		if err = models.Init(); err != nil {
			exit(err, 1)
		}

		myApp.Start()

		handleSignals()
	}

	if err := a.Run(os.Args); err != nil {
		exit(err, 1)
	}
}
