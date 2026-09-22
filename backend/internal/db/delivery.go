package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"webhook/internal/model"
)

// Claimed is one delivery a worker has leased, together with everything
// needed to perform the HTTP call.
type Claimed struct {
	Delivery *model.Delivery
	Endpoint *model.Endpoint
	Event    *model.Event
	OrderKey *string
}

// ClaimNext atomically leases up to limit deliveries that are due.
//
// Ordering guarantee: for each endpoint, deliveries with the same non-empty
// order_key are processed strictly FIFO. The query locks candidate rows
// (FOR UPDATE SKIP LOCKED) and skips a candidate whenever an earlier
// delivery for the same (endpoint_id, order_key) is not yet succeeded.
// Different keys and key-less deliveries never block each other.
func (s *Store) ClaimNext(ctx context.Context, workerID string, lease time.Duration, limit int) ([]*Claimed, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		WITH candidates AS (
			SELECT d.*
			FROM deliveries d
			WHERE d.status = 'pending'
			  AND d.leased_until IS NULL
			  AND d.not_before <= now()
			ORDER BY d.created_at, d.id
			LIMIT $3::int
			FOR UPDATE OF d SKIP LOCKED
		), params AS (SELECT $1::text AS worker, $2::double precision AS lease)
		SELECT c.id, c.endpoint_id, c.event_id, c.max_attempts, e.order_key
		FROM candidates c
		CROSS JOIN params
		JOIN events e ON e.id = c.event_id
		WHERE e.order_key IS NULL
		   OR NOT EXISTS (
				SELECT 1 FROM deliveries p
				JOIN events pe ON pe.id = p.event_id
				WHERE p.endpoint_id = c.endpoint_id
				  AND pe.order_key = e.order_key
				  AND (p.created_at, p.id) < (c.created_at, c.id)
				  AND p.status <> 'succeeded'
		   )`,
		workerID, lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	type idRow struct {
		id, endpointID, eventID, maxAttempts int64
	}
	var ids []idRow
	var orderKeys []*string
	for rows.Next() {
		var r idRow
		var ok *string
		if err := rows.Scan(&r.id, &r.endpointID, &r.eventID, &r.maxAttempts, &ok); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, r)
		orderKeys = append(orderKeys, ok)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	claimed := make([]*Claimed, 0, len(ids))
	for i, r := range ids {
		var d model.Delivery
		err := tx.QueryRow(ctx, `
			UPDATE deliveries
			SET status='in_flight', leased_at=now(),
			    leased_until=now() + make_interval(secs => $2::double precision),
			    leased_by=$3, updated_at=now()
			WHERE id=$1
			RETURNING id, endpoint_id, event_id, status, attempts, max_attempts,
			          not_before, leased_at, leased_until, leased_by,
			          error_kind, error_detail, created_at, updated_at`,
			r.id, lease.Seconds(), workerID).
			Scan(&d.ID, &d.EndpointID, &d.EventID, &d.Status, &d.Attempts, &d.MaxAttempts,
				&d.NotBefore, &d.LeasedAt, &d.LeasedUntil, &d.LeasedBy,
				&d.ErrorKind, &d.ErrorDetail, &d.CreatedAt, &d.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO delivery_attempts
			    (delivery_id, attempt_no, status, started_at, worker_id)
			VALUES ($1, $2, 'timeout', now(), $3)`,
			d.ID, d.Attempts+1, workerID); err != nil {
			return nil, err
		}
		ep, err := getEndpointTx(ctx, tx, d.EndpointID)
		if err != nil {
			return nil, err
		}
		ev, err := getEventTx(ctx, tx, d.EventID)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, &Claimed{Delivery: &d, Endpoint: ep, Event: ev, OrderKey: orderKeys[i]})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claimed, nil
}

func getEndpointTx(ctx context.Context, tx pgx.Tx, id int64) (*model.Endpoint, error) {
	row := tx.QueryRow(ctx,
		`SELECT id, url, secret, events, active, description, created_at FROM endpoints WHERE id=$1`, id)
	return scanEndpoint(row)
}

func getEventTx(ctx context.Context, tx pgx.Tx, id int64) (*model.Event, error) {
	var ev model.Event
	err := tx.QueryRow(ctx,
		`SELECT id, event_type, payload, order_key, created_at FROM events WHERE id=$1`, id).
		Scan(&ev.ID, &ev.EventType, &ev.Payload, &ev.OrderKey, &ev.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &ev, nil
}
