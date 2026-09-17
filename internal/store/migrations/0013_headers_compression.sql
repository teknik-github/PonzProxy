-- Custom headers and response compression.
--
-- Both off for every existing host and every new one. A proxy that silently
-- starts rewriting headers, or rewriting bodies, is one whose output an
-- operator can no longer predict from the configuration in front of them.

-- Header rules, for a host or for one of its locations. One table rather than
-- two because the rules are identical in shape and in meaning; which of the
-- two owns a row is the only difference, and exactly one of the columns is set.
--
-- direction is 'request' (what the backend receives) or 'response' (what the
-- visitor receives). They are separate lists rather than one because adding an
-- internal auth header for a backend and adding a Content-Security-Policy for
-- a browser are different jobs that happen to share a mechanism.
CREATE TABLE header_rules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    host_id     INTEGER REFERENCES hosts (id) ON DELETE CASCADE,
    location_id INTEGER REFERENCES host_locations (id) ON DELETE CASCADE,
    direction   TEXT    NOT NULL CHECK (direction IN ('request', 'response')),
    name        TEXT    NOT NULL,
    value       TEXT    NOT NULL DEFAULT '',
    remove      INTEGER NOT NULL DEFAULT 0,
    position    INTEGER NOT NULL,
    -- Exactly one owner. A row belonging to both, or to neither, would be
    -- applied twice or never, and both are silent.
    CHECK ((host_id IS NULL) <> (location_id IS NULL))
);

CREATE INDEX idx_header_rules_host ON header_rules (host_id);
CREATE INDEX idx_header_rules_location ON header_rules (location_id);

-- Response compression. Most backends do not compress, so without this the
-- bytes go out whole: this project's own console bundle is 1019 kB
-- uncompressed and 291 kB gzipped, and the difference is transfer the
-- operator is billed for.
ALTER TABLE hosts ADD COLUMN compress_enabled   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hosts ADD COLUMN compress_min_bytes INTEGER NOT NULL DEFAULT 1024;
ALTER TABLE hosts ADD COLUMN compress_level     INTEGER NOT NULL DEFAULT 5;

-- The content types worth compressing. Anything already compressed — images,
-- video, archives — is absent on purpose: re-compressing spends CPU to make
-- the response very slightly larger. text/event-stream is absent for a
-- different reason: compressing a stream means buffering it, and an event that
-- arrives in a batch minutes later is not an event.
--
-- This is a snapshot of domain.DefaultCompressionTypes at the time of the
-- migration and is deliberately not kept in step with it: a migration records
-- what a database was given on one day.
CREATE TABLE host_compress_types (
    host_id  INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    type     TEXT    NOT NULL,
    position INTEGER NOT NULL,
    PRIMARY KEY (host_id, type)
) WITHOUT ROWID;

INSERT INTO host_compress_types (host_id, type, position)
SELECT h.id, t.type, t.position
FROM hosts AS h
CROSS JOIN (
    SELECT 'text/html' AS type, 0 AS position
    UNION ALL SELECT 'text/css', 1
    UNION ALL SELECT 'text/plain', 2
    UNION ALL SELECT 'text/xml', 3
    UNION ALL SELECT 'text/javascript', 4
    UNION ALL SELECT 'application/javascript', 5
    UNION ALL SELECT 'application/x-javascript', 6
    UNION ALL SELECT 'application/json', 7
    UNION ALL SELECT 'application/xml', 8
    UNION ALL SELECT 'application/rss+xml', 9
    UNION ALL SELECT 'image/svg+xml', 10
    UNION ALL SELECT 'application/wasm', 11
) AS t;
