-- Passive health: eject a backend after repeated connection failures on real
-- traffic, without waiting for the next active probe.
--
-- Defaults are chosen so existing hosts gain the protection on upgrade: three
-- consecutive failures, back in rotation after 30 seconds. A host that wants
-- the old behaviour sets ph_enabled to 0.
ALTER TABLE hosts ADD COLUMN ph_enabled       INTEGER NOT NULL DEFAULT 1;
ALTER TABLE hosts ADD COLUMN ph_max_fails     INTEGER NOT NULL DEFAULT 3 CHECK (ph_max_fails >= 1);
ALTER TABLE hosts ADD COLUMN ph_eject_for_ms  INTEGER NOT NULL DEFAULT 30000 CHECK (ph_eject_for_ms >= 1000);
