package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"webhook/internal/model"
)

// ErrLeaseLost is returned when a worker no longer owns the delivery lease
// (another worker took over after the lease expired).
var ErrLeaseLost = errors.New("delivery lease lost to another worker")

// Result is the outcome of one HTTP attempt.
type Result struct {
	AttemptNo   int
	Status      string // succeeded | http_error | network_error | timeout | rejected
	HTTPStatus  int    // 0 when unavailable
	ErrorKind   string
	ErrorDetail string
	DurationMS  int
	// RetryAfterHint, when positive, overrides the computed backoff.
	RetryAfterHint time.Duration
}

// Succeed marks a delivery succeeded, but only while the worker still owns
// the lease. A failed ownership check means another worker already took
// over, so this attempt cannot be the one that decides the outcome.
func (s *Store) Succeed(ctx context.Context, deliveryID int64, attemptNo int, workerID string, r Result) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE deliveries
		SET status='succeeded', attempts=$2, error_kind=NULL, error_detail=NULL,
		    leased_until=NULL, updated_at=now()
		WHERE id=$1 AND status='in_flight' AND leased_by=$3`,
		deliveryID, attemptNo, workerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Ownership lost: record the late result, caller treats as lost.
		_, _ = tx.Exec(ctx, `
			UPDATE delivery_attempts
			SET status='lost_lease', http_status=$4, duration_ms=$5, finished_at=now(),
			    error_kind=$6, error_detail=$7, worker_id=$3
			WHERE delivery_id=$1 AND attempt_no=$2`,
			deliveryID, attemptNo, workerID, nilIfZero(r.HTTPStatus), r.DurationMS,
			model.KindLeaseLost, r.ErrorDetail)
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return ErrLeaseLost
	}
	if err := finishAttempt(ctx, tx, deliveryID, attemptNo, workerID, "succeeded", r); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Fail records a failed attempt and either schedules a retry or moves the
// delivery to the dead letter queue. Lease ownership is guarded the same
// way as in Succeed.
func (s *Store) Fail(ctx context.Context, deliveryID int64, attemptNo int, workerID string, r Result, backoff time.Duration, maxAttempts int) (dead bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	target := "pending"
	next := attemptNo + 1
	if next > maxAttempts || r.Status == "rejected" {
		target = "dead"
	}
	nb := time.Now().Add(backoff)
	if r.RetryAfterHint > 0 {
		hint := time.Now().Add(r.RetryAfterHint)
		if hint.After(nb) {
			nb = hint
		}
	}

	tag, err := tx.Exec(ctx, `
		UPDATE deliveries
		SET status=$4, attempts=$2, not_before=$5,
		    leased_at=NULL, leased_until=NULL, leased_by=NULL,
		    error_kind=$6, error_detail=$7, updated_at=now()
		WHERE id=$1 AND status='in_flight' AND leased_by=$3`,
		deliveryID, attemptNo, workerID, target, nb, r.ErrorKind, truncate(r.ErrorDetail))
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		_, _ = tx.Exec(ctx, `
			UPDATE delivery_attempts
			SET status='lost_lease', http_status=$4, duration_ms=$5, finished_at=now(),
			    error_kind=$6, error_detail=$7, worker_id=$3
			WHERE delivery_id=$1 AND attempt_no=$2`,
			deliveryID, attemptNo, workerID, nilIfZero(r.HTTPStatus), r.DurationMS,
			model.KindLeaseLost, r.ErrorDetail)
		if err := tx.Commit(ctx); err != nil {
			return false, ErrLeaseLost
		}
		return false, ErrLeaseLost
	}
	if err := finishAttempt(ctx, tx, deliveryID, attemptNo, workerID, r.Status, r); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return target == "dead", nil
}

// attemptRowStatus maps a Result status onto the delivery_attempts enum.
func attemptRowStatus(resultStatus string) string {
	switch resultStatus {
	case "succeeded":
		return "succeeded"
	case "timeout":
		return "timeout"
	case "network_error":
		return "network_error"
	case "rejected":
		return "rejected"
	default: // http_error, bad_response ...
		return "http_error"
	}
}

func finishAttempt(ctx context.Context, tx pgx.Tx, deliveryID int64, attemptNo int, workerID, status string, r Result) error {
	_, err := tx.Exec(ctx, `
		UPDATE delivery_attempts
		SET status=$3, http_status=$4, duration_ms=$5, finished_at=now(),
		    error_kind=$6, error_detail=$7, worker_id=$8
		WHERE delivery_id=$1 AND attempt_no=$2`,
		deliveryID, attemptNo, attemptRowStatus(status), nilIfZero(r.HTTPStatus), r.DurationMS,
		nilIfEmpty(r.ErrorKind), truncate(r.ErrorDetail), workerID)
	return err
}

// ReapExpiredLeases releases attempts whose lease has expired: the attempt
// row is marked timeout and the delivery goes back to pending with
// not_before=now, so any worker can immediately take it over.
func (s *Store) ReapExpiredLeases(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		WITH expired AS (
			SELECT d.id, d.attempts
			FROM deliveries d
			WHERE d.status='in_flight' AND d.leased_until < now()
			FOR UPDATE SKIP LOCKED
		), upd AS (
			UPDATE deliveries d
			SET status='pending', attempts=d.attempts+1, not_before=now(), leased_until=NULL,
			    error_kind=$1,
			    error_detail='lease expired while held by ' || COALESCE(d.leased_by,'?'),
			    updated_at=now()
			FROM expired e WHERE d.id = e.id
			RETURNING d.id, d.attempts
		)
		UPDATE delivery_attempts a
		SET status='timeout', finished_at=now(),
		    error_kind=$1, error_detail='lease expired before worker reported a result'
		FROM upd
		WHERE a.delivery_id = upd.id AND a.attempt_no = upd.attempts`,
		model.KindLeaseTaken)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// Redrive resets a dead delivery for another full round of attempts.
func (s *Store) Redrive(ctx context.Context, id int64) (*model.Delivery, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE deliveries
		SET status='pending', attempts=0, not_before=now(),
		    leased_at=NULL, leased_until=NULL, leased_by=NULL,
		    error_kind=NULL, error_detail=NULL, updated_at=now()
		WHERE id=$1 AND status='dead'
		RETURNING id, endpoint_id, event_id, status, attempts, max_attempts,
		          not_before, leased_at, leased_until, leased_by,
		          error_kind, error_detail, created_at, updated_at, 0, ''`, id)
	d, err := scanDelivery(row)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func nilIfZero(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func truncate(s string) *string {
	if s == "" {
		return nil
	}
	if len(s) > 2000 {
		s = s[:2000]
	}
	return &s
}
