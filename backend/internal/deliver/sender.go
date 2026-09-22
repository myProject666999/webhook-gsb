// Package deliver performs the actual HTTP POST for one delivery attempt.
package deliver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"webhook/internal/db"
	"webhook/internal/model"
	"webhook/internal/signing"
)

type Sender struct {
	Client         *http.Client
	AttemptTimeout time.Duration
	Now            func() time.Time
	NonceLen       int
}

func NewSender(timeout time.Duration) *Sender {
	return &Sender{
		Client:         &http.Client{Timeout: timeout},
		AttemptTimeout: timeout,
		Now:            time.Now,
		NonceLen:       16,
	}
}

func newNonce(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Send executes one attempt and classifies the outcome.
func (s *Sender) Send(ctx context.Context, c *db.Claimed) db.Result {
	now := s.Now()
	attemptNo := c.Delivery.Attempts + 1
	body, err := json.Marshal(struct {
		ID        string         `json:"id"`
		Type      string         `json:"type"`
		Data      map[string]any `json:"data"`
		OrderKey  *string        `json:"order_key,omitempty"`
		CreatedAt time.Time      `json:"created_at"`
	}{
		ID:        strconv.FormatInt(c.Event.ID, 10),
		Type:      c.Event.EventType,
		Data:      c.Event.Payload,
		OrderKey:  c.OrderKey,
		CreatedAt: c.Event.CreatedAt,
	})
	if err != nil {
		return db.Result{AttemptNo: attemptNo, Status: "rejected", ErrorKind: model.KindBadResponse, ErrorDetail: "encode payload: " + err.Error()}
	}

	reqCtx, cancel := context.WithTimeout(ctx, s.AttemptTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.Endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return db.Result{AttemptNo: attemptNo, Status: "rejected", ErrorKind: model.KindBadResponse, ErrorDetail: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "webhook-gsb/1.0")
	req.Header.Set(signing.HeaderEventID, strconv.FormatInt(c.Event.ID, 10))
	req.Header.Set(signing.HeaderDelivery, strconv.FormatInt(c.Delivery.ID, 10))
	req.Header.Set(signing.HeaderAttempt, strconv.Itoa(attemptNo))
	signing.SignRequest(req, c.Endpoint.Secret, now, body, newNonce(s.NonceLen))

	start := s.Now()
	resp, err := s.Client.Do(req)
	duration := s.Now().Sub(start)
	r := db.Result{AttemptNo: attemptNo, DurationMS: int(duration / time.Millisecond)}
	if err != nil {
		if errors.Is(reqCtx.Err(), context.DeadlineExceeded) || isTimeoutErr(err) {
			r.Status = "timeout"
			r.ErrorKind = model.KindTimeout
			r.ErrorDetail = fmt.Sprintf("attempt timed out after %s: %v", s.AttemptTimeout, err)
		} else {
			r.Status = "network_error"
			r.ErrorKind = model.KindNetwork
			r.ErrorDetail = err.Error()
		}
		return r
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	r.HTTPStatus = resp.StatusCode
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		r.Status = "succeeded"
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		r.Status = "http_error"
		r.ErrorKind = model.KindHTTP5xx
		r.ErrorDetail = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, firstLine(respBody))
		r.RetryAfterHint = parseRetryAfter(resp.Header.Get("Retry-After"), s.Now)
	case resp.StatusCode >= 400:
		// 4xx other than 429 is treated as permanent: straight to dead letter.
		r.Status = "rejected"
		r.ErrorKind = model.KindHTTP4xx
		r.ErrorDetail = fmt.Sprintf("HTTP %d (permanent, no retry): %s", resp.StatusCode, firstLine(respBody))
	default:
		r.Status = "http_error"
		r.ErrorKind = model.KindBadResponse
		r.ErrorDetail = fmt.Sprintf("unexpected HTTP %d: %s", resp.StatusCode, firstLine(respBody))
	}
	return r
}

func isTimeoutErr(err error) bool {
	var te interface{ Timeout() bool }
	if errors.As(err, &te) {
		return te.Timeout()
	}
	return false
}

func firstLine(b []byte) string {
	s := string(bytes.TrimSpace(b))
	if len(s) > 300 {
		s = s[:300]
	}
	if s == "" {
		return "<empty response body>"
	}
	return s
}

// parseRetryAfter supports both delta-seconds and HTTP-date.
func parseRetryAfter(v string, now func() time.Time) time.Duration {
	if v == "" || now == nil {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 || secs > 3600 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(now())
		if d > 0 && d <= time.Hour {
			return d
		}
	}
	return 0
}

// Backoff computes exponential backoff with full jitter:
//
//	random(0, min(max, base * 2^(attempt-1)))
//
// attempt is 1-based number of the attempt that just failed.
func Backoff(base, max time.Duration, attempt int, rng float64) time.Duration {
	if base <= 0 {
		return 0
	}
	mult := math.Pow(2, float64(attempt-1))
	d := float64(base) * mult
	if d > float64(max) || math.IsInf(d, 0) {
		d = float64(max)
	}
	// Equal jitter: pick a value in [d/2, d) so retries spread out while
	// the floor still grows exponentially.
	out := time.Duration(d * (0.5 + 0.5*rng))
	if out > max {
		out = max
	}
	return out
}
