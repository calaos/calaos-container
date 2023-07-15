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

// Infof outputs info log
func Infof(domain string, format string, v ...interface{}) {
	Logger.WithField("domain", domain).Infof(format, v...)
}

// Warnf outputs info log
func Warnf(domain string, format string, v ...interface{}) {
	Logger.WithField("domain", domain).Warnf(format, v...)
}

// Fatalf outputs info log
func Fatalf(domain string, format string, v ...interface{}) {
	Logger.WithField("domain", domain).Fatalf(format, v...)
}

// Tracef outputs info log
func Tracef(domain string, format string, v ...interface{}) {
	Logger.WithField("domain", domain).Tracef(format, v...)
}

// Debugf outputs info log
func Debugf(domain string, format string, v ...interface{}) {
	Logger.WithField("domain", domain).Debugf(format, v...)
}

// Errorf outputs info log
func Errorf(domain string, format string, v ...interface{}) {
	Logger.WithField("domain", domain).Errorf(format, v...)
}

// Panicf outputs info log
func Panicf(domain string, format string, v ...interface{}) {
	Logger.WithField("domain", domain).Panicf(format, v...)
}

// Printf outputs info log, same as Infof
func Printf(domain string, format string, v ...interface{}) {
	Logger.WithField("domain", domain).Printf(format, v...)
}
