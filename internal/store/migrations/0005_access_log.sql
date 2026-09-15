-- Searchable record of proxied requests, enabled per host.
--
-- Rows are written in batches by a background writer, never by a request, and
-- bounded by both age and count — a busy proxy would otherwise fill the disk.
-- WITHOUT ROWID is deliberately not used here: the table is append-heavy with
-- an autoincrement key, which is exactly the shape a rowid table handles best.
CREATE TABLE access_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          INTEGER NOT NULL,           -- unix milliseconds
    host_id     INTEGER NOT NULL,           -- 0 for traffic that matched no host
    method      TEXT    NOT NULL,
    path        TEXT    NOT NULL,
    status      INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    bytes_out   INTEGER NOT NULL,
    client_ip   TEXT    NOT NULL,
    upstream    TEXT    NOT NULL DEFAULT '',
    user_agent  TEXT    NOT NULL DEFAULT '',
    error       TEXT    NOT NULL DEFAULT ''
);

-- Every query is "most recent first", optionally narrowed to one host, so
-- these two cover the read path and the retention sweep alike.
CREATE INDEX idx_access_log_ts ON access_log (ts DESC);
CREATE INDEX idx_access_log_host_ts ON access_log (host_id, ts DESC);

-- Per-host switch. Off by default, including for hosts that already exist.
ALTER TABLE hosts ADD COLUMN log_enabled       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hosts ADD COLUMN log_include_query INTEGER NOT NULL DEFAULT 0;
