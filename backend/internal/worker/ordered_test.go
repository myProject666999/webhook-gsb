package worker_test

import (
	"context"
	"testing"

	"webhook/internal/db"
	"webhook/internal/model"
	"webhook/internal/testsupport"
)

// Same order key: three deliveries must succeed one after another, in the
// order the events arrived — even with 4 concurrent worker slots.
func TestOrderedDelivery_SameKeyFIFO(t *testing.T) {
	st := testsupport.NewStore(t)
	mkEndpoint(t, st, "http://example.test/hook")
	exec := newFakeExec()
	exec.dflt = db.Result{Status: "succeeded"}
	w := newWorker(st, exec)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ids []int64
	for i := 0; i < 3; i++ {
		_, n := ingest(t, st, "invoice.paid", "customer-42")
		if n != 1 {
			t.Fatalf("fanout=%d want 1", n)
		}
	}
	dls, _ := st.ListDeliveries(ctx, db.DeliveryFilter{Limit: 10})
	// ListDeliveries is id DESC; reverse to creation order.
	for i := len(dls) - 1; i >= 0; i-- {
		ids = append(ids, dls[i].ID)
	}

	go w.Run(ctx)

	for _, id := range ids {
		waitStatus(t, st, id, model.StatusSucceeded)
	}
	got := exec.callOrder()
	if len(got) != 3 || got[0] != ids[0] || got[1] != ids[1] || got[2] != ids[2] {
		t.Fatalf("attempt order=%v want %v", got, ids)
	}
}

// A stuck head of line blocks later deliveries for the SAME key but must not
// block a different key or a keyless delivery.
func TestOrderedDelivery_HeadOfLineBlocking(t *testing.T) {
	st := testsupport.NewStore(t)
	mkEndpoint(t, st, "http://example.test/hook")
	exec := newFakeExec()
	exec.dflt = db.Result{Status: "succeeded"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newWorker(st, exec)
	go w.Run(ctx)

	// First event (blocked head).
	ev1, _ := ingest(t, st, "invoice.paid", "key-A")
	release := make(chan struct{})
	exec.mu.Lock()
	exec.block[deliveryIDForEvent(t, st, ev1.ID)] = release
	exec.mu.Unlock()

	// Second same-key event: must wait behind the head.
	ev2, _ := ingest(t, st, "invoice.paid", "key-A")
	// Different key: must not be blocked.
	ev3, _ := ingest(t, st, "invoice.paid", "key-B")
	// Keyless event: must not be blocked.
	ev4, _ := ingest(t, st, "invoice.paid", "")

	id2 := deliveryIDForEvent(t, st, ev2.ID)
	id3 := deliveryIDForEvent(t, st, ev3.ID)
	id4 := deliveryIDForEvent(t, st, ev4.ID)

	waitStatus(t, st, id3, model.StatusSucceeded)
	waitStatus(t, st, id4, model.StatusSucceeded)

	if d, _ := st.GetDelivery(ctx, id2); d.Status == model.StatusSucceeded {
		t.Fatalf("same-key delivery succeeded while head blocked")
	}
	// UI visibility: queue position for the blocked delivery.
	dls, _ := st.ListDeliveries(ctx, db.DeliveryFilter{Status: model.StatusPending, Limit: 50})
	found := false
	for _, d := range dls {
		if d.ID == id2 {
			found = true
			if d.QueuePosition < 2 {
				t.Fatalf("queue_position=%d want >= 2", d.QueuePosition)
			}
			if d.BlockedReason != "head_of_line" {
				t.Fatalf("blocked_reason=%q want head_of_line", d.BlockedReason)
			}
		}
	}
	if !found {
		t.Fatalf("blocked delivery %d not listed as pending", id2)
	}

	close(release)
	waitStatus(t, st, deliveryIDForEvent(t, st, ev1.ID), model.StatusSucceeded)
	waitStatus(t, st, id2, model.StatusSucceeded)
}

func deliveryIDForEvent(t *testing.T, st *db.Store, eventID int64) int64 {
	t.Helper()
	dls, err := st.ListDeliveries(context.Background(), db.DeliveryFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dls {
		if d.EventID == eventID {
			return d.ID
		}
	}
	t.Fatalf("no delivery for event %d", eventID)
	return 0
}
