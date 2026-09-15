-- Operator accounts for the control plane.
CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('admin', 'viewer')),
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER
);

-- TLS key material plus whatever is needed to renew it.
CREATE TABLE certificates (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT    NOT NULL,
    source          TEXT    NOT NULL CHECK (source IN ('acme', 'manual', 'self_signed')),
    certificate_pem TEXT    NOT NULL DEFAULT '',
    private_key_pem TEXT    NOT NULL DEFAULT '',
    issuer          TEXT    NOT NULL DEFAULT '',
    not_before      INTEGER NOT NULL DEFAULT 0,
    not_after       INTEGER NOT NULL DEFAULT 0,
    challenge       TEXT    NOT NULL DEFAULT '',
    dns_provider    TEXT    NOT NULL DEFAULT '',
    dns_credentials TEXT    NOT NULL DEFAULT '',
    last_error      TEXT    NOT NULL DEFAULT '',
    last_issued_at  INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- Domains a certificate covers. Kept in its own table so renewal can look up
-- by name without a LIKE scan over a delimited column.
CREATE TABLE certificate_domains (
    certificate_id INTEGER NOT NULL REFERENCES certificates (id) ON DELETE CASCADE,
    domain         TEXT    NOT NULL,
    position       INTEGER NOT NULL,
    PRIMARY KEY (certificate_id, domain)
);
CREATE INDEX idx_certificate_domains_domain ON certificate_domains (domain);

-- Virtual hosts. Renewal-only certificate rows are referenced with ON DELETE
-- RESTRICT so a certificate in use cannot vanish from under a live listener.
CREATE TABLE hosts (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT    NOT NULL,
    enabled              INTEGER NOT NULL DEFAULT 1,
    algorithm            TEXT    NOT NULL CHECK (
                             algorithm IN ('round_robin', 'weighted_round_robin',
                                           'least_connections', 'ip_hash')),
    certificate_id       INTEGER REFERENCES certificates (id) ON DELETE RESTRICT,
    force_https          INTEGER NOT NULL DEFAULT 0,
    hsts_max_age         INTEGER NOT NULL DEFAULT 0,
    websocket_support    INTEGER NOT NULL DEFAULT 1,
    preserve_host        INTEGER NOT NULL DEFAULT 0,
    hc_enabled           INTEGER NOT NULL DEFAULT 1,
    hc_path              TEXT    NOT NULL DEFAULT '/',
    hc_interval_ms       INTEGER NOT NULL DEFAULT 10000,
    hc_timeout_ms        INTEGER NOT NULL DEFAULT 5000,
    hc_healthy_threshold INTEGER NOT NULL DEFAULT 2,
    hc_unhealthy_threshold INTEGER NOT NULL DEFAULT 3,
    hc_expect_status     INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);
CREATE INDEX idx_hosts_certificate ON hosts (certificate_id);

-- One row per domain a host answers for. The UNIQUE constraint is what makes
-- overlapping routes impossible to save.
CREATE TABLE host_domains (
    host_id  INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    domain   TEXT    NOT NULL UNIQUE,
    position INTEGER NOT NULL
);
CREATE INDEX idx_host_domains_host ON host_domains (host_id);

-- Backends behind a host.
CREATE TABLE upstreams (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    host_id         INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    scheme          TEXT    NOT NULL CHECK (scheme IN ('http', 'https')),
    address         TEXT    NOT NULL,
    weight          INTEGER NOT NULL DEFAULT 1 CHECK (weight >= 1),
    max_conns       INTEGER NOT NULL DEFAULT 0 CHECK (max_conns >= 0),
    enabled         INTEGER NOT NULL DEFAULT 1,
    skip_tls_verify INTEGER NOT NULL DEFAULT 0,
    position        INTEGER NOT NULL,
    UNIQUE (host_id, scheme, address)
);
CREATE INDEX idx_upstreams_host ON upstreams (host_id);

-- Traffic rollups, one row per host per flush interval. host_id 0 is reserved
-- for traffic that matched no host, so those requests stay visible.
CREATE TABLE metrics_samples (
    host_id      INTEGER NOT NULL,
    ts           INTEGER NOT NULL,
    requests     INTEGER NOT NULL DEFAULT 0,
    s2xx         INTEGER NOT NULL DEFAULT 0,
    s3xx         INTEGER NOT NULL DEFAULT 0,
    s4xx         INTEGER NOT NULL DEFAULT 0,
    s5xx         INTEGER NOT NULL DEFAULT 0,
    serr         INTEGER NOT NULL DEFAULT 0,
    bytes_in     INTEGER NOT NULL DEFAULT 0,
    bytes_out    INTEGER NOT NULL DEFAULT 0,
    lat_sum_ms   INTEGER NOT NULL DEFAULT 0,
    lat_max_ms   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (host_id, ts)
) WITHOUT ROWID;
-- Pruning and all-host queries both scan by time, so index it on its own.
CREATE INDEX idx_metrics_samples_ts ON metrics_samples (ts);
