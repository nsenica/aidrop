package config

import (
	"strings"
	"testing"
)

func TestLoadUsesLoopbackDefaultsAndRequiresToken(t *testing.T) {
	cfg, err := load(lookupEnv(map[string]string{
		EnvBearerToken: "test-secret",
	}))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:6420" {
		t.Errorf("listen address = %q, want loopback default", cfg.ListenAddr)
	}
	if cfg.DataDir != "./data" {
		t.Errorf("data directory = %q, want ./data", cfg.DataDir)
	}

	_, err = load(lookupEnv(nil))
	if err == nil || !strings.Contains(err.Error(), EnvBearerToken) {
		t.Fatalf("load without token error = %v, want required-token error", err)
	}
}

func TestLoadAcceptsConfiguredValues(t *testing.T) {
	cfg, err := load(lookupEnv(map[string]string{
		EnvListenAddr:     "127.0.0.1:8081",
		EnvDataDir:        "/var/lib/aidrop",
		EnvBearerToken:    "secret-value",
		EnvAllowedOrigins: "HTTPS://Aidrop.Example:443, http://localhost:3000",
	}))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8081" || cfg.DataDir != "/var/lib/aidrop" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	wantOrigins := []string{"https://aidrop.example", "http://localhost:3000"}
	if len(cfg.AllowedOrigins) != len(wantOrigins) {
		t.Fatalf("allowed origins = %v, want %v", cfg.AllowedOrigins, wantOrigins)
	}
	for i := range wantOrigins {
		if cfg.AllowedOrigins[i] != wantOrigins[i] {
			t.Errorf("allowed origin %d = %q, want %q", i, cfg.AllowedOrigins[i], wantOrigins[i])
		}
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "malformed listen address",
			env:  map[string]string{EnvListenAddr: "not-an-address", EnvBearerToken: "secret"},
			want: EnvListenAddr,
		},
		{
			name: "invalid port",
			env:  map[string]string{EnvListenAddr: "localhost:70000", EnvBearerToken: "secret"},
			want: EnvListenAddr,
		},
		{
			name: "empty data directory",
			env:  map[string]string{EnvDataDir: " ", EnvBearerToken: "secret"},
			want: EnvDataDir,
		},
		{
			name: "empty token",
			env:  map[string]string{EnvBearerToken: "  "},
			want: EnvBearerToken,
		},
		{
			name: "token with whitespace",
			env:  map[string]string{EnvBearerToken: "secret value"},
			want: EnvBearerToken,
		},
		{
			name: "origin with path",
			env:  map[string]string{EnvBearerToken: "secret", EnvAllowedOrigins: "https://aidrop.example/mcp"},
			want: EnvAllowedOrigins,
		},
		{
			name: "origin with unsupported scheme",
			env:  map[string]string{EnvBearerToken: "secret", EnvAllowedOrigins: "ftp://aidrop.example"},
			want: EnvAllowedOrigins,
		},
		{
			name: "empty value in origin list",
			env:  map[string]string{EnvBearerToken: "secret", EnvAllowedOrigins: "https://aidrop.example,"},
			want: EnvAllowedOrigins,
		},
		{
			name: "wildcard origin",
			env:  map[string]string{EnvBearerToken: "secret", EnvAllowedOrigins: "https://*"},
			want: EnvAllowedOrigins,
		},
		{
			name: "origin port out of range",
			env:  map[string]string{EnvBearerToken: "secret", EnvAllowedOrigins: "https://aidrop.example:70000"},
			want: EnvAllowedOrigins,
		},
		{
			name: "empty fragment delimiter",
			env:  map[string]string{EnvBearerToken: "secret", EnvAllowedOrigins: "https://aidrop.example#"},
			want: EnvAllowedOrigins,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(lookupEnv(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("load error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func lookupEnv(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
