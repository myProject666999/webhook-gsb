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

type fakeExec struct {
	mu     sync.Mutex
	calls  []int64 // delivery ids in invocation order
	sticky map[int64]db.Result
	block  map[int64]chan struct{}
	dflt   db.Result
}

func newFakeExec() *fakeExec {
	return &fakeExec{sticky: map[int64]db.Result{}, block: map[int64]chan struct{}{}}
}

func (f *fakeExec) Send(ctx context.Context, c *db.Claimed) db.Result {
	id := c.Delivery.ID
	f.mu.Lock()
	f.calls = append(f.calls, id)
	ch := f.block[id]
	res, ok := f.sticky[id]
	f.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
		}
	}
	if !ok {
		res = f.dflt
	}
	res.AttemptNo = c.Delivery.Attempts + 1
	return res
}

func (f *fakeExec) callOrder() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.calls))
	copy(out, f.calls)
	return out
}

func mkEndpoint(t *testing.T, st *db.Store, url string) *model.Endpoint {
	t.Helper()
	ep, err := st.CreateEndpoint(context.Background(), &model.Endpoint{
		URL:    url,
		Secret: "test-secret-12345678",
		Events: []string{"invoice.paid", "test.event"},
		Active: true,
	})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	return ep
}

func ingest(t *testing.T, st *db.Store, typ, orderKey string) (*model.Event, int) {
	t.Helper()
	key := orderKey
	var pk *string
	if key != "" {
		pk = &key
	}
	ev := &model.Event{EventType: typ, Payload: map[string]any{"k": "v"}, OrderKey: pk}
	id, n, err := st.CreateEventAndFanout(context.Background(), ev, 4)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	ev.ID = id
	return ev, n
}

func strp(s string) *string { return &s }

func waitStatus(t *testing.T, st *db.Store, id int64, want string) *model.Delivery {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		d, err := st.GetDelivery(context.Background(), id)
		if err != nil {
			t.Fatalf("get delivery: %v", err)
		}
		if d.Status == want {
			return d
		}
		time.Sleep(30 * time.Millisecond)
	}
	d, _ := st.GetDelivery(context.Background(), id)
	t.Fatalf("delivery %d status=%s, want %s (error=%v)", id, d.Status, want, d.ErrorDetail)
	return nil
}

func newWorker(st *db.Store, exec worker.Executor) *worker.Worker {
	return worker.New(st, testsupport.Config()).WithExecutor(exec)
}
