-- Alert destinations and the events each one subscribes to.
--
-- The URL is stored as written because it is what must be posted to, and for
-- a chat webhook the URL *is* the credential. It is never returned by the API;
-- anyone who can read the console should not thereby be able to post as this
-- installation.
CREATE TABLE alert_channels (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT    NOT NULL,
    type             TEXT    NOT NULL CHECK (type IN ('webhook')),
    url              TEXT    NOT NULL,
    enabled          INTEGER NOT NULL DEFAULT 1,
    min_interval_ms  INTEGER NOT NULL DEFAULT 300000 CHECK (min_interval_ms >= 60000),
    last_attempt_at  INTEGER,
    last_error       TEXT    NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

-- One row per subscribed event. A separate table rather than a delimited
-- column so "which channels want upstream_down" is an index lookup, which is
-- the question the dispatcher asks on every event.
CREATE TABLE alert_channel_events (
    channel_id INTEGER NOT NULL REFERENCES alert_channels (id) ON DELETE CASCADE,
    event      TEXT    NOT NULL,
    PRIMARY KEY (channel_id, event)
) WITHOUT ROWID;
CREATE INDEX idx_alert_channel_events_event ON alert_channel_events (event);
