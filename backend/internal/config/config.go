package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr           string
	DatabaseURL        string
	WorkerID           string
	WorkerCount        int
	PollInterval       time.Duration
	AttemptTimeout     time.Duration
	LeaseDuration      time.Duration
	BackoffBase        time.Duration
	BackoffMax         time.Duration
	DefaultMaxAttempts int
	SignatureWindow    time.Duration
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func Load() Config {
	host, _ := os.Hostname()
	return Config{
		HTTPAddr:           env("HTTP_ADDR", ":8080"),
		DatabaseURL:        env("DATABASE_URL", "postgres://webhook:webhook@localhost:5432/webhook?sslmode=disable"),
		WorkerID:           env("WORKER_ID", host+"-"+strconv.Itoa(os.Getpid())),
		WorkerCount:        envInt("WORKER_CONCURRENCY", 4),
		PollInterval:       envDur("POLL_INTERVAL", 500*time.Millisecond),
		AttemptTimeout:     envDur("ATTEMPT_TIMEOUT", 20*time.Second),
		LeaseDuration:      envDur("LEASE_DURATION", 45*time.Second),
		BackoffBase:        envDur("BACKOFF_BASE", 2*time.Second),
		BackoffMax:         envDur("BACKOFF_MAX", 10*time.Minute),
		DefaultMaxAttempts: envInt("DEFAULT_MAX_ATTEMPTS", 6),
		SignatureWindow:    envDur("SIGNATURE_WINDOW", 5*time.Minute),
	}
}
