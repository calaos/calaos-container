package models

import (
	logger "github.com/calaos/calaos-container/log"

	"github.com/sirupsen/logrus"
)

var (
	logging *logrus.Entry
)

func init() {
	logging = logger.NewLogger("database")
}

// Init models
func Init() (err error) {

	return
}

// Shutdown models
func Shutdown() {

}
