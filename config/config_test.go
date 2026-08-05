package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConf writes content to a temporary config file and returns its path.
func writeConf(t *testing.T, content string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), "calaos-container.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return p
}

func TestInitConfigDefaults(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		wantAddr       string
		wantPort       int
		wantUpdateTime string
	}{
		{
			name:           "empty file uses every default",
			content:        "",
			wantAddr:       "127.0.0.1",
			wantPort:       8000,
			wantUpdateTime: "12h",
		},
		{
			name:           "minimal config without update_time",
			content:        "[general]\nport = 8014\n",
			wantAddr:       "127.0.0.1",
			wantPort:       8014,
			wantUpdateTime: "12h",
		},
		{
			name:           "file values win over defaults",
			content:        "[general]\nport = 9000\naddress = \"0.0.0.0\"\nupdate_time = \"30m\"\n",
			wantAddr:       "0.0.0.0",
			wantPort:       9000,
			wantUpdateTime: "30m",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conf := writeConf(t, tc.content)
			if err := InitConfig(&conf); err != nil {
				t.Fatalf("InitConfig: unexpected error: %v", err)
			}

			if got := Config.String("general.address"); got != tc.wantAddr {
				t.Errorf("general.address = %q, want %q", got, tc.wantAddr)
			}
			if got := Config.Int("general.port"); got != tc.wantPort {
				t.Errorf("general.port = %d, want %d", got, tc.wantPort)
			}
			if got := Config.String("general.update_time"); got != tc.wantUpdateTime {
				t.Errorf("general.update_time = %q, want %q", got, tc.wantUpdateTime)
			}
			if got := Config.String("log.default"); got != "info" {
				t.Errorf("log.default = %q, want %q", got, "info")
			}
		})
	}
}

// A bad configuration must be fatal: the daemon used to log the error and
// carry on with an empty bind address, i.e. listening on every interface.
func TestInitConfigErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		// file, when false, means the config file is not created at all.
		file    bool
		wantErr string
	}{
		{
			name:    "missing config file",
			file:    false,
			wantErr: "failed to load config file",
		},
		{
			name:    "malformed toml",
			file:    true,
			content: "[general\nport = ",
			wantErr: "failed to load config file",
		},
		{
			name:    "empty address",
			file:    true,
			content: "[general]\naddress = \"\"\n",
			wantErr: "general.address is empty",
		},
		{
			name:    "address with whitespace",
			file:    true,
			content: "[general]\naddress = \"127.0.0.1 \"\n",
			wantErr: "contains whitespace",
		},
		{
			name:    "port out of range",
			file:    true,
			content: "[general]\nport = 70000\n",
			wantErr: "out of range",
		},
		{
			name:    "port zero",
			file:    true,
			content: "[general]\nport = 0\n",
			wantErr: "out of range",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conf := filepath.Join(t.TempDir(), "missing.toml")
			if tc.file {
				conf = writeConf(t, tc.content)
			}

			err := InitConfig(&conf)
			if err == nil {
				t.Fatalf("InitConfig(%q): expected an error, got none", conf)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("InitConfig error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestInitConfigNoFilename(t *testing.T) {
	empty := ""

	if err := InitConfig(nil); err == nil {
		t.Error("InitConfig(nil): expected an error, got none")
	}
	if err := InitConfig(&empty); err == nil {
		t.Error("InitConfig(\"\"): expected an error, got none")
	}
}

func TestInitConfigEnvOverride(t *testing.T) {
	t.Setenv("CALAOS_GENERAL_ADDRESS", "127.0.0.2")

	conf := writeConf(t, "[general]\naddress = \"127.0.0.1\"\n")
	if err := InitConfig(&conf); err != nil {
		t.Fatalf("InitConfig: unexpected error: %v", err)
	}

	if got := Config.String("general.address"); got != "127.0.0.2" {
		t.Errorf("general.address = %q, want %q (env must win over the file)", got, "127.0.0.2")
	}
}
