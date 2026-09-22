// Package signing produces and verifies HMAC webhook signatures.
//
// Signed string: timestamp + "." + body  (the nonce is included as a
// separate, unsigned header; see the replay-protection note below).
//
// Header X-Webhook-Signature carries a Stripe-style, comma separated list:
//
//	t=<unix seconds>,v1=<hex hmac-sha256>
//
// Replay protection:
//   - timestamps older than MaxTimestampAge are rejected;
//   - when a nonce store is provided the X-Webhook-Nonce header must be
//     unique within the window (one-time random string).
package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderSignature = "X-Webhook-Signature"
	HeaderTimestamp = "X-Webhook-Timestamp"
	HeaderNonce     = "X-Webhook-Nonce"
	HeaderEventID   = "X-Webhook-Event-Id"
	HeaderDelivery  = "X-Webhook-Delivery-Id"
	HeaderAttempt   = "X-Webhook-Attempt"
)

// NonceStore remembers recently seen nonces.
type NonceStore interface {
	// SeenOrRemember returns true if the nonce was already present.
	SeenOrRemember(nonce string, ttl time.Duration) (bool, error)
}

// Sign returns the value for X-Webhook-Signature.
func Sign(secret string, timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", timestamp.Unix(), body)
	return "t=" + strconv.FormatInt(timestamp.Unix(), 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// SignRequest stamps timestamp, nonce and signature headers on req.
func SignRequest(req *http.Request, secret string, now time.Time, body []byte, nonce string) {
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	if nonce != "" {
		req.Header.Set(HeaderNonce, nonce)
	}
	req.Header.Set(HeaderSignature, Sign(secret, now, body))
}

// Verify validates the signature of an inbound request.
func Verify(secret string, timestamp time.Time, body []byte, sigHeader string, maxAge time.Duration) error {
	ts, macHex, err := parseSignature(sigHeader)
	if err != nil {
		return err
	}
	if maxAge > 0 {
		age := timestamp.Sub(ts)
		if age < 0 {
			age = -age
		}
		if age > maxAge {
			return fmt.Errorf("signature timestamp outside allowed window (age=%s, max=%s)", age.Round(time.Second), maxAge)
		}
	}
	want := Sign(secret, ts, body)
	if !hmac.Equal([]byte(macHex), []byte(strings.TrimPrefix(want, "t="+strconv.FormatInt(ts.Unix(), 10)+",v1="))) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// VerifyRequest validates headers/body and, if nonces != nil, the nonce.
func VerifyRequest(secret string, body []byte, h http.Header, now time.Time, maxAge time.Duration, nonces NonceStore) error {
	if _, err := strconv.ParseInt(h.Get(HeaderTimestamp), 10, 64); err != nil {
		return fmt.Errorf("missing or invalid %s header", HeaderTimestamp)
	}
	if err := Verify(secret, now, body, h.Get(HeaderSignature), maxAge); err != nil {
		return err
	}
	if nonces != nil {
		nonce := h.Get(HeaderNonce)
		if nonce == "" {
			return fmt.Errorf("missing %s header", HeaderNonce)
		}
		seen, err := nonces.SeenOrRemember(nonce, maxAge)
		if err != nil {
			return fmt.Errorf("nonce store error: %w", err)
		}
		if seen {
			return fmt.Errorf("replayed nonce %q", nonce)
		}
	}
	return nil
}

// parseSignature returns the timestamp and hex v1 MAC from the header.
func parseSignature(h string) (time.Time, string, error) {
	var tsSec int64
	var sig string
	var haveTS, haveSig bool
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "t="):
			v, err := strconv.ParseInt(strings.TrimPrefix(part, "t="), 10, 64)
			if err != nil {
				return time.Time{}, "", fmt.Errorf("invalid timestamp in signature")
			}
			tsSec, haveTS = v, true
		case strings.HasPrefix(part, "v1="):
			sig, haveSig = strings.TrimPrefix(part, "v1="), true
		}
	}
	if !haveTS || !haveSig {
		return time.Time{}, "", fmt.Errorf("signature must contain t= and v1= parts")
	}
	return time.Unix(tsSec, 0), sig, nil
}
