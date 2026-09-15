-- Per-host request inspection.
--
-- Off for every existing host: switching inspection on for someone's live
-- traffic without being asked is how a proxy earns a reputation for breaking
-- things. The rule list is empty until an operator chooses.
ALTER TABLE hosts ADD COLUMN guardian_mode      TEXT    NOT NULL DEFAULT 'off'
    CHECK (guardian_mode IN ('off', 'detect', 'block'));
ALTER TABLE hosts ADD COLUMN guardian_max_uri   INTEGER NOT NULL DEFAULT 2048;

CREATE TABLE host_guardian_rules (
    host_id INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    rule    TEXT    NOT NULL,
    PRIMARY KEY (host_id, rule)
) WITHOUT ROWID;
