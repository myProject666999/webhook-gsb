// Package worker runs the delivery loop: claim due deliveries, perform the
// HTTP call, and commit the outcome under lease ownership.
package worker

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"webhook/internal/config"
	"webhook/internal/db"
	"webhook/internal/deliver"
)

type Executor interface {
	Send(ctx context.Context, c *db.Claimed) db.Result
}

type Worker struct {
	store   *db.Store
	exec    Executor
	id      string
	conc    int
	poll    time.Duration
	lease   time.Duration
	base    time.Duration
	maxWait time.Duration
	wake    chan struct{}
	wg      sync.WaitGroup
}

func New(st *db.Store, cfg config.Config) *Worker {
	return &Worker{
		store:   st,
		exec:    deliver.NewSender(cfg.AttemptTimeout),
		id:      cfg.WorkerID,
		conc:    cfg.WorkerCount,
		poll:    cfg.PollInterval,
		lease:   cfg.LeaseDuration,
		base:    cfg.BackoffBase,
		maxWait: cfg.BackoffMax,
		wake:    make(chan struct{}, 1),
	}
}

// WithExecutor replaces the HTTP executor (used in tests).
func (w *Worker) WithExecutor(e Executor) *Worker { w.exec = e; return w }

func (w *Worker) ID() string { return w.id }

// Wake nudges the dispatcher to re-scan immediately (e.g. after a new event).
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *Worker) tick(ctx context.Context) {
	// Lease housekeeping first, then claim.
	if n, err := w.store.ReapExpiredLeases(ctx); err != nil {
		log.Printf("worker=%s reap error: %v", w.id, err)
	} else if n > 0 {
		log.Printf("worker=%s took over %d expired lease(s)", w.id, n)
	}
	claimed, err := w.store.ClaimNext(ctx, w.id, w.lease, w.conc)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("worker=%s claim error: %v", w.id, err)
		}
		return
	}
	for _, c := range claimed {
		w.wg.Add(1)
		go w.process(ctx, c)
	}
}

func (w *Worker) process(ctx context.Context, c *db.Claimed) {
	defer w.wg.Done()
	res := w.exec.Send(ctx, c)
	if res.AttemptNo == 0 {
		res.AttemptNo = c.Delivery.Attempts + 1
	}
	// Commits use a detached context: an HTTP call that completes just as
	// the worker is shutting down must still finish its lease transaction,
	// otherwise the delivery waits until lease expiry to be taken over.
	w.commit(context.Background(), c, res)
}

// Wait blocks until all in-flight process goroutines have committed.
func (w *Worker) Wait() { w.wg.Wait() }

func (w *Worker) commit(ctx context.Context, c *db.Claimed, r db.Result) {
	switch r.Status {
	case "succeeded":
		if err := w.store.Succeed(ctx, c.Delivery.ID, r.AttemptNo, w.id, r); err != nil {
			if err == db.ErrLeaseLost {
				log.Printf("worker=%s delivery=%d late success after lease takeover (at-least-once duplicate possible)", w.id, c.Delivery.ID)
			} else {
				log.Printf("worker=%s delivery=%d succeed commit error: %v", w.id, c.Delivery.ID, err)
			}
		}
	default:
		wait := r.RetryAfterHint
		if wait <= 0 {
			wait = deliver.Backoff(w.base, w.maxWait, r.AttemptNo, rand.Float64())
		}
		dead, err := w.store.Fail(ctx, c.Delivery.ID, r.AttemptNo, w.id, r, wait, c.Delivery.MaxAttempts)
		if err != nil {
			if err == db.ErrLeaseLost {
				log.Printf("worker=%s delivery=%d late failure after lease takeover, takeover result wins", w.id, c.Delivery.ID)
			} else {
				log.Printf("worker=%s delivery=%d fail commit error: %v", w.id, c.Delivery.ID, err)
			}
			return
		}
		if dead {
			log.Printf("worker=%s delivery=%d moved to dead letter (%s: %s)",
				w.id, c.Delivery.ID, r.ErrorKind, r.ErrorDetail)
		} else {
			log.Printf("worker=%s delivery=%d attempt %d failed (%s), retry in %s",
				w.id, c.Delivery.ID, r.AttemptNo, r.ErrorKind, wait.Round(time.Millisecond))
		}
	}
	w.Wake()
}

// Run blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	log.Printf("worker %s starting: concurrency=%d lease=%s", w.id, w.conc, w.lease)
	t := time.NewTicker(w.poll)
	defer t.Stop()
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Printf("worker %s stopping", w.id)
			return
		case <-t.C:
			w.tick(ctx)
		case <-w.wake:
			w.tick(ctx)
		}
	}
}

// RunOnce performs one reap+claim+process cycle and waits for processing to
// finish. Intended for deterministic tests.
func (w *Worker) RunOnce(ctx context.Context) {
	w.tick(ctx)
	w.wg.Wait()
}
