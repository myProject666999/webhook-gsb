package worker_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"webhook/internal/db"
	"webhook/internal/model"
	"webhook/internal/testsupport"
	"webhook/internal/worker"
)

type seqExec struct {
	mu        sync.Mutex
	failFirst int32 // number of initial attempts per delivery that fail 500
	calls     map[int64]int32
}

func (e *seqExec) Send(ctx context.Context, c *db.Claimed) db.Result {
	e.mu.Lock()
	n := e.calls[c.Delivery.ID] + 1
	e.calls[c.Delivery.ID] = n
	failFirst := e.failFirst
	e.mu.Unlock()
	r := db.Result{AttemptNo: c.Delivery.Attempts + 1, ErrorKind: model.KindHTTP5xx,
		ErrorDetail: "HTTP 500: injected"}
	if n > failFirst {
		r = db.Result{AttemptNo: c.Delivery.Attempts + 1, Status: "succeeded", HTTPStatus: 200}
	}
	return r
}

// Fail twice with 5xx (retried with backoff), then succeed: at-least-once.
func TestRetry_RecoversAfter5xx(t *testing.T) {
	st := testsupport.NewStore(t)
	mkEndpoint(t, st, "http://example.test/hook")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ev, n := ingest(t, st, "invoice.paid", "k1")
	if n != 1 {
		t.Fatalf("fanout=%d", n)
	}
	id := deliveryIDForEvent(t, st, ev.ID)

	w := worker.New(st, testsupport.Config()).WithExecutor(&seqExec{failFirst: 2, calls: map[int64]int32{}})
	go w.Run(ctx)
	d := waitStatus(t, st, id, model.StatusSucceeded)
	if d.Attempts < 3 {
		t.Fatalf("attempts=%d want >= 3 (2 failures + success)", d.Attempts)
	}
	attempts, err := st.ListAttempts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var failKinds int
	for _, a := range attempts {
		if a.ErrorKind != nil && *a.ErrorKind == model.KindHTTP5xx {
			failKinds++
		}
	}
	if failKinds != 2 {
		t.Fatalf("http_5xx attempts=%d want 2", failKinds)
	}
}

// Every attempt fails: after max_attempts it lands in the dead letter queue
// with distinguishable failure reasons.
func TestRetry_ExhaustsIntoDeadLetter(t *testing.T) {
	st := testsupport.NewStore(t)
	mkEndpoint(t, st, "http://example.test/hook")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ev, _ := ingest(t, st, "invoice.paid", "k2")
	id := deliveryIDForEvent(t, st, ev.ID)

	alwaysFail := &seqExec{failFirst: 100, calls: map[int64]int32{}}
	cfg := testsupport.Config()
	cfg.DefaultMaxAttempts = 3
	_ = cfg
	// Endpoint already created deliveries with max 4 (ingest helper default).
	// Drive via worker.
	w := worker.New(st, testsupport.Config()).WithExecutor(alwaysFail)
	go w.Run(ctx)

	d := waitStatus(t, st, id, model.StatusDead)
	if d.Attempts != 4 {
		t.Fatalf("attempts=%d want 4", d.Attempts)
	}
	if d.ErrorKind == nil || *d.ErrorKind != model.KindHTTP5xx {
		t.Fatalf("dead error_kind=%v want http_5xx", d.ErrorKind)
	}
	// NotBefore reset far into future is irrelevant after dead; error detail
	// on each attempt must distinguish the failure.
	attempts, _ := st.ListAttempts(ctx, id)
	if len(attempts) != 4 {
		t.Fatalf("attempt rows=%d want 4", len(attempts))
	}
	for _, a := range attempts {
		if a.Status != "http_error" {
			t.Fatalf("attempt %d status=%q want http_error", a.AttemptNo, a.Status)
		}
	}

	// Redrive: dead -> pending with attempts reset, then it succeeds.
	exec2 := &seqExec{failFirst: 0, calls: map[int64]int32{}}
	w2 := worker.New(st, testsupport.Config()).WithExecutor(exec2)
	w2.Wake()
	if _, err := st.Redrive(ctx, id); err != nil {
		t.Fatalf("redrive: %v", err)
	}
	go w2.Run(ctx)
	d = waitStatus(t, st, id, model.StatusSucceeded)
	if d.Attempts != 1 {
		t.Fatalf("after redrive attempts=%d want 1", d.Attempts)
	}
}

// A 4xx (other than 429) is permanent: no retries, straight to dead.
func TestRetry_Permanent4xxGoesStraightToDead(t *testing.T) {
	st := testsupport.NewStore(t)
	mkEndpoint(t, st, "http://example.test/hook")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ev, _ := ingest(t, st, "invoice.paid", "k3")
	id := deliveryIDForEvent(t, st, ev.ID)

	exec := &staticExec{res: db.Result{Status: "rejected", HTTPStatus: 404,
		ErrorKind: model.KindHTTP4xx, ErrorDetail: "HTTP 404 (permanent, no retry)"}}
	w := worker.New(st, testsupport.Config()).WithExecutor(exec)
	go w.Run(ctx)

	d := waitStatus(t, st, id, model.StatusDead)
	if d.Attempts != 1 {
		t.Fatalf("attempts=%d want 1 for 4xx", d.Attempts)
	}
	if d.ErrorKind == nil || *d.ErrorKind != model.KindHTTP4xx {
		t.Fatalf("error_kind=%v want http_4xx", d.ErrorKind)
	}
}

type staticExec struct{ res db.Result }

func (e *staticExec) Send(ctx context.Context, c *db.Claimed) db.Result {
	r := e.res
	r.AttemptNo = c.Delivery.Attempts + 1
	time.Sleep(5 * time.Millisecond)
	return r
}
