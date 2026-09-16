-- Per-host traffic budget, with an alert when it is passed.
--
-- Off for every host. A budget nobody set is not a budget, and a proxy that
-- starts sending "you are over" messages on someone else's numbers is a proxy
-- whose alert channel gets muted — taking the outage alerts with it.
--
-- The window is rolling rather than calendar: ponzproxy does not know the
-- operator's billing day, and guessing one would put the reset in the wrong
-- place every single month.
ALTER TABLE hosts ADD COLUMN usage_alert_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hosts ADD COLUMN usage_alert_bytes   INTEGER NOT NULL DEFAULT 107374182400;
ALTER TABLE hosts ADD COLUMN usage_alert_days    INTEGER NOT NULL DEFAULT 30;
