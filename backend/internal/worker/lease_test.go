package worker_test

import (
	"context"
	"testing"
	"time"

	"webhook/internal/db"
	"webhook/internal/model"
	"webhook/internal/testsupport"
	"webhook/internal/worker"
)

// Lease takeover: worker A stalls beyond the lease duration; worker B's
// reaper recovers the delivery and completes it. A's late success must NOT
// overwrite the final outcome and must not create a double success.
func TestLeaseTakeoverAfterTimeout(t *testing.T) {
	st := testsupport.NewStore(t)
	mkEndpoint(t, st, "http://example.test/hook")

	ev, _ := ingest(t, st, "invoice.paid", "lease-key")
	id := deliveryIDForEvent(t, st, ev.ID)

	// Worker A: holds the claim (fake executor blocks), does not commit.
	aDone := make(chan struct{})
	execA := &blockingExec{release: aDone, res: db.Result{Status: "succeeded", HTTPStatus: 200}}
	cfgA := testsupport.Config()
	cfgA.WorkerID = "worker-A"
	cfgA.LeaseDuration = 1500 * time.Millisecond
	wA := worker.New(st, cfgA).WithExecutor(execA)

	ctxA, cancelA := context.WithCancel(context.Background())
	go wA.Run(ctxA)

	// Wait until A holds the lease and its sender is blocked in the attempt.
	waitUntil(t, func() bool {
		d, _ := st.GetDelivery(context.Background(), id)
		return d.Status == model.StatusInFlight && d.LeasedBy != nil && *d.LeasedBy == "worker-A"
	})

	// Stop A's dispatch loop. Its in-flight sender goroutine stays blocked
	// in a detached commit until aDone closes: this models a frozen worker
	// that is still nominally "delivering".
	cancelA()

	// Worker B with a short reaper cadence takes over.
	cfgB := testsupport.Config()
	cfgB.WorkerID = "worker-B"
	cfgB.LeaseDuration = 1500 * time.Millisecond
	cfgB.PollInterval = 50 * time.Millisecond
	execB := &staticExec{res: db.Result{Status: "succeeded", HTTPStatus: 200}}
	wB := worker.New(st, cfgB).WithExecutor(execB)
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	go wB.Run(ctxB)

	d := waitStatus(t, st, id, model.StatusSucceeded)
	if d.LeasedBy == nil || *d.LeasedBy != "worker-B" {
		t.Fatalf("final owner=%v want worker-B; only B commits after takeover", d.LeasedBy)
	}

	// Now the frozen A wakes up and tries to commit its late success.
	// Store guards reject it; delivery stays exactly as B left it.
	close(aDone)
	time.Sleep(300 * time.Millisecond)

	d, _ = st.GetDelivery(context.Background(), id)
	if d.Status != model.StatusSucceeded {
		t.Fatalf("status after late commit=%s want succeeded", d.Status)
	}
	if d.Attempts != 2 {
		t.Fatalf("attempts=%d want 2 (one timed out, one succeeded)", d.Attempts)
	}
	attempts, _ := st.ListAttempts(context.Background(), id)
	var bSucceeded, aLost int
	for _, a := range attempts {
		if a.Status == "succeeded" && a.WorkerID != nil && *a.WorkerID == "worker-B" {
			bSucceeded++
		}
		if a.Status == "lost_lease" && a.WorkerID != nil && *a.WorkerID == "worker-A" {
			aLost++
		}
	}
	if bSucceeded != 1 {
		t.Fatalf("worker-B succeeded attempts=%d want 1", bSucceeded)
	}
	if aLost != 1 {
		t.Fatalf("A late attempts rejected as lost_lease=%d want 1", aLost)
	}
}

// A's late commit after takeover is rejected by lease ownership guards:
// exactly the lost_lease path (checked directly against the store).
func TestLeaseTakeover_LateCommitRejected(t *testing.T) {
	st := testsupport.NewStore(t)
	mkEndpoint(t, st, "http://example.test/hook")
	ctx := context.Background()
	ev, _ := ingest(t, st, "invoice.paid", "lease-key2")
	id := deliveryIDForEvent(t, st, ev.ID)

	_, err := st.ClaimNext(ctx, "worker-A", 200*time.Millisecond, 1)
	if err != nil {
		t.Fatal(err)
	}
	// B waits for A's lease to expire, reaps it and claims the delivery.
	time.Sleep(300 * time.Millisecond)
	if _, err := st.ReapExpiredLeases(ctx); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNext(ctx, "worker-B", 5*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].Delivery.ID != id {
		t.Fatalf("B did not take over delivery %d: %+v", id, claimed)
	}
	if claimed[0].Delivery.Attempts != 1 {
		t.Fatalf("post-reap attempts=%d want 1", claimed[0].Delivery.Attempts)
	}
	if err := st.Succeed(ctx, id, 2, "worker-B",
		db.Result{AttemptNo: 2, Status: "succeeded", HTTPStatus: 200, DurationMS: 1}); err != nil {
		t.Fatalf("B succeed: %v", err)
	}
	// A wakes up late and tries to commit attempt 1 as success: rejected.
	if err := st.Succeed(ctx, id, 1, "worker-A",
		db.Result{AttemptNo: 1, Status: "succeeded", HTTPStatus: 200, DurationMS: 1}); err != db.ErrLeaseLost {
		t.Fatalf("A late succeed err=%v want ErrLeaseLost", err)
	}
	d, _ := st.GetDelivery(ctx, id)
	if d.Attempts != 2 || d.Status != model.StatusSucceeded {
		t.Fatalf("final state attempts=%d status=%s want attempts=2 succeeded", d.Attempts, d.Status)
	}
	rows, _ := st.ListAttempts(ctx, id)
	var lost, succ int
	for _, a := range rows {
		switch a.Status {
		case "lost_lease":
			lost++
		case "succeeded":
			succ++
		}
	}
	if lost != 1 || succ != 1 {
		t.Fatalf("attempt rows lost_lease=%d succeeded=%d, want 1/1", lost, succ)
	}
}

type blockingExec struct {
	release chan struct{}
	res     db.Result
}

func (e *blockingExec) Send(ctx context.Context, c *db.Claimed) db.Result {
	<-e.release
	r := e.res
	r.AttemptNo = c.Delivery.Attempts + 1
	return r
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
