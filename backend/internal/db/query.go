package db

import (
	"context"

	"webhook/internal/model"
)

type DeliveryFilter struct {
	Status     string
	EndpointID int64
	Limit      int
}

func (s *Store) ListDeliveries(ctx context.Context, f DeliveryFilter) ([]*model.Delivery, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.endpoint_id, d.event_id, d.status, d.attempts, d.max_attempts,
		       d.not_before, d.leased_at, d.leased_until, d.leased_by,
		       d.error_kind, d.error_detail, d.created_at, d.updated_at,
		       COALESCE(qp.pos, 0),
		       CASE
		         WHEN d.status='pending' AND qp.pos > 1 THEN 'head_of_line'
		         ELSE ''
		       END AS blocked
		FROM deliveries d
		LEFT JOIN LATERAL (
			SELECT COUNT(*) + 1 AS pos
			FROM deliveries p
			JOIN events pe ON pe.id = p.event_id
			JOIN events ce ON ce.id = d.event_id
			WHERE pe.order_key IS NOT NULL
			  AND p.endpoint_id = d.endpoint_id
			  AND pe.order_key = ce.order_key
			  AND (p.created_at, p.id) <= (d.created_at, d.id)
			  AND p.status <> 'succeeded'
		) qp ON TRUE
		WHERE ($1 = '' OR d.status = $1)
		  AND ($2 = 0 OR d.endpoint_id = $2)
		ORDER BY d.id DESC
		LIMIT $3`,
		f.Status, f.EndpointID, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDelivery(ctx context.Context, id int64) (*model.Delivery, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT d.id, d.endpoint_id, d.event_id, d.status, d.attempts, d.max_attempts,
		       d.not_before, d.leased_at, d.leased_until, d.leased_by,
		       d.error_kind, d.error_detail, d.created_at, d.updated_at,
		       0, ''
		FROM deliveries d WHERE d.id=$1`, id)
	return scanDelivery(row)
}

func (s *Store) ListAttempts(ctx context.Context, deliveryID int64) ([]*model.Attempt, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, delivery_id, attempt_no, status, http_status,
		       error_kind, error_detail, started_at, finished_at, duration_ms, worker_id
		FROM delivery_attempts
		WHERE delivery_id=$1
		ORDER BY attempt_no`, deliveryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Attempt
	for rows.Next() {
		var a model.Attempt
		if err := rows.Scan(&a.ID, &a.DeliveryID, &a.AttemptNo, &a.Status, &a.HTTPStatus,
			&a.ErrorKind, &a.ErrorDetail, &a.StartedAt, &a.FinishedAt, &a.DurationMS, &a.WorkerID); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

type Stats struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	InFlight  int `json:"in_flight"`
	Succeeded int `json:"succeeded"`
	Dead      int `json:"dead"`
}

func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	var st Stats
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status='pending'),
		       count(*) FILTER (WHERE status='in_flight'),
		       count(*) FILTER (WHERE status='succeeded'),
		       count(*) FILTER (WHERE status='dead')
		FROM deliveries`).
		Scan(&st.Total, &st.Pending, &st.InFlight, &st.Succeeded, &st.Dead)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// HeadInfo describes what blocks a pending ordered delivery, for the UI.
type HeadInfo struct {
	HeadDeliveryID int64  `json:"head_delivery_id"`
	HeadStatus     string `json:"head_status"`
}

func scanDelivery(r rowScanner) (*model.Delivery, error) {
	var d model.Delivery
	var pos int64
	var blocked string
	if err := r.Scan(&d.ID, &d.EndpointID, &d.EventID, &d.Status, &d.Attempts, &d.MaxAttempts,
		&d.NotBefore, &d.LeasedAt, &d.LeasedUntil, &d.LeasedBy,
		&d.ErrorKind, &d.ErrorDetail, &d.CreatedAt, &d.UpdatedAt, &pos, &blocked); err != nil {
		return nil, err
	}
	d.QueuePosition = int(pos)
	d.BlockedReason = blocked
	return &d, nil
}
