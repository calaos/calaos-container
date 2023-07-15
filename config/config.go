package config

import (
	"log"
	"os"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
)

// Config is the main app config
var Config = koanf.New(".")

var (
	defaultConfig map[string]interface{} = map[string]interface{}{
		"general.port":    8000,
		"general.address": "",
		"log.default":     "info",
	}

	//a dedicated logger must be used here to avoid conflict
	logging *logrus.Logger
)

// InitConfig from config file, must be called early
func InitConfig(conffile *string) error {
	logging = logrus.New()
	logging.Formatter = &logrus.TextFormatter{
		DisableTimestamp: true,
		QuoteEmptyFields: true,
	}
	logging.SetLevel(logrus.TraceLevel)
	log.SetOutput(os.Stdout)

	// Load default values using the confmap provider.
	// We provide a flat map with the "." delimiter.
	// A nested map can be loaded by setting the delimiter to an empty string "".
	Config.Load(confmap.Provider(defaultConfig, "."), nil)

	// Load JSON config.
	errJson := Config.Load(file.Provider(*conffile), json.Parser())

	// Load TOML config and merge into the previously loaded config (because we can).
	errToml := Config.Load(file.Provider(*conffile), toml.Parser())

	if errJson != nil && errToml != nil {
		logging.WithField("parser", "json").Errorf("error loading config (%s): %v", *conffile, errJson)
		logging.WithField("parser", "toml").Errorf("error loading config (%s): %v", *conffile, errToml)
	}

	// Load environment variables and merge into the loaded config.
	// "GH" is the prefix to filter the env vars by.
	// "." is the delimiter used to represent the key hierarchy in env vars.
	// The (optional, or can be nil) function can be used to transform
	// the env var names, for instance, to lowercase them.
	//
	// For example, env vars: CALAOS_TYPE and CALAOS_PARENT1_CHILD1_NAME
	// will be merged into the "type" and the nested "parent1.child1.name"
	// keys in the config file here as we lowercase the key,
	// replace `_` with `.` and strip the CALAOS_ prefix so that
	// only "parent1.child1.name" remains.
	return Config.Load(env.Provider("CALAOS_", ".", func(s string) string {
		return strings.Replace(strings.ToLower(
			strings.TrimPrefix(s, "CALAOS_")), "_", ".", -1)
	}), nil)
}
