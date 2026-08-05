package logger

import (
	"io"
	"log"
	"os"

	"github.com/sirupsen/logrus"
)

var (
	//Logger is the main logrus logger
	Logger *logrus.Logger
)

// InitLogger inits logger
func init() {
	Logger = logrus.New()
	Logger.Formatter = NewFilterFormatter()
	//logger.SetReportCaller(true)
	Logger.SetLevel(logrus.TraceLevel)

	mw := io.MultiWriter(os.Stdout)
	log.SetOutput(mw)
}

// NewLogger create a logrus entry with domain
func NewLogger(domain string) *logrus.Entry {
	return Logger.WithField("domain", domain)
}
