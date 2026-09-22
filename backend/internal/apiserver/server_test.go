package apiserver_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"webhook/internal/apiserver"
	"webhook/internal/testsupport"
)

func TestAPI_FanoutValidationAndRedrive(t *testing.T) {
	st := testsupport.NewStore(t)
	h := apiserver.New(st, testsupport.Config(), nil, nil).Routes()

	post := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	get := func(path string) (int, any) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// validation: short secret, bad url, empty events
	code, body := post("/api/endpoints", map[string]any{"url": "ftp://x", "secret": "short", "events": []string{}})
	if code != http.StatusBadRequest {
		t.Fatalf("create endpoint code=%d body=%v", code, body)
	}

	// two endpoints subscribe to different sets
	post("/api/endpoints", map[string]any{"url": "http://a/hook", "secret": "secret-123456", "events": []string{"alpha"}})
	post("/api/endpoints", map[string]any{"url": "http://b/hook", "secret": "secret-123456", "events": []string{"alpha", "beta"}})

	code, body = post("/api/events", map[string]any{"type": "alpha", "payload": map[string]any{"x": 1}, "order_key": "k"})
	if code != http.StatusCreated || body["deliveries_created"].(float64) != 2 {
		t.Fatalf("alpha fanout: %d %v", code, body)
	}
	code, body = post("/api/events", map[string]any{"type": "beta", "payload": map[string]any{}})
	if code != http.StatusCreated || body["deliveries_created"].(float64) != 1 {
		t.Fatalf("beta fanout: %d %v", code, body)
	}

	code, stats := get("/api/stats")
	if code != 200 || stats.(map[string]any)["total"].(float64) != 3 {
		t.Fatalf("stats: %d %v", code, stats)
	}

	// redrive on a pending delivery must 404 (only dead is redrivable)
	code, _ = get("/api/deliveries")
	if code != 200 {
		t.Fatalf("list deliveries %d", code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/deliveries/1/redrive", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("redrive pending code=%d want 404", rec.Code)
	}
}
