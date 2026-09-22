package signing_test

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"webhook/internal/signing"
)

type fakeNonces struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (f *fakeNonces) SeenOrRemember(nonce string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen[nonce] {
		return true, nil
	}
	f.seen = map[string]bool{}
	f.seen[nonce] = true
	return false, nil
}

func TestSignAndVerify_RoundTrip(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	now := time.Unix(1_700_000_000, 0)

	req := httptest.NewRequest("POST", "/hook", strings.NewReader(string(body)))
	signing.SignRequest(req, "topsecret", now, body, "nonce-abc")

	if err := signing.VerifyRequest("topsecret", body, req.Header, now.Add(30*time.Second), time.Minute, nil); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerify_WrongSecretRejected(t *testing.T) {
	body := []byte("payload")
	now := time.Now()
	req := httptest.NewRequest("POST", "/hook", strings.NewReader(string(body)))
	signing.SignRequest(req, "correct-secret", now, body, "n1")
	if err := signing.VerifyRequest("wrong-secret", body, req.Header, now, time.Minute, nil); err == nil {
		t.Fatal("expected signature mismatch")
	}
}

func TestVerify_TamperedBodyRejected(t *testing.T) {
	body := []byte(`{"a":1}`)
	now := time.Now()
	req := httptest.NewRequest("POST", "/hook", strings.NewReader(string(body)))
	signing.SignRequest(req, "s", now, body, "n")
	tampered := []byte(`{"a":2}`)
	if err := signing.VerifyRequest("s", tampered, req.Header, now, time.Minute, nil); err == nil {
		t.Fatal("expected mismatch for tampered body")
	}
}

func TestVerify_OldTimestampRejected(t *testing.T) {
	body := []byte("x")
	sent := time.Now().Add(-10 * time.Minute)
	req := httptest.NewRequest("POST", "/hook", strings.NewReader(string(body)))
	signing.SignRequest(req, "s", sent, body, "n")
	if err := signing.VerifyRequest("s", body, req.Header, time.Now(), 5*time.Minute, nil); err == nil {
		t.Fatal("expected stale timestamp rejection")
	}
}

func TestVerify_ReplayedNonceRejected(t *testing.T) {
	ns := &fakeNonces{}
	body := []byte("x")
	now := time.Now()
	req := httptest.NewRequest("POST", "/hook", strings.NewReader(string(body)))
	signing.SignRequest(req, "s", now, body, "nonce-unique")
	if err := signing.VerifyRequest("s", body, req.Header, now, time.Minute, ns); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	// Attacker replays the exact same request bytes and headers.
	req2 := httptest.NewRequest("POST", "/hook", strings.NewReader(string(body)))
	req2.Header = req.Header.Clone()
	if err := signing.VerifyRequest("s", body, req2.Header, now.Add(time.Second), time.Minute, ns); err == nil {
		t.Fatal("expected replayed nonce rejection")
	}
}

func TestVerify_MissingNonceRejectedWhenEnforced(t *testing.T) {
	body := []byte("x")
	now := time.Now()
	req := httptest.NewRequest("POST", "/hook", strings.NewReader(string(body)))
	signing.SignRequest(req, "s", now, body, "")
	req.Header.Del(signing.HeaderNonce)
	ns := &fakeNonces{}
	if err := signing.VerifyRequest("s", body, req.Header, now, time.Minute, ns); err == nil {
		t.Fatal("expected missing nonce rejection")
	}
}
