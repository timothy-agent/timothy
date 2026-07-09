// Package config loads per-service runtime configuration from the
// environment.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Service holds the configuration shared by every Timothy service.
// Only DATABASE_URL may be empty: the service then starts degraded and
// keeps retrying instead of exiting.
type Service struct {
	Name        string
	Port        int
	DatabaseURL string
	LogLevel    string
}

// Load reads the environment for the named service, applying defaults
// and validating what it finds.
func Load(name string, defaultPort int) (Service, error) {
	if name == "" {
		return Service{}, fmt.Errorf("config: service name is required")
	}

	port := defaultPort
	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return Service{}, fmt.Errorf("config: invalid PORT %q: %w", v, err)
		}
		port = p
	}
	if port < 1 || port > 65535 {
		return Service{}, fmt.Errorf("config: port %d out of range", port)
	}

	level := os.Getenv("LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	switch level {
	case "debug", "info", "warn", "error":
	default:
		return Service{}, fmt.Errorf("config: invalid LOG_LEVEL %q", level)
	}

	return Service{
		Name:        name,
		Port:        port,
		DatabaseURL: os.Getenv("DATABASE_URL"),
		LogLevel:    level,
	}, nil
}
