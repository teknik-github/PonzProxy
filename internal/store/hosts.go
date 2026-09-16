package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// hostRepo is a view over Store, so repositories share one handle without
// holding a back-pointer or duplicating construction.
type hostRepo Store

const hostColumns = `
	id, name, enabled, algorithm, certificate_id, access_list_id, force_https, hsts_max_age,
	websocket_support, preserve_host, hc_enabled, hc_path, hc_interval_ms,
	hc_timeout_ms, hc_healthy_threshold, hc_unhealthy_threshold,
	hc_expect_status, ph_enabled, ph_max_fails, ph_eject_for_ms,
	log_enabled, log_include_query, guardian_mode, guardian_max_uri,
	cache_enabled, cache_ttl_ms, cache_max_ttl_ms, cache_max_object_bytes,
	cache_max_bytes, limit_mode, limit_rps, limit_burst, limit_max_conns,
	limit_max_body, created_at, updated_at`

func (r *hostRepo) List(ctx context.Context) ([]domain.Host, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+hostColumns+` FROM hosts ORDER BY name, id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	hosts := make([]domain.Host, 0, 16)
	byID := make(map[int64]*domain.Host)
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, *h)
	}
	if err := rows.Err(); err != nil {
		return nil, translateErr(err)
	}
	// Index after the slice stops growing: appending can move the backing
	// array, which would leave earlier pointers dangling.
	for i := range hosts {
		byID[hosts[i].ID] = &hosts[i]
	}

	if err := r.attachDomains(ctx, byID); err != nil {
		return nil, err
	}
	if err := r.attachUpstreams(ctx, byID); err != nil {
		return nil, err
	}
	if err := r.attachGuardianRules(ctx, byID); err != nil {
		return nil, err
	}
	if err := r.attachLimitExempt(ctx, byID); err != nil {
		return nil, err
	}
	if err := r.attachCachePaths(ctx, byID); err != nil {
		return nil, err
	}
	return hosts, nil
}

func (r *hostRepo) Get(ctx context.Context, id int64) (*domain.Host, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+hostColumns+` FROM hosts WHERE id = ?`, id)
	h, err := scanHost(row)
	if err != nil {
		return nil, err
	}

	byID := map[int64]*domain.Host{h.ID: h}
	if err := r.attachDomains(ctx, byID); err != nil {
		return nil, err
	}
	if err := r.attachUpstreams(ctx, byID); err != nil {
		return nil, err
	}
	if err := r.attachGuardianRules(ctx, byID); err != nil {
		return nil, err
	}
	if err := r.attachLimitExempt(ctx, byID); err != nil {
		return nil, err
	}
	if err := r.attachCachePaths(ctx, byID); err != nil {
		return nil, err
	}
	return h, nil
}

func (r *hostRepo) Create(ctx context.Context, h *domain.Host) error {
	now := time.Now().UTC()
	h.CreatedAt, h.UpdatedAt = now, now

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO hosts (
				name, enabled, algorithm, certificate_id, access_list_id, force_https,
				hsts_max_age, websocket_support, preserve_host, hc_enabled,
				hc_path, hc_interval_ms, hc_timeout_ms, hc_healthy_threshold,
				hc_unhealthy_threshold, hc_expect_status,
				ph_enabled, ph_max_fails, ph_eject_for_ms,
				log_enabled, log_include_query, guardian_mode, guardian_max_uri,
				cache_enabled, cache_ttl_ms, cache_max_ttl_ms,
				cache_max_object_bytes, cache_max_bytes,
				limit_mode, limit_rps, limit_burst, limit_max_conns, limit_max_body,
				created_at, updated_at
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			hostInsertArgs(h)...)
		if err != nil {
			return translateErr(err)
		}
		if h.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return writeHostChildren(ctx, tx, h)
	})
}

func (r *hostRepo) Update(ctx context.Context, h *domain.Host) error {
	h.UpdatedAt = time.Now().UTC()

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE hosts SET
				name = ?, enabled = ?, algorithm = ?, certificate_id = ?,
				access_list_id = ?, force_https = ?, hsts_max_age = ?, websocket_support = ?,
				preserve_host = ?, hc_enabled = ?, hc_path = ?,
				hc_interval_ms = ?, hc_timeout_ms = ?, hc_healthy_threshold = ?,
				hc_unhealthy_threshold = ?, hc_expect_status = ?,
				ph_enabled = ?, ph_max_fails = ?, ph_eject_for_ms = ?,
				log_enabled = ?, log_include_query = ?,
				guardian_mode = ?, guardian_max_uri = ?,
				cache_enabled = ?, cache_ttl_ms = ?, cache_max_ttl_ms = ?,
				cache_max_object_bytes = ?, cache_max_bytes = ?,
				limit_mode = ?, limit_rps = ?, limit_burst = ?,
				limit_max_conns = ?, limit_max_body = ?, updated_at = ?
			WHERE id = ?`,
			hostUpdateArgs(h)...)
		if err != nil {
			return translateErr(err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return domain.ErrNotFound
		}

		// Children are replaced wholesale. Upstream ids are reassigned as a
		// result, which is why the balancer keys runtime state by
		// scheme+address rather than by id.
		if _, err := tx.ExecContext(ctx, `DELETE FROM host_domains WHERE host_id = ?`, h.ID); err != nil {
			return translateErr(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM upstreams WHERE host_id = ?`, h.ID); err != nil {
			return translateErr(err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM host_guardian_rules WHERE host_id = ?`, h.ID); err != nil {
			return translateErr(err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM host_cache_paths WHERE host_id = ?`, h.ID); err != nil {
			return translateErr(err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM host_limit_exempt WHERE host_id = ?`, h.ID); err != nil {
			return translateErr(err)
		}
		return writeHostChildren(ctx, tx, h)
	})
}

func (r *hostRepo) Delete(ctx context.Context, id int64) error {
	// host_domains and upstreams cascade, so one statement is enough.
	res, err := r.db.ExecContext(ctx, `DELETE FROM hosts WHERE id = ?`, id)
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

func (r *hostRepo) CountByCertificate(ctx context.Context, certID int64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hosts WHERE certificate_id = ?`, certID).Scan(&n)
	return n, translateErr(err)
}

// attachDomains and attachUpstreams load every child row in one query each,
// rather than one query per host, so listing stays O(3) round trips.
func (r *hostRepo) attachDomains(ctx context.Context, byID map[int64]*domain.Host) error {
	if len(byID) == 0 {
		return nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT host_id, domain FROM host_domains ORDER BY host_id, position`)
	if err != nil {
		return translateErr(err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int64
		var d string
		if err := rows.Scan(&hostID, &d); err != nil {
			return err
		}
		if h, ok := byID[hostID]; ok {
			h.Domains = append(h.Domains, d)
		}
	}
	return translateErr(rows.Err())
}

func (r *hostRepo) attachGuardianRules(ctx context.Context, byID map[int64]*domain.Host) error {
	if len(byID) == 0 {
		return nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT host_id, rule FROM host_guardian_rules ORDER BY host_id, rule`)
	if err != nil {
		return translateErr(err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int64
		var rule string
		if err := rows.Scan(&hostID, &rule); err != nil {
			return err
		}
		if h, ok := byID[hostID]; ok {
			h.Guardian.Rules = append(h.Guardian.Rules, domain.GuardianRule(rule))
		}
	}
	return translateErr(rows.Err())
}

// attachLimitExempt loads each host's exemptions and parses them once, so the
// request path never parses a CIDR. Normalize is what builds the parsed form,
// which is why it runs here rather than being left to the caller.
func (r *hostRepo) attachLimitExempt(ctx context.Context, byID map[int64]*domain.Host) error {
	if len(byID) == 0 {
		return nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT host_id, cidr FROM host_limit_exempt ORDER BY host_id, position`)
	if err != nil {
		return translateErr(err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int64
		var cidr string
		if err := rows.Scan(&hostID, &cidr); err != nil {
			return err
		}
		if h, ok := byID[hostID]; ok {
			h.TrafficLimits.Exempt = append(h.TrafficLimits.Exempt, cidr)
		}
	}
	if err := translateErr(rows.Err()); err != nil {
		return err
	}
	for _, h := range byID {
		h.TrafficLimits.Normalize()
	}
	return nil
}

func (r *hostRepo) attachCachePaths(ctx context.Context, byID map[int64]*domain.Host) error {
	if len(byID) == 0 {
		return nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT host_id, path FROM host_cache_paths ORDER BY host_id, position`)
	if err != nil {
		return translateErr(err)
	}
	defer rows.Close()

	for rows.Next() {
		var hostID int64
		var path string
		if err := rows.Scan(&hostID, &path); err != nil {
			return err
		}
		if h, ok := byID[hostID]; ok {
			h.Cache.Paths = append(h.Cache.Paths, path)
		}
	}
	return translateErr(rows.Err())
}

func (r *hostRepo) attachUpstreams(ctx context.Context, byID map[int64]*domain.Host) error {
	if len(byID) == 0 {
		return nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, host_id, scheme, address, weight, max_conns, enabled, skip_tls_verify
		FROM upstreams ORDER BY host_id, position, id`)
	if err != nil {
		return translateErr(err)
	}
	defer rows.Close()

	for rows.Next() {
		var u domain.Upstream
		if err := rows.Scan(&u.ID, &u.HostID, &u.Scheme, &u.Address,
			&u.Weight, &u.MaxConns, &u.Enabled, &u.SkipTLSVerify); err != nil {
			return err
		}
		if h, ok := byID[u.HostID]; ok {
			h.Upstreams = append(h.Upstreams, u)
		}
	}
	return translateErr(rows.Err())
}

// writeHostChildren inserts the domain and upstream rows for a host that has
// just been created or had its children cleared.
func writeHostChildren(ctx context.Context, tx *sql.Tx, h *domain.Host) error {
	for i, d := range h.Domains {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO host_domains (host_id, domain, position) VALUES (?,?,?)`,
			h.ID, d, i); err != nil {
			return domainConflict(err, d)
		}
	}
	for _, rule := range h.Guardian.Rules {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO host_guardian_rules (host_id, rule) VALUES (?,?)`,
			h.ID, string(rule)); err != nil {
			return translateErr(err)
		}
	}
	for i, p := range h.Cache.Paths {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO host_cache_paths (host_id, path, position) VALUES (?,?,?)`,
			h.ID, p, i); err != nil {
			return translateErr(err)
		}
	}
	for i, c := range h.TrafficLimits.Exempt {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO host_limit_exempt (host_id, cidr, position) VALUES (?,?,?)`,
			h.ID, c, i); err != nil {
			return translateErr(err)
		}
	}
	for i := range h.Upstreams {
		u := &h.Upstreams[i]
		u.HostID = h.ID
		res, err := tx.ExecContext(ctx, `
			INSERT INTO upstreams (host_id, scheme, address, weight, max_conns,
				enabled, skip_tls_verify, position)
			VALUES (?,?,?,?,?,?,?,?)`,
			u.HostID, u.Scheme, u.Address, u.Weight, u.MaxConns,
			u.Enabled, u.SkipTLSVerify, i)
		if err != nil {
			return translateErr(err)
		}
		if u.ID, err = res.LastInsertId(); err != nil {
			return err
		}
	}
	return nil
}

// domainConflict turns the UNIQUE violation on host_domains into a message
// that names the offending domain, since that is the only detail the operator
// needs to fix it.
func domainConflict(err error, d string) error {
	translated := translateErr(err)
	// The cross-table guard raises its own message rather than a UNIQUE
	// violation, so it has to be recognised separately or a host taking a
	// redirect's domain would surface as an internal error.
	if translated != nil &&
		(errorIsConflict(translated) || strings.Contains(err.Error(), routedElsewhereMarker)) {
		return fmt.Errorf("%w: domain %q is already routed by another host or redirect",
			domain.ErrConflict, d)
	}
	return translated
}

func hostInsertArgs(h *domain.Host) []any {
	return []any{
		h.Name, h.Enabled, string(h.Algorithm), certIDArg(h.CertificateID),
		certIDArg(h.AccessListID),
		h.ForceHTTPS, h.HSTSMaxAge, h.WebSocketSupport, h.PreserveHost,
		h.HealthCheck.Enabled, h.HealthCheck.Path,
		h.HealthCheck.Interval.Milliseconds(), h.HealthCheck.Timeout.Milliseconds(),
		h.HealthCheck.HealthyThreshold, h.HealthCheck.UnhealthyThreshold,
		h.HealthCheck.ExpectStatus,
		h.PassiveHealth.Enabled, h.PassiveHealth.MaxFails,
		h.PassiveHealth.EjectFor.Milliseconds(),
		h.AccessLog.Enabled, h.AccessLog.IncludeQuery,
		string(h.Guardian.Mode), h.Guardian.MaxURILength,
		h.Cache.Enabled, h.Cache.TTL.Milliseconds(), h.Cache.MaxTTL.Milliseconds(),
		h.Cache.MaxObjectBytes, h.Cache.MaxBytes,
		string(h.TrafficLimits.Mode), h.TrafficLimits.RequestsPerSecond,
		h.TrafficLimits.Burst, h.TrafficLimits.MaxConcurrent, h.TrafficLimits.MaxBodyBytes,
		h.CreatedAt.Unix(), h.UpdatedAt.Unix(),
	}
}

func hostUpdateArgs(h *domain.Host) []any {
	return []any{
		h.Name, h.Enabled, string(h.Algorithm), certIDArg(h.CertificateID),
		certIDArg(h.AccessListID),
		h.ForceHTTPS, h.HSTSMaxAge, h.WebSocketSupport, h.PreserveHost,
		h.HealthCheck.Enabled, h.HealthCheck.Path,
		h.HealthCheck.Interval.Milliseconds(), h.HealthCheck.Timeout.Milliseconds(),
		h.HealthCheck.HealthyThreshold, h.HealthCheck.UnhealthyThreshold,
		h.HealthCheck.ExpectStatus,
		h.PassiveHealth.Enabled, h.PassiveHealth.MaxFails,
		h.PassiveHealth.EjectFor.Milliseconds(),
		h.AccessLog.Enabled, h.AccessLog.IncludeQuery,
		string(h.Guardian.Mode), h.Guardian.MaxURILength,
		h.Cache.Enabled, h.Cache.TTL.Milliseconds(), h.Cache.MaxTTL.Milliseconds(),
		h.Cache.MaxObjectBytes, h.Cache.MaxBytes,
		string(h.TrafficLimits.Mode), h.TrafficLimits.RequestsPerSecond,
		h.TrafficLimits.Burst, h.TrafficLimits.MaxConcurrent, h.TrafficLimits.MaxBodyBytes,
		h.UpdatedAt.Unix(), h.ID,
	}
}

func certIDArg(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

// scanner covers both *sql.Row and *sql.Rows so scanHost serves Get and List.
type scanner interface{ Scan(dest ...any) error }

func scanHost(sc scanner) (*domain.Host, error) {
	var (
		h             domain.Host
		certID        sql.NullInt64
		accessListID  sql.NullInt64
		intervalMS    int64
		timeoutMS     int64
		ejectForMS    int64
		guardianMode  string
		cacheTTLMS    int64
		cacheMaxTTLMS int64
		limitMode     string
		created       int64
		updated       int64
		algorithm     string
	)
	err := sc.Scan(
		&h.ID, &h.Name, &h.Enabled, &algorithm, &certID, &accessListID, &h.ForceHTTPS,
		&h.HSTSMaxAge, &h.WebSocketSupport, &h.PreserveHost,
		&h.HealthCheck.Enabled, &h.HealthCheck.Path, &intervalMS, &timeoutMS,
		&h.HealthCheck.HealthyThreshold, &h.HealthCheck.UnhealthyThreshold,
		&h.HealthCheck.ExpectStatus,
		&h.PassiveHealth.Enabled, &h.PassiveHealth.MaxFails, &ejectForMS,
		&h.AccessLog.Enabled, &h.AccessLog.IncludeQuery,
		&guardianMode, &h.Guardian.MaxURILength,
		&h.Cache.Enabled, &cacheTTLMS, &cacheMaxTTLMS,
		&h.Cache.MaxObjectBytes, &h.Cache.MaxBytes,
		&limitMode, &h.TrafficLimits.RequestsPerSecond, &h.TrafficLimits.Burst,
		&h.TrafficLimits.MaxConcurrent, &h.TrafficLimits.MaxBodyBytes,
		&created, &updated,
	)
	if err != nil {
		return nil, translateErr(err)
	}

	h.Algorithm = domain.Algorithm(algorithm)
	if certID.Valid {
		id := certID.Int64
		h.CertificateID = &id
	}
	if accessListID.Valid {
		id := accessListID.Int64
		h.AccessListID = &id
	}
	h.HealthCheck.Interval = time.Duration(intervalMS) * time.Millisecond
	h.HealthCheck.Timeout = time.Duration(timeoutMS) * time.Millisecond
	h.PassiveHealth.EjectFor = time.Duration(ejectForMS) * time.Millisecond
	h.Guardian.Mode = domain.Mode(guardianMode)
	h.TrafficLimits.Mode = domain.Mode(limitMode)
	h.Cache.TTL = time.Duration(cacheTTLMS) * time.Millisecond
	h.Cache.MaxTTL = time.Duration(cacheMaxTTLMS) * time.Millisecond
	h.CreatedAt = time.Unix(created, 0).UTC()
	h.UpdatedAt = time.Unix(updated, 0).UTC()
	return &h, nil
}
