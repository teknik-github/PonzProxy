-- Per-host limits on what one client address may ask for.
--
-- Off for every existing host, and off for new ones. A limit that starts
-- refusing someone's live traffic without being asked is indistinguishable
-- from an outage, and the operator has no reason to suspect the proxy.
--
-- The defaults below are what an operator gets the moment they switch limits
-- on, not what is enforced today. They sit well above ordinary browsing: a
-- page load is a burst of a dozen requests, so a rate without headroom would
-- refuse a normal visitor on their first click.
ALTER TABLE hosts ADD COLUMN limit_mode          TEXT    NOT NULL DEFAULT 'off';
ALTER TABLE hosts ADD COLUMN limit_rps           INTEGER NOT NULL DEFAULT 50;
ALTER TABLE hosts ADD COLUMN limit_burst         INTEGER NOT NULL DEFAULT 100;
ALTER TABLE hosts ADD COLUMN limit_max_conns     INTEGER NOT NULL DEFAULT 40;
ALTER TABLE hosts ADD COLUMN limit_max_body      INTEGER NOT NULL DEFAULT 33554432;

-- Addresses the limits never apply to: monitoring probes, an office range, an
-- external health checker. Without this, switching limits on is liable to take
-- out your own uptime checks first — they are, by design, the most regular
-- traffic a host receives.
--
-- position keeps the operator's own ordering for display. Matching does not
-- depend on it.
CREATE TABLE host_limit_exempt (
    host_id  INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    cidr     TEXT    NOT NULL,
    position INTEGER NOT NULL,
    PRIMARY KEY (host_id, cidr)
) WITHOUT ROWID;
