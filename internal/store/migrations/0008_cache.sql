-- Per-host in-memory cache of static assets.
--
-- Off for every existing host. A cache that starts answering someone's live
-- traffic without being asked turns a stale asset into the proxy's fault, and
-- the operator has no reason to suspect the proxy.
--
-- The limits are stored per host rather than as one process-wide setting
-- because the budget is what stops a busy host from filling memory, and a
-- number that means "all of them together" is one nobody can reason about.
ALTER TABLE hosts ADD COLUMN cache_enabled          INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hosts ADD COLUMN cache_ttl_ms           INTEGER NOT NULL DEFAULT 300000;
ALTER TABLE hosts ADD COLUMN cache_max_ttl_ms       INTEGER NOT NULL DEFAULT 86400000;
ALTER TABLE hosts ADD COLUMN cache_max_object_bytes INTEGER NOT NULL DEFAULT 1048576;
ALTER TABLE hosts ADD COLUMN cache_max_bytes        INTEGER NOT NULL DEFAULT 67108864;

-- The paths a host caches. An entry starting with "." is an extension matched
-- against the end of the request path; anything else is a path prefix.
--
-- position keeps the list in the order the operator wrote it, which is the
-- order the console shows back. Matching does not depend on it.
CREATE TABLE host_cache_paths (
    host_id  INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    path     TEXT    NOT NULL,
    position INTEGER NOT NULL,
    PRIMARY KEY (host_id, path)
) WITHOUT ROWID;

-- Existing hosts are given the same starting selection a new host gets, so
-- that switching the cache on is one decision rather than two. Caching stays
-- off until someone asks for it; this list only decides what it would apply to.
--
-- This is a snapshot of domain.DefaultCachePaths at the time of the migration
-- and is deliberately not kept in step with it: a migration describes what a
-- database was given on one day, not what the code would choose today.
INSERT INTO host_cache_paths (host_id, path, position)
SELECT h.id, p.path, p.position
FROM hosts AS h
CROSS JOIN (
    SELECT '.js' AS path, 0 AS position
    UNION ALL SELECT '.mjs', 1
    UNION ALL SELECT '.css', 2
    UNION ALL SELECT '.woff', 3
    UNION ALL SELECT '.woff2', 4
    UNION ALL SELECT '.ico', 5
    UNION ALL SELECT '.png', 6
    UNION ALL SELECT '.jpg', 7
    UNION ALL SELECT '.jpeg', 8
    UNION ALL SELECT '.gif', 9
    UNION ALL SELECT '.svg', 10
    UNION ALL SELECT '.webp', 11
    UNION ALL SELECT '.avif', 12
    UNION ALL SELECT '/assets/', 13
    UNION ALL SELECT '/static/', 14
) AS p;
