-- Maintenance mode and per-host error pages.
--
-- Off for every existing host and every new one. The wording below is what an
-- operator gets the moment they switch it on, so that turning maintenance on
-- during an incident is one decision rather than one decision and a writing
-- exercise.
--
-- 503 is the default status because it is the only one that means "temporarily
-- unavailable, come back": crawlers understand it as such and do not drop the
-- page, where a 200 would tell them the maintenance notice is the real content.
ALTER TABLE hosts ADD COLUMN maint_enabled     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hosts ADD COLUMN maint_status      INTEGER NOT NULL DEFAULT 503;
ALTER TABLE hosts ADD COLUMN maint_title       TEXT    NOT NULL DEFAULT 'Down for maintenance';
ALTER TABLE hosts ADD COLUMN maint_message     TEXT    NOT NULL DEFAULT 'We are making some changes and will be back shortly. Thank you for your patience.';
ALTER TABLE hosts ADD COLUMN maint_retry_after INTEGER NOT NULL DEFAULT 300;

ALTER TABLE hosts ADD COLUMN error_page_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hosts ADD COLUMN error_page_title   TEXT    NOT NULL DEFAULT 'This site is temporarily unavailable';
ALTER TABLE hosts ADD COLUMN error_page_message TEXT    NOT NULL DEFAULT 'Something went wrong on our side. Please try again in a few moments.';

-- Addresses that reach the backend while the maintenance page is up. Without
-- this, switching maintenance on locks out the person performing it, who then
-- cannot check that the work succeeded — so they turn it off to look, which
-- defeats the point.
CREATE TABLE host_maint_allow (
    host_id  INTEGER NOT NULL REFERENCES hosts (id) ON DELETE CASCADE,
    cidr     TEXT    NOT NULL,
    position INTEGER NOT NULL,
    PRIMARY KEY (host_id, cidr)
) WITHOUT ROWID;
