package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
)

const (
	EnvListenAddr     = "AIDROP_LISTEN_ADDR"
	EnvDataDir        = "AIDROP_DATA_DIR"
	EnvBearerToken    = "AIDROP_BEARER_TOKEN"
	EnvAllowedOrigins = "AIDROP_ALLOWED_ORIGINS"

	defaultListenAddr = "127.0.0.1:6420"
	defaultDataDir    = "./data"
)

// Config contains the settings needed to start the HTTP service.
type Config struct {
	ListenAddr     string
	DataDir        string
	BearerToken    string
	AllowedOrigins []string
}

// LoadFromEnv reads and validates the process environment.
func LoadFromEnv() (Config, error) {
	return load(func(key string) (string, bool) {
		value, ok := os.LookupEnv(key)
		return value, ok
	})
}

func load(lookup func(string) (string, bool)) (Config, error) {
	listenAddr := defaultListenAddr
	if value, ok := lookup(EnvListenAddr); ok {
		listenAddr = value
	}
	dataDir := defaultDataDir
	if value, ok := lookup(EnvDataDir); ok {
		dataDir = value
	}
	token, _ := lookup(EnvBearerToken)
	allowedOriginsValue, _ := lookup(EnvAllowedOrigins)
	allowedOrigins, err := parseAllowedOrigins(allowedOriginsValue)
	if err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", EnvAllowedOrigins, err)
	}

	cfg := Config{
		ListenAddr:     listenAddr,
		DataDir:        dataDir,
		BearerToken:    token,
		AllowedOrigins: allowedOrigins,
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks configuration before the server opens its listener.
func (c Config) Validate() error {
	if err := validateListenAddr(c.ListenAddr); err != nil {
		return fmt.Errorf("invalid %s: %w", EnvListenAddr, err)
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("%s must not be empty", EnvDataDir)
	}
	if strings.TrimSpace(c.BearerToken) == "" {
		return fmt.Errorf("%s is required", EnvBearerToken)
	}
	if strings.IndexFunc(c.BearerToken, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%s must not contain whitespace", EnvBearerToken)
	}
	if err := validateAllowedOrigins(c.AllowedOrigins); err != nil {
		return fmt.Errorf("invalid %s: %w", EnvAllowedOrigins, err)
	}
	return nil
}

func parseAllowedOrigins(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	origins := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		canonical, err := NormalizeOrigin(origin)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		origins = append(origins, canonical)
	}
	return origins, nil
}

func validateAllowedOrigins(origins []string) error {
	for _, origin := range origins {
		if _, err := NormalizeOrigin(origin); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeOrigin validates and canonicalizes one HTTP or HTTPS origin.
func NormalizeOrigin(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(value, "#") {
		return "", fmt.Errorf("%q must be an origin without credentials, path, query, or fragment", value)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%q must use http or https", value)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || strings.Contains(host, "*") || strings.HasSuffix(parsed.Host, ":") {
		return "", fmt.Errorf("%q must include a valid host", value)
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", fmt.Errorf("%q must include a valid port", parsed.Host)
		}
		defaultPort := (scheme == "http" && portNumber == 80) || (scheme == "https" && portNumber == 443)
		if !defaultPort {
			host += ":" + strconv.Itoa(portNumber)
		}
	}
	return scheme + "://" + host, nil
}

func validateListenAddr(addr string) error {
	if strings.TrimSpace(addr) == "" || strings.IndexFunc(addr, unicode.IsSpace) >= 0 {
		return fmt.Errorf("must be a host:port address")
	}
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("must be a host:port address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}
