package model

import "time"

type Endpoint struct {
	ID          int64     `json:"id"`
	URL         string    `json:"url"`
	Secret      string    `json:"secret"`
	Events      []string  `json:"events"`
	Active      bool      `json:"active"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

type Event struct {
	ID        int64          `json:"id"`
	EventType string         `json:"event_type"`
	Payload   map[string]any `json:"payload"`
	OrderKey  *string        `json:"order_key,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

const (
	StatusPending   = "pending"
	StatusInFlight  = "in_flight"
	StatusSucceeded = "succeeded"
	StatusDead      = "dead"
)

type Delivery struct {
	ID          int64      `json:"id"`
	EndpointID  int64      `json:"endpoint_id"`
	EventID     int64      `json:"event_id"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	NotBefore   time.Time  `json:"not_before"`
	LeasedAt    *time.Time `json:"leased_at,omitempty"`
	LeasedUntil *time.Time `json:"leased_until,omitempty"`
	LeasedBy    *string    `json:"leased_by,omitempty"`
	ErrorKind   *string    `json:"error_kind,omitempty"`
	ErrorDetail *string    `json:"error_detail,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`

	QueuePosition int    `json:"queue_position"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

type Attempt struct {
	ID          int64      `json:"id"`
	DeliveryID  int64      `json:"delivery_id"`
	AttemptNo   int        `json:"attempt_no"`
	Status      string     `json:"status"`
	HTTPStatus  *int       `json:"http_status,omitempty"`
	ErrorKind   *string    `json:"error_kind,omitempty"`
	ErrorDetail *string    `json:"error_detail,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	DurationMS  *int       `json:"duration_ms,omitempty"`
	WorkerID    *string    `json:"worker_id,omitempty"`
}

// Error kinds classify why an attempt / delivery failed.
const (
	KindTimeout     = "timeout"
	KindHTTP5xx     = "http_5xx"
	KindHTTP4xx     = "http_4xx"
	KindNetwork     = "network"
	KindLeaseTaken  = "lease_timeout"
	KindLeaseLost   = "lease_lost"
	KindBadResponse = "bad_response"
)
