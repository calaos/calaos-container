package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
)

// Config is the main app config
var Config = koanf.New(".")

// defaultConfig holds a value for every key the daemon relies on, as a flat
// map using the "." delimiter.
//
// general.address defaults to the loopback interface on purpose: an empty
// address makes fasthttp listen on *every* interface, which would expose the
// control API (reboot, disk wipe, network configuration) to the LAN.
var defaultConfig = map[string]interface{}{
	"general.port":    8000,
	"general.address": "127.0.0.1",

	// Duration between update checks, parsed by the models package.
	"general.update_time": "12h",

	// Optional comma separated CORS allow-list. When empty, the allowed
	// origins are derived from general.address / general.port.
	"general.cors_origins": "",

	"log.default": "info",
}

// InitConfig loads the configuration from conffile. It must be called early,
// and every failure is returned to the caller: the daemon must never start on
// a partially loaded configuration, because the fallback used to be an empty
// bind address (i.e. all interfaces).
//
// The configuration is only committed to the package level Config once it has
// been fully loaded and validated.
func InitConfig(conffile *string) error {
	// Keep the stdlib logger (used by a few dependencies) on stdout, like the
	// rest of our logs.
	log.SetOutput(os.Stdout)

	if conffile == nil || strings.TrimSpace(*conffile) == "" {
		return errors.New("no configuration file given")
	}

	// Load into a fresh instance so that a failure leaves the previous
	// configuration untouched and repeated calls stay deterministic.
	k := koanf.New(".")

	if err := k.Load(confmap.Provider(defaultConfig, "."), nil); err != nil {
		return fmt.Errorf("failed to load default config values: %w", err)
	}

	// The config file is TOML. It used to be parsed as JSON *and* TOML with an
	// error reported only when both parsers failed, which hid the real syntax
	// error; parse it as TOML only and surface the error.
	if err := k.Load(file.Provider(*conffile), toml.Parser()); err != nil {
		return fmt.Errorf("failed to load config file %s: %w", *conffile, err)
	}

	// Environment variables are merged last and win over the file: CALAOS_ is
	// stripped, the name is lowercased and "_" becomes the key delimiter, so
	// CALAOS_GENERAL_ADDRESS sets general.address.
	if err := k.Load(env.Provider("CALAOS_", ".", func(s string) string {
		return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, "CALAOS_")), "_", ".")
	}), nil); err != nil {
		return fmt.Errorf("failed to load config from environment: %w", err)
	}

	if err := validate(k); err != nil {
		return fmt.Errorf("invalid config %s: %w", *conffile, err)
	}

	Config = k

	return nil
}

// validate rejects a configuration the daemon must not run with.
func validate(k *koanf.Koanf) error {
	addr := k.String("general.address")
	switch {
	case addr == "":
		return errors.New("general.address is empty; an empty address listens on all " +
			"interfaces and exposes the control API, set it to 127.0.0.1 " +
			"(or explicitly to 0.0.0.0 to listen on every interface)")
	case strings.ContainsAny(addr, " \t\r\n"):
		return fmt.Errorf("general.address %q contains whitespace", addr)
	}

	if port := k.Int("general.port"); port < 1 || port > 65535 {
		return fmt.Errorf("general.port %d is out of range (1-65535)", port)
	}

	return nil
}
