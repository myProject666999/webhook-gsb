// Package testsupport provides a real PostgreSQL database for integration
// tests. It either launches an in-process embedded PostgreSQL, or connects
// to TEST_DATABASE_URL and isolates each test in its own schema.
package testsupport

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"testing"
	"time"

	embedded "github.com/fergusstrange/embedded-postgres"

	"webhook/internal/config"
	"webhook/internal/db"
)

func NewStore(t *testing.T) *db.Store {
	t.Helper()
	dsn := dsn(t)
	ctx := context.Background()
	st, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func Config() config.Config {
	cfg := config.Load()
	cfg.WorkerID = "worker-test"
	cfg.WorkerCount = 4
	cfg.PollInterval = 20 * time.Millisecond
	cfg.LeaseDuration = 3 * time.Second
	cfg.AttemptTimeout = 2 * time.Second
	cfg.BackoffBase = 20 * time.Millisecond
	cfg.BackoffMax = 500 * time.Millisecond
	cfg.DefaultMaxAttempts = 4
	cfg.SignatureWindow = 5 * time.Minute
	return cfg
}

func dsn(t *testing.T) string {
	if base := os.Getenv("TEST_DATABASE_URL"); base != "" {
		schema := "t_" + regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(t.Name(), "_")
		schema = regexp.MustCompile(`_+`).ReplaceAllString(schema, "_")
		ctx := context.Background()
		admin, err := db.New(ctx, base)
		if err != nil {
			t.Fatalf("connect admin: %v", err)
		}
		_, _ = admin.Pool().Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		if _, err := admin.Pool().Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		admin.Close()
		t.Cleanup(func() {
			c, err := db.New(context.Background(), base)
			if err == nil {
				_, _ = c.Pool().Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
				c.Close()
			}
		})
		u, err := url.Parse(base)
		if err != nil {
			t.Fatalf("parse TEST_DATABASE_URL: %v", err)
		}
		q := u.Query()
		existing := q.Get("search_path")
		if existing != "" {
			q.Set("search_path", schema+","+existing)
		} else {
			q.Set("search_path", schema)
		}
		u.RawQuery = q.Encode()
		return u.String()
	}

	port := 25432 + (int(time.Now().UnixNano()) % 2000)
	dataDir := t.TempDir()
	ep := embedded.NewDatabase(embedded.DefaultConfig().
		Version(embedded.V16).
		Port(uint32(port)).
		Database("webhooktest").
		Username("webhook").
		Password("webhook").
		RuntimePath(t.TempDir()).
		DataPath(dataDir),
	)
	if err := ep.Start(); err != nil {
		t.Fatalf("start embedded postgres: %v", err)
	}
	t.Cleanup(func() { _ = ep.Stop() })
	return fmt.Sprintf("postgres://webhook:webhook@127.0.0.1:%d/webhooktest?sslmode=disable", port)
}
