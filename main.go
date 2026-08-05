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

var (
	errorRed = color.New(color.FgRed).SprintFunc()

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
