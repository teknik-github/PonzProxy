-- Named rule sets a host can put in front of its upstreams.
CREATE TABLE access_lists (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    -- satisfy_any relaxes the address rules and the credentials from AND to
    -- OR; see domain.EvaluateAccess for the exact contract.
    satisfy_any INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- CIDR rules, stored masked so the same block cannot be saved twice under two
-- spellings. Order is kept for the UI only: deny always beats allow, so the
-- decision does not depend on it.
CREATE TABLE access_list_rules (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    access_list_id INTEGER NOT NULL REFERENCES access_lists (id) ON DELETE CASCADE,
    action         TEXT    NOT NULL CHECK (action IN ('allow', 'deny')),
    cidr           TEXT    NOT NULL,
    position       INTEGER NOT NULL,
    UNIQUE (access_list_id, action, cidr)
);
CREATE INDEX idx_access_list_rules_list ON access_list_rules (access_list_id);

-- HTTP basic auth credentials. Only the bcrypt hash is stored, exactly as for
-- an operator account.
CREATE TABLE access_list_users (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    access_list_id INTEGER NOT NULL REFERENCES access_lists (id) ON DELETE CASCADE,
    username       TEXT    NOT NULL,
    password_hash  TEXT    NOT NULL,
    position       INTEGER NOT NULL,
    UNIQUE (access_list_id, username)
);
CREATE INDEX idx_access_list_users_list ON access_list_users (access_list_id);

-- Hosts opt in by reference. ON DELETE RESTRICT, like certificate_id: a list
-- must not disappear from under a host that is relying on it to keep traffic
-- out. NULL means the host is open to everyone, which is the pre-existing
-- behaviour every current row keeps.
ALTER TABLE hosts ADD COLUMN access_list_id INTEGER REFERENCES access_lists (id) ON DELETE RESTRICT;
CREATE INDEX idx_hosts_access_list ON hosts (access_list_id);
