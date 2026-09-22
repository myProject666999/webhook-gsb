// Package apiserver exposes the management REST API and serves the SPA.
package apiserver

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"webhook/internal/config"
	"webhook/internal/db"
	"webhook/internal/model"
)

const maxEventBody = 1 << 20 // 1 MiB

type Server struct {
	store  *db.Store
	cfg    config.Config
	waker  func()
	static fs.FS
}

func New(st *db.Store, cfg config.Config, waker func(), static fs.FS) *Server {
	return &Server{store: st, cfg: cfg, waker: waker, static: static}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)

	mux.HandleFunc("GET /api/endpoints", s.listEndpoints)
	mux.HandleFunc("POST /api/endpoints", s.createEndpoint)
	mux.HandleFunc("GET /api/endpoints/{id}", s.getEndpoint)
	mux.HandleFunc("PUT /api/endpoints/{id}", s.updateEndpoint)
	mux.HandleFunc("DELETE /api/endpoints/{id}", s.deleteEndpoint)

	mux.HandleFunc("POST /api/events", s.createEvent)
	mux.HandleFunc("GET /api/events", s.listEvents)

	mux.HandleFunc("GET /api/deliveries", s.listDeliveries)
	mux.HandleFunc("GET /api/deliveries/{id}", s.getDelivery)
	mux.HandleFunc("GET /api/deliveries/{id}/attempts", s.listAttempts)
	mux.HandleFunc("POST /api/deliveries/{id}/redrive", s.redrive)
	mux.HandleFunc("GET /api/stats", s.stats)

	mux.Handle("/", s.spaHandler())
	return logging(mux)
}

func (s *Server) spaHandler() http.Handler {
	if s.static == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			http.Error(w, "frontend build not embedded", http.StatusNotFound)
		})
	}
	fileServer := http.FileServer(http.FS(s.static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(s.static, p); err != nil {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Pool().Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "worker_id": s.cfg.WorkerID})
}

func (s *Server) listEndpoints(w http.ResponseWriter, r *http.Request) {
	eps, err := s.store.ListEndpoints(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, eps)
}

type endpointInput struct {
	URL         string   `json:"url"`
	Secret      string   `json:"secret"`
	Events      []string `json:"events"`
	Active      *bool    `json:"active"`
	Description string   `json:"description"`
}

func (in endpointInput) toModel(id int64) (*model.Endpoint, string) {
	if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		return nil, "url must start with http:// or https://"
	}
	if len(in.Secret) < 8 {
		return nil, "secret must be at least 8 characters"
	}
	if len(in.Events) == 0 {
		return nil, "events must contain at least one event type (e.g. \"invoice.paid\")"
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	return &model.Endpoint{
		ID:          id,
		URL:         strings.TrimSpace(in.URL),
		Secret:      in.Secret,
		Events:      in.Events,
		Active:      active,
		Description: in.Description,
	}, ""
}

func (s *Server) createEndpoint(w http.ResponseWriter, r *http.Request) {
	var in endpointInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	e, msg := in.toModel(0)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	e, err := s.store.CreateEndpoint(r.Context(), e)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) getEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	e, err := s.store.GetEndpoint(r.Context(), id)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) updateEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var in endpointInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	e, msg := in.toModel(id)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	e, err := s.store.UpdateEndpoint(r.Context(), e)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) deleteEndpoint(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteEndpoint(r.Context(), id); err != nil {
		writeDBError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type eventInput struct {
	Type     string         `json:"type"`
	Payload  map[string]any `json:"payload"`
	OrderKey *string        `json:"order_key"`
}

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxEventBody)
	var in eventInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Type == "" {
		writeError(w, http.StatusBadRequest, "type is required")
		return
	}
	if in.Payload == nil {
		in.Payload = map[string]any{}
	}
	if in.OrderKey != nil && strings.TrimSpace(*in.OrderKey) == "" {
		in.OrderKey = nil
	}
	ev := &model.Event{EventType: in.Type, Payload: in.Payload, OrderKey: in.OrderKey}
	id, n, err := s.store.CreateEventAndFanout(r.Context(), ev, s.cfg.DefaultMaxAttempts)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if s.waker != nil {
		s.waker()
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"event_id":           id,
		"deliveries_created": n,
	})
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := s.store.ListEvents(r.Context(), 100)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, evs)
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	f := db.DeliveryFilter{Status: r.URL.Query().Get("status"), Limit: 200}
	if v := r.URL.Query().Get("endpoint_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid endpoint_id")
			return
		}
		f.EndpointID = n
	}
	ds, err := s.store.ListDeliveries(r.Context(), f)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ds)
}

func (s *Server) getDelivery(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	d, err := s.store.GetDelivery(r.Context(), id)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) listAttempts(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	as, err := s.store.ListAttempts(r.Context(), id)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, as)
}

func (s *Server) redrive(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	d, err := s.store.Redrive(r.Context(), id)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if s.waker != nil {
		s.waker()
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.Stats(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func idParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeDBError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	log.Printf("db error: %v", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
