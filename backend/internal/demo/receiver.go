// Package demo implements a small callback receiver used for manual
// self-testing. It verifies signatures, rejects replays, and supports
// failure injection through the registered URL query parameters:
//
//	?mode=fail500   always reply 500
//	?mode=fail404   always reply 404 (permanent failure -> dead letter)
//	?mode=timeout   hold the connection longer than the sender timeout
//	?mode=flaky     fail the first two attempts, then succeed
//	?delay=2s       add a fixed delay
//
// Run with: server receiver [-addr :9000] [-secret ...] [-window 5m]
package demo

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"webhook/internal/signing"
)

type memNonces struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newMemNonces() *memNonces { return &memNonces{m: map[string]time.Time{}} }

func (n *memNonces) SeenOrRemember(nonce string, ttl time.Duration) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	for k, exp := range n.m {
		if exp.Before(now) {
			delete(n.m, k)
		}
	}
	if _, ok := n.m[nonce]; ok {
		return true, nil
	}
	n.m[nonce] = now.Add(ttl)
	return false, nil
}

type flakyState struct {
	mu    sync.Mutex
	count map[string]int
}

func RunReceiver(args []string) {
	fs := flag.NewFlagSet("receiver", flag.ExitOnError)
	addr := fs.String("addr", ":9000", "listen address")
	secret := fs.String("secret", "demo-secret-please-change", "HMAC secret expected from the sender")
	window := fs.Duration("window", 5*time.Minute, "signature timestamp / nonce window")
	_ = fs.Parse(args)

	nonces := newMemNonces()
	flaky := &flakyState{count: map[string]int{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := signing.VerifyRequest(*secret, body, r.Header, time.Now(), *window, nonces); err != nil {
			log.Printf("receiver REJECT delivery=%s attempt=%s: %v",
				r.Header.Get(signing.HeaderDelivery), r.Header.Get(signing.HeaderAttempt), err)
			http.Error(w, "signature verification failed: "+err.Error(), http.StatusUnauthorized)
			return
		}

		mode := r.URL.Query().Get("mode")
		if d := r.URL.Query().Get("delay"); d != "" {
			if dur, err := time.ParseDuration(d); err == nil {
				select {
				case <-time.After(dur):
				case <-r.Context().Done():
				}
			}
		}
		if mode == "timeout" {
			time.Sleep(30 * time.Second)
		}
		if mode == "fail500" {
			http.Error(w, "injected server error", http.StatusInternalServerError)
			return
		}
		if mode == "fail404" {
			http.Error(w, "injected permanent error", http.StatusNotFound)
			return
		}
		if mode == "flaky" {
			key := r.Header.Get(signing.HeaderDelivery)
			flaky.mu.Lock()
			n := flaky.count[key]
			flaky.count[key] = n + 1
			flaky.mu.Unlock()
			if n < 2 {
				http.Error(w, "injected flaky failure", http.StatusBadGateway)
				return
			}
		}

		var pretty bytes.Buffer
		_ = json.Indent(&pretty, body, "", "  ")
		log.Printf("receiver OK delivery=%s attempt=%s event=%s body=%s",
			r.Header.Get(signing.HeaderDelivery), r.Header.Get(signing.HeaderAttempt),
			r.Header.Get(signing.HeaderEventID), pretty.String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"received":true}`)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("demo receiver listening on %s (secret=%q, window=%s)", *addr, *secret, *window)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
