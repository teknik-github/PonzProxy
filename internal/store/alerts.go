package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

type alertRepo Store

// AlertChannels returns the alert channel repository.
func (s *Store) AlertChannels() domain.AlertChannelRepository { return (*alertRepo)(s) }

const alertColumns = `
	id, name, type, url, enabled, min_interval_ms, last_attempt_at,
	last_error, created_at, updated_at`

func (r *alertRepo) List(ctx context.Context) ([]domain.AlertChannel, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+alertColumns+` FROM alert_channels ORDER BY name, id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	channels := make([]domain.AlertChannel, 0, 4)
	for rows.Next() {
		c, err := scanAlertChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, translateErr(err)
	}

	byID := make(map[int64]*domain.AlertChannel, len(channels))
	for i := range channels {
		byID[channels[i].ID] = &channels[i]
	}
	if err := r.attachEvents(ctx, byID); err != nil {
		return nil, err
	}
	return channels, nil
}

func (r *alertRepo) Get(ctx context.Context, id int64) (*domain.AlertChannel, error) {
	c, err := scanAlertChannel(r.db.QueryRowContext(ctx,
		`SELECT `+alertColumns+` FROM alert_channels WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if err := r.attachEvents(ctx, map[int64]*domain.AlertChannel{c.ID: c}); err != nil {
		return nil, err
	}
	return c, nil
}

func (r *alertRepo) Create(ctx context.Context, c *domain.AlertChannel) error {
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO alert_channels (
				name, type, url, enabled, min_interval_ms, created_at, updated_at
			) VALUES (?,?,?,?,?,?,?)`,
			c.Name, string(c.Type), c.URL, c.Enabled,
			c.MinInterval.Milliseconds(), c.CreatedAt.Unix(), c.UpdatedAt.Unix())
		if err != nil {
			return translateErr(err)
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return writeAlertEvents(ctx, tx, c)
	})
}

func (r *alertRepo) Update(ctx context.Context, c *domain.AlertChannel) error {
	c.UpdatedAt = time.Now().UTC()

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE alert_channels SET
				name = ?, type = ?, url = ?, enabled = ?,
				min_interval_ms = ?, updated_at = ?
			WHERE id = ?`,
			c.Name, string(c.Type), c.URL, c.Enabled,
			c.MinInterval.Milliseconds(), c.UpdatedAt.Unix(), c.ID)
		if err != nil {
			return translateErr(err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return domain.ErrNotFound
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM alert_channel_events WHERE channel_id = ?`, c.ID); err != nil {
			return translateErr(err)
		}
		return writeAlertEvents(ctx, tx, c)
	})
}

func (r *alertRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM alert_channels WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	return requireOneRow(res)
}

// RecordDelivery is called from the dispatcher after every attempt, so a
// channel that has silently stopped working shows up in the console.
func (r *alertRepo) RecordDelivery(ctx context.Context, id int64, at time.Time, deliveryErr string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE alert_channels SET last_attempt_at = ?, last_error = ? WHERE id = ?`,
		at.Unix(), deliveryErr, id)
	return translateErr(err)
}

func (r *alertRepo) attachEvents(ctx context.Context, byID map[int64]*domain.AlertChannel) error {
	if len(byID) == 0 {
		return nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT channel_id, event FROM alert_channel_events ORDER BY channel_id, event`)
	if err != nil {
		return translateErr(err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var event string
		if err := rows.Scan(&id, &event); err != nil {
			return err
		}
		if c, ok := byID[id]; ok {
			c.Events = append(c.Events, domain.AlertEvent(event))
		}
	}
	return translateErr(rows.Err())
}

func writeAlertEvents(ctx context.Context, tx *sql.Tx, c *domain.AlertChannel) error {
	for _, e := range c.Events {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO alert_channel_events (channel_id, event) VALUES (?,?)`,
			c.ID, string(e)); err != nil {
			return translateErr(err)
		}
	}
	return nil
}

func scanAlertChannel(sc scanner) (*domain.AlertChannel, error) {
	var (
		c           domain.AlertChannel
		channelType string
		intervalMS  int64
		lastAttempt sql.NullInt64
		created     int64
		updated     int64
	)
	if err := sc.Scan(&c.ID, &c.Name, &channelType, &c.URL, &c.Enabled,
		&intervalMS, &lastAttempt, &c.LastError, &created, &updated); err != nil {
		return nil, translateErr(err)
	}

	c.Type = domain.AlertChannelType(channelType)
	c.MinInterval = time.Duration(intervalMS) * time.Millisecond
	c.LastAttempt = nullTime(lastAttempt)
	c.CreatedAt = time.Unix(created, 0).UTC()
	c.UpdatedAt = time.Unix(updated, 0).UTC()
	return &c, nil
}
