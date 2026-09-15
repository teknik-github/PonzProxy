package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

type certRepo Store

const certColumns = `
	id, name, source, certificate_pem, private_key_pem, issuer, not_before,
	not_after, challenge, dns_provider, dns_credentials, last_error,
	last_issued_at, created_at, updated_at`

func (r *certRepo) List(ctx context.Context) ([]domain.Certificate, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+certColumns+` FROM certificates ORDER BY name, id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	certs := make([]domain.Certificate, 0, 8)
	for rows.Next() {
		c, err := scanCertificate(rows)
		if err != nil {
			return nil, err
		}
		certs = append(certs, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, translateErr(err)
	}

	byID := make(map[int64]*domain.Certificate, len(certs))
	for i := range certs {
		byID[certs[i].ID] = &certs[i]
	}
	if err := r.attachDomains(ctx, byID); err != nil {
		return nil, err
	}
	return certs, nil
}

func (r *certRepo) Get(ctx context.Context, id int64) (*domain.Certificate, error) {
	c, err := scanCertificate(r.db.QueryRowContext(ctx,
		`SELECT `+certColumns+` FROM certificates WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if err := r.attachDomains(ctx, map[int64]*domain.Certificate{c.ID: c}); err != nil {
		return nil, err
	}
	return c, nil
}

func (r *certRepo) Create(ctx context.Context, c *domain.Certificate) error {
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO certificates (
				name, source, certificate_pem, private_key_pem, issuer,
				not_before, not_after, challenge, dns_provider, dns_credentials,
				last_error, last_issued_at, created_at, updated_at
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.Name, string(c.Source), c.CertificatePEM, c.PrivateKeyPEM, c.Issuer,
			unixOrZero(c.NotBefore), unixOrZero(c.NotAfter), string(c.Challenge),
			c.DNSProvider, c.DNSCredentials, c.LastError,
			timePtrToNull(c.LastIssued), c.CreatedAt.Unix(), c.UpdatedAt.Unix())
		if err != nil {
			return translateErr(err)
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return writeCertDomains(ctx, tx, c)
	})
}

func (r *certRepo) Update(ctx context.Context, c *domain.Certificate) error {
	c.UpdatedAt = time.Now().UTC()

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE certificates SET
				name = ?, source = ?, certificate_pem = ?, private_key_pem = ?,
				issuer = ?, not_before = ?, not_after = ?, challenge = ?,
				dns_provider = ?, dns_credentials = ?, last_error = ?,
				last_issued_at = ?, updated_at = ?
			WHERE id = ?`,
			c.Name, string(c.Source), c.CertificatePEM, c.PrivateKeyPEM, c.Issuer,
			unixOrZero(c.NotBefore), unixOrZero(c.NotAfter), string(c.Challenge),
			c.DNSProvider, c.DNSCredentials, c.LastError,
			timePtrToNull(c.LastIssued), c.UpdatedAt.Unix(), c.ID)
		if err != nil {
			return translateErr(err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return domain.ErrNotFound
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM certificate_domains WHERE certificate_id = ?`, c.ID); err != nil {
			return translateErr(err)
		}
		return writeCertDomains(ctx, tx, c)
	})
}

func (r *certRepo) Delete(ctx context.Context, id int64) error {
	// hosts.certificate_id is ON DELETE RESTRICT, so this fails while any
	// host still terminates TLS with the certificate.
	res, err := r.db.ExecContext(ctx, `DELETE FROM certificates WHERE id = ?`, id)
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

// DueForRenewal narrows to ACME rows in SQL and applies the precise renewal
// window in Go, where the rule already lives on the domain type.
func (r *certRepo) DueForRenewal(ctx context.Context, now time.Time) ([]domain.Certificate, error) {
	all, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	due := make([]domain.Certificate, 0, 4)
	for i := range all {
		if all[i].Source != domain.CertSourceACME {
			continue
		}
		// List omits nothing, but re-read key material only when needed.
		if all[i].NeedsRenewal(now) {
			due = append(due, all[i])
		}
	}
	return due, nil
}

func (r *certRepo) attachDomains(ctx context.Context, byID map[int64]*domain.Certificate) error {
	if len(byID) == 0 {
		return nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT certificate_id, domain FROM certificate_domains ORDER BY certificate_id, position`)
	if err != nil {
		return translateErr(err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var d string
		if err := rows.Scan(&id, &d); err != nil {
			return err
		}
		if c, ok := byID[id]; ok {
			c.Domains = append(c.Domains, d)
		}
	}
	return translateErr(rows.Err())
}

func writeCertDomains(ctx context.Context, tx *sql.Tx, c *domain.Certificate) error {
	for i, d := range c.Domains {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO certificate_domains (certificate_id, domain, position) VALUES (?,?,?)`,
			c.ID, d, i); err != nil {
			return translateErr(err)
		}
	}
	return nil
}

func scanCertificate(sc scanner) (*domain.Certificate, error) {
	var (
		c                   domain.Certificate
		source, challenge   string
		notBefore, notAfter int64
		lastIssued          sql.NullInt64
		created, updated    int64
	)
	err := sc.Scan(&c.ID, &c.Name, &source, &c.CertificatePEM, &c.PrivateKeyPEM,
		&c.Issuer, &notBefore, &notAfter, &challenge, &c.DNSProvider,
		&c.DNSCredentials, &c.LastError, &lastIssued, &created, &updated)
	if err != nil {
		return nil, translateErr(err)
	}

	c.Source = domain.CertSource(source)
	c.Challenge = domain.ChallengeType(challenge)
	c.NotBefore = timeOrZero(notBefore)
	c.NotAfter = timeOrZero(notAfter)
	c.LastIssued = nullTime(lastIssued)
	c.CreatedAt = time.Unix(created, 0).UTC()
	c.UpdatedAt = time.Unix(updated, 0).UTC()
	return &c, nil
}

// unixOrZero and timeOrZero keep "not set" as 0 in the column rather than
// storing the year-1 zero time as a large negative number.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(unix int64) time.Time {
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0).UTC()
}
