CREATE TABLE IF NOT EXISTS endpoints (
    id          BIGSERIAL PRIMARY KEY,
    url         TEXT NOT NULL,
    secret      TEXT NOT NULL,
    events      TEXT[] NOT NULL DEFAULT '{}',
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS events (
    id         BIGSERIAL PRIMARY KEY,
    event_type TEXT NOT NULL,
    payload    JSONB NOT NULL,
    order_key  TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_events_created ON events (created_at DESC);

CREATE TABLE IF NOT EXISTS deliveries (
    id           BIGSERIAL PRIMARY KEY,
    endpoint_id  BIGINT NOT NULL REFERENCES endpoints(id) ON DELETE CASCADE,
    event_id     BIGINT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','in_flight','succeeded','dead')),
    attempts     INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 6,
    not_before   TIMESTAMPTZ NOT NULL DEFAULT now(),
    leased_at    TIMESTAMPTZ,
    leased_until TIMESTAMPTZ,
    leased_by    TEXT,
    error_kind   TEXT,
    error_detail TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_delivery_dispatch
    ON deliveries (status, not_before)
    INCLUDE (endpoint_id, event_id);
CREATE INDEX IF NOT EXISTS idx_delivery_endpoint ON deliveries (endpoint_id);
CREATE INDEX IF NOT EXISTS idx_delivery_event ON deliveries (event_id);

CREATE TABLE IF NOT EXISTS delivery_attempts (
    id          BIGSERIAL PRIMARY KEY,
    delivery_id BIGINT NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    attempt_no  INT NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('timeout','http_error','network_error','rejected','succeeded','lost_lease')),
    http_status INT,
    error_kind  TEXT,
    error_detail TEXT,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    duration_ms INT,
    worker_id   TEXT
);
CREATE INDEX IF NOT EXISTS idx_attempts_delivery ON delivery_attempts (delivery_id, attempt_no);
