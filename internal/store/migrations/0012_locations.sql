-- Path-based routing: one host can send a path prefix to its own backends.
--
-- Until now routing looked only at the Host header, so a domain was one thing.
-- Serving a frontend at / and an API at /api meant two subdomains, two
-- certificates or a wildcard, a CORS policy, and cookies that no longer shared
-- an origin — a lot of accidental complexity to work around a routing table
-- that read one header.
--
-- A location is not a whole host. It borrows the host's algorithm, health
-- checks, TLS, access list, limits and inspection, because those describe the
-- site rather than a path. Only the backends and the prefix differ.
--
-- No existing host gains one, so routing is unchanged until somebody adds a
-- location themselves.
CREATE TABLE host_locations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    host_id      INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    path         TEXT    NOT NULL,
    strip_prefix INTEGER NOT NULL DEFAULT 0,
    position     INTEGER NOT NULL,
    -- Two locations claiming the same path is not a preference between them;
    -- it is a configuration with no defined answer.
    UNIQUE (host_id, path)
);

CREATE INDEX idx_host_locations_host ON host_locations (host_id);

-- A location's backends. The same shape as upstreams, with location_id in
-- place of host_id: a location is a pool, and everything the balancer needs
-- about a backend is the same wherever it is configured.
CREATE TABLE location_upstreams (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    location_id     INTEGER NOT NULL REFERENCES host_locations (id) ON DELETE CASCADE,
    scheme          TEXT    NOT NULL,
    address         TEXT    NOT NULL,
    weight          INTEGER NOT NULL DEFAULT 1,
    max_conns       INTEGER NOT NULL DEFAULT 0,
    enabled         INTEGER NOT NULL DEFAULT 1,
    skip_tls_verify INTEGER NOT NULL DEFAULT 0,
    position        INTEGER NOT NULL
);

CREATE INDEX idx_location_upstreams_location ON location_upstreams (location_id);
