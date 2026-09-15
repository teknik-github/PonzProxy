-- Redirection hosts: domains answered with a Location header instead of being
-- proxied. Shaped like `hosts` deliberately, so the two read the same.
CREATE TABLE redirects (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT    NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 1,
    target         TEXT    NOT NULL,
    -- Only the four codes the domain layer allows. 307 and 308 keep the
    -- method and body; 301 and 302 let clients rewrite a POST into a GET.
    status_code    INTEGER NOT NULL DEFAULT 301 CHECK (status_code IN (301, 302, 307, 308)),
    preserve_path  INTEGER NOT NULL DEFAULT 1,
    certificate_id INTEGER REFERENCES certificates (id) ON DELETE RESTRICT,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
CREATE INDEX idx_redirects_certificate ON redirects (certificate_id);

-- One row per domain a redirect answers for, mirroring host_domains including
-- its UNIQUE constraint.
CREATE TABLE redirect_domains (
    redirect_id INTEGER NOT NULL REFERENCES redirects (id) ON DELETE CASCADE,
    domain      TEXT    NOT NULL UNIQUE,
    position    INTEGER NOT NULL
);
CREATE INDEX idx_redirect_domains_redirect ON redirect_domains (redirect_id);

-- Hosts and redirects share ONE domain namespace: a request carries a single
-- Host header, so a domain that is both proxied and redirected has no defined
-- answer. host_domains already makes that impossible within hosts with a
-- UNIQUE column, and redirect_domains does the same within redirects, but
-- SQLite cannot spell a UNIQUE constraint across two tables.
--
-- These triggers close the gap in both directions. Enforcing it in the schema
-- rather than in a repository read-then-write is what makes it actually hold:
-- a check in Go races with a concurrent insert from the other table and would
-- have to be repeated in every future writer, while a trigger runs inside the
-- same transaction as the insert it guards.
--
-- Both tables replace their rows wholesale on update (DELETE then INSERT), so
-- guarding INSERT is enough to cover edits as well as creates.
CREATE TRIGGER redirect_domain_not_already_a_host
BEFORE INSERT ON redirect_domains
FOR EACH ROW WHEN EXISTS (SELECT 1 FROM host_domains WHERE domain = NEW.domain)
BEGIN
    SELECT RAISE(ABORT, 'domain is already routed elsewhere');
END;

CREATE TRIGGER host_domain_not_already_a_redirect
BEFORE INSERT ON host_domains
FOR EACH ROW WHEN EXISTS (SELECT 1 FROM redirect_domains WHERE domain = NEW.domain)
BEGIN
    SELECT RAISE(ABORT, 'domain is already routed elsewhere');
END;
