package logger

import (
	"github.com/sirupsen/logrus"
	log "github.com/sirupsen/logrus"

	"github.com/calaos/calaos-container/config"
)

// FilterFormatter formats logs into text using logrus.TextFormatter
type FilterFormatter struct {
	formatter logrus.Formatter
}

// NewFilterFormatter returns a new formatter
func NewFilterFormatter() *FilterFormatter {
	return &FilterFormatter{
		formatter: &logrus.TextFormatter{
			DisableTimestamp: true,
			QuoteEmptyFields: true,
		},
	}
}

// Format renders a single log entry
func (f *FilterFormatter) Format(entry *log.Entry) ([]byte, error) {
	defaultLogLevel, err := logrus.ParseLevel(config.Config.String("log.default"))
	if err != nil {
		defaultLogLevel = logrus.InfoLevel
	}

	//get current log domain
	currentDom := "default"
	if val, ok := entry.Data["domain"]; ok {
		if dom, ok := val.(string); ok {
			currentDom = dom
		}
	}

	//check if there is a specific log level for this domain
	wantedLevel := defaultLogLevel
	lvl := config.Config.String("log." + currentDom)
	if lvl != "" {
		l, err := logrus.ParseLevel(lvl)
		if err == nil {
			wantedLevel = l
		}
	}

	//	fmt.Printf("currentDom=%s  lvl=%s  entry.Level=%v(%d)  wantedLevel=%v(%d)  displayed=%v\n",
	//		currentDom, lvl, entry.Level, entry.Level, wantedLevel, wantedLevel, entry.Level <= wantedLevel)

	if entry.Level <= wantedLevel {
		return f.formatter.Format(entry)
	}

	return nil, nil
}
