package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// redirectRepo is a view over Store, the same shape hostRepo uses so both
// repositories share one handle.
type redirectRepo Store

// Redirects returns the redirect repository. It sits here rather than beside
// the other accessors in store.go so that the feature is contained in one
// file; Go does not care which file a method is declared in.
func (s *Store) Redirects() domain.RedirectRepository { return (*redirectRepo)(s) }

const redirectColumns = `
	id, name, enabled, target, status_code, preserve_path, certificate_id,
	created_at, updated_at`

// routedElsewhereMarker is the text the 0004 triggers raise when a domain is
// claimed by the other table. SQLite reports a RAISE(ABORT) as a plain
// message, not as a constraint violation translateErr recognises, so the
// marker is how the two are connected.
const routedElsewhereMarker = "domain is already routed elsewhere"

func (r *redirectRepo) List(ctx context.Context) ([]domain.Redirect, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+redirectColumns+` FROM redirects ORDER BY name, id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	redirects := make([]domain.Redirect, 0, 8)
	for rows.Next() {
		rd, err := scanRedirect(rows)
		if err != nil {
			return nil, err
		}
		redirects = append(redirects, *rd)
	}
	if err := rows.Err(); err != nil {
		return nil, translateErr(err)
	}

	// Index only once the slice has stopped growing, for the same reason
	// hostRepo.List does: append can move the backing array.
	byID := make(map[int64]*domain.Redirect, len(redirects))
	for i := range redirects {
		byID[redirects[i].ID] = &redirects[i]
	}
	if err := r.attachDomains(ctx, byID); err != nil {
		return nil, err
	}
	return redirects, nil
}

func (r *redirectRepo) Get(ctx context.Context, id int64) (*domain.Redirect, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+redirectColumns+` FROM redirects WHERE id = ?`, id)
	rd, err := scanRedirect(row)
	if err != nil {
		return nil, err
	}
	if err := r.attachDomains(ctx, map[int64]*domain.Redirect{rd.ID: rd}); err != nil {
		return nil, err
	}
	return rd, nil
}

func (r *redirectRepo) Create(ctx context.Context, rd *domain.Redirect) error {
	now := time.Now().UTC()
	rd.CreatedAt, rd.UpdatedAt = now, now

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO redirects (
				name, enabled, target, status_code, preserve_path,
				certificate_id, created_at, updated_at
			) VALUES (?,?,?,?,?,?,?,?)`,
			rd.Name, rd.Enabled, rd.Target, rd.StatusCode, rd.PreservePath,
			certIDArg(rd.CertificateID), rd.CreatedAt.Unix(), rd.UpdatedAt.Unix())
		if err != nil {
			return translateErr(err)
		}
		if rd.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return writeRedirectDomains(ctx, tx, rd)
	})
}

func (r *redirectRepo) Update(ctx context.Context, rd *domain.Redirect) error {
	rd.UpdatedAt = time.Now().UTC()

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE redirects SET
				name = ?, enabled = ?, target = ?, status_code = ?,
				preserve_path = ?, certificate_id = ?, updated_at = ?
			WHERE id = ?`,
			rd.Name, rd.Enabled, rd.Target, rd.StatusCode, rd.PreservePath,
			certIDArg(rd.CertificateID), rd.UpdatedAt.Unix(), rd.ID)
		if err != nil {
			return translateErr(err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return domain.ErrNotFound
		}

		// Domains are replaced wholesale, as a host's are. Deleting first
		// inside the transaction is also what lets a redirect keep a
		// domain it already owns across an edit.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM redirect_domains WHERE redirect_id = ?`, rd.ID); err != nil {
			return translateErr(err)
		}
		return writeRedirectDomains(ctx, tx, rd)
	})
}

func (r *redirectRepo) Delete(ctx context.Context, id int64) error {
	// redirect_domains cascades.
	res, err := r.db.ExecContext(ctx, `DELETE FROM redirects WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *redirectRepo) CountByCertificate(ctx context.Context, certID int64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM redirects WHERE certificate_id = ?`, certID).Scan(&n)
	return n, translateErr(err)
}

// attachDomains loads every child row in one query, so listing stays two round
// trips no matter how many redirects are configured.
func (r *redirectRepo) attachDomains(ctx context.Context, byID map[int64]*domain.Redirect) error {
	if len(byID) == 0 {
		return nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT redirect_id, domain FROM redirect_domains ORDER BY redirect_id, position`)
	if err != nil {
		return translateErr(err)
	}
	defer rows.Close()

	for rows.Next() {
		var redirectID int64
		var d string
		if err := rows.Scan(&redirectID, &d); err != nil {
			return err
		}
		if rd, ok := byID[redirectID]; ok {
			rd.Domains = append(rd.Domains, d)
		}
	}
	return translateErr(rows.Err())
}

// writeRedirectDomains inserts the domain rows for a redirect that has just
// been created or had its domains cleared.
func writeRedirectDomains(ctx context.Context, tx *sql.Tx, rd *domain.Redirect) error {
	for i, d := range rd.Domains {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO redirect_domains (redirect_id, domain, position) VALUES (?,?,?)`,
			rd.ID, d, i); err != nil {
			return redirectDomainConflict(err, d)
		}
	}
	return nil
}

// redirectDomainConflict names the offending domain, whichever of the two
// guards rejected it: the UNIQUE column when another redirect holds it, or the
// cross-table trigger when a host does. The operator's fix is the same either
// way, so the message does not distinguish them beyond saying it is taken.
func redirectDomainConflict(err error, d string) error {
	if err == nil {
		return nil
	}
	translated := translateErr(err)
	if errorIsConflict(translated) || strings.Contains(err.Error(), routedElsewhereMarker) {
		return fmt.Errorf("%w: domain %q is already routed by another host or redirect",
			domain.ErrConflict, d)
	}
	return translated
}

func scanRedirect(sc scanner) (*domain.Redirect, error) {
	var (
		rd      domain.Redirect
		certID  sql.NullInt64
		created int64
		updated int64
	)
	err := sc.Scan(&rd.ID, &rd.Name, &rd.Enabled, &rd.Target, &rd.StatusCode,
		&rd.PreservePath, &certID, &created, &updated)
	if err != nil {
		return nil, translateErr(err)
	}

	if certID.Valid {
		id := certID.Int64
		rd.CertificateID = &id
	}
	rd.CreatedAt = time.Unix(created, 0).UTC()
	rd.UpdatedAt = time.Unix(updated, 0).UTC()
	return &rd, nil
}
