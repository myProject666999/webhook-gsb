package deliver_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"webhook/internal/db"
	"webhook/internal/deliver"
	"webhook/internal/model"
	"webhook/internal/signing"
)

func claimed(url string) *db.Claimed {
	key := "k"
	return &db.Claimed{
		Delivery: &model.Delivery{ID: 7, EndpointID: 1, EventID: 9, Attempts: 0, MaxAttempts: 4},
		Endpoint: &model.Endpoint{ID: 1, URL: url, Secret: "s3cret-key-123456"},
		Event:    &model.Event{ID: 9, EventType: "invoice.paid", Payload: map[string]any{"amount": 42}},
		OrderKey: &key,
	}
}

// End-to-end over real HTTP: a slow endpoint producing 503 twice then 200 is
// classified http_error/http_5xx twice and finally succeeds; requests carry
// a valid HMAC signature, timestamp and unique nonce.
func TestSender_Flaky5xxThenSuccess(t *testing.T) {
	var mu sync.Mutex
	var nonces []string
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := signing.VerifyRequest("s3cret-key-123456", body, r.Header, time.Now(), time.Minute, nil); err != nil {
			t.Errorf("signature verify: %v", err)
		}
		nonce := r.Header.Get(signing.HeaderNonce)
		mu.Lock()
		for _, prev := range nonces {
			if prev == nonce {
				t.Errorf("nonce reused: %s", nonce)
			}
		}
		nonces = append(nonces, nonce)
		n++
		if r.Header.Get(signing.HeaderEventID) != "9" ||
			r.Header.Get(signing.HeaderDelivery) != "7" ||
			r.Header.Get(signing.HeaderAttempt) != "1" {
			t.Errorf("identity headers wrong: %v", r.Header)
		}
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		if got["type"] != "invoice.paid" {
			t.Errorf("body type=%v", got["type"])
		}
		mu.Unlock()
		if n <= 2 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := deliver.NewSender(3 * time.Second)
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		r := s.Send(ctx, claimed(srv.URL))
		if r.Status != "http_error" || r.ErrorKind != model.KindHTTP5xx || r.HTTPStatus != 503 {
			t.Fatalf("attempt %d: %+v", i, r)
		}
	}
	r := s.Send(ctx, claimed(srv.URL))
	if r.Status != "succeeded" || r.HTTPStatus != 200 {
		t.Fatalf("third attempt: %+v", r)
	}
}

// A connect failure must be classified as a network error.
func TestSender_ConnectionRefused(t *testing.T) {
	s := deliver.NewSender(time.Second)
	r := s.Send(context.Background(), claimed("http://127.0.0.1:1/hook"))
	if r.Status != "network_error" || r.ErrorKind != model.KindNetwork {
		t.Fatalf("got %+v want network_error/network", r)
	}
}

// A slow endpoint must hit the attempt timeout classification.
func TestSender_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
	}))
	defer srv.Close()
	s := deliver.NewSender(100 * time.Millisecond)
	r := s.Send(context.Background(), claimed(srv.URL))
	if r.Status != "timeout" || r.ErrorKind != model.KindTimeout {
		t.Fatalf("got %+v want timeout/timeout", r)
	}
}

// 4xx is permanent (rejected) except 429.
func TestSender_4xxPermanentAnd429Retryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "404") {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		w.Header().Set("Retry-After", "1")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	s := deliver.NewSender(3 * time.Second)
	c := claimed(srv.URL + "?x=404")
	r := s.Send(context.Background(), c)
	if r.Status != "rejected" || r.ErrorKind != model.KindHTTP4xx {
		t.Fatalf("404 -> %+v", r)
	}
	c2 := claimed(srv.URL)
	r = s.Send(context.Background(), c2)
	if r.Status != "http_error" || r.RetryAfterHint != time.Second {
		t.Fatalf("429 -> %+v", r)
	}
}

func TestBackoff_BoundedAndGrowing(t *testing.T) {
	b := deliver.Backoff(time.Second, 30*time.Second, 1, 1)
	if b != time.Second {
		t.Fatalf("base backoff=%s want 1s", b)
	}
	if got := deliver.Backoff(time.Second, 30*time.Second, 5, 1); got != 16*time.Second {
		t.Fatalf("2^4 base=%s want 16s", got)
	}
	if got := deliver.Backoff(time.Second, 10*time.Second, 10, 1); got != 10*time.Second {
		t.Fatalf("cap=%s want 10s", got)
	}
}
