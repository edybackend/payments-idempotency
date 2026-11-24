// Package config reads the service settings from environment variables, with defaults for local runs.
package config

import (
	"os"
	"time"
)

// Config holds the service settings.
type Config struct {
	HTTPAddr         string
	DBPath           string
	AcquirerDBPath   string
	LockTimeout      time.Duration
	KeyTTL           time.Duration
	AcquirerLatency  time.Duration
	AcquirerTimeout  time.Duration
	JanitorFrequency time.Duration
}

// Load reads the configuration from the environment.
func Load() Config {
	return Config{
		HTTPAddr:         env("HTTP_ADDR", ":8080"),
		DBPath:           env("DB_PATH", "data/payments.db"),
		AcquirerDBPath:   env("ACQUIRER_DB_PATH", "data/acquirer.db"),
		LockTimeout:      duration("LOCK_TIMEOUT", 30*time.Second),
		KeyTTL:           duration("KEY_TTL", 24*time.Hour),
		AcquirerLatency:  duration("ACQUIRER_LATENCY", 400*time.Millisecond),
		AcquirerTimeout:  duration("ACQUIRER_TIMEOUT", 5*time.Second),
		JanitorFrequency: duration("JANITOR_FREQUENCY", 10*time.Minute),
	}
}

func env(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}
	return fallback
}

func duration(name string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(name)); err == nil && d > 0 {
		return d
	}
	return fallback
}
