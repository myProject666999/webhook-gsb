package db

import (
	"context"
	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"

	"webhook/internal/model"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close()              { s.pool.Close() }
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, schemaSQL)
	return err
}

func (s *Store) CreateEndpoint(ctx context.Context, e *model.Endpoint) (*model.Endpoint, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO endpoints (url, secret, events, active, description)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, url, secret, events, active, description, created_at`,
		e.URL, e.Secret, e.Events, e.Active, e.Description)
	return scanEndpoint(row)
}

func (s *Store) UpdateEndpoint(ctx context.Context, e *model.Endpoint) (*model.Endpoint, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE endpoints SET url=$2, secret=$3, events=$4, active=$5, description=$6
		WHERE id=$1
		RETURNING id, url, secret, events, active, description, created_at`,
		e.ID, e.URL, e.Secret, e.Events, e.Active, e.Description)
	return scanEndpoint(row)
}

func (s *Store) GetEndpoint(ctx context.Context, id int64) (*model.Endpoint, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, url, secret, events, active, description, created_at FROM endpoints WHERE id=$1`, id)
	return scanEndpoint(row)
}

func (s *Store) ListEndpoints(ctx context.Context) ([]*model.Endpoint, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, url, secret, events, active, description, created_at FROM endpoints ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Endpoint
	for rows.Next() {
		e, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) DeleteEndpoint(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM endpoints WHERE id=$1`, id)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEndpoint(r rowScanner) (*model.Endpoint, error) {
	var e model.Endpoint
	if err := r.Scan(&e.ID, &e.URL, &e.Secret, &e.Events, &e.Active, &e.Description, &e.CreatedAt); err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) CreateEventAndFanout(ctx context.Context, ev *model.Event, defaultMaxAttempts int) (int64, int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	row := tx.QueryRow(ctx, `
		INSERT INTO events (event_type, payload, order_key)
		VALUES ($1, $2, $3) RETURNING id, created_at`,
		ev.EventType, ev.Payload, ev.OrderKey)
	if err := row.Scan(&ev.ID, &ev.CreatedAt); err != nil {
		return 0, 0, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO deliveries (endpoint_id, event_id, max_attempts)
		SELECT e.id, $1, $3
		FROM endpoints e
		WHERE e.active = TRUE AND $2 = ANY(e.events)`,
		ev.ID, ev.EventType, defaultMaxAttempts)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return ev.ID, int(tag.RowsAffected()), nil
}

func (s *Store) GetEvent(ctx context.Context, id int64) (*model.Event, error) {
	var ev model.Event
	err := s.pool.QueryRow(ctx,
		`SELECT id, event_type, payload, order_key, created_at FROM events WHERE id=$1`, id).
		Scan(&ev.ID, &ev.EventType, &ev.Payload, &ev.OrderKey, &ev.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &ev, nil
}

func (s *Store) ListEvents(ctx context.Context, limit int) ([]*model.Event, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, event_type, payload, order_key, created_at FROM events ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Event
	for rows.Next() {
		var ev model.Event
		if err := rows.Scan(&ev.ID, &ev.EventType, &ev.Payload, &ev.OrderKey, &ev.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &ev)
	}
	return out, rows.Err()
}
