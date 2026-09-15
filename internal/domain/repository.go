package domain

import (
	"context"
	"time"
)

// The interfaces below are the only way the rest of ponzproxy reaches
// persistence. They live here, next to the types they carry, so that the
// store package depends on the domain and never the other way round.

// HostRepository stores virtual hosts together with their upstreams. Save and
// Delete must be atomic across both tables: a host is never visible without
// the upstreams it routes to.
type HostRepository interface {
	// List returns every host, upstreams included, ordered by name.
	List(ctx context.Context) ([]Host, error)
	// Get returns ErrNotFound when no host has the given id.
	Get(ctx context.Context, id int64) (*Host, error)
	// Create assigns an id and returns ErrConflict when one of the host's
	// domains is already routed elsewhere.
	Create(ctx context.Context, h *Host) error
	// Update replaces the host and its upstream set wholesale.
	Update(ctx context.Context, h *Host) error
	Delete(ctx context.Context, id int64) error
	// CountByCertificate reports how many hosts reference a certificate, so
	// deleting one still in use can be refused with ErrInUse.
	CountByCertificate(ctx context.Context, certID int64) (int, error)
}

// CertificateRepository stores key material and renewal metadata.
type CertificateRepository interface {
	List(ctx context.Context) ([]Certificate, error)
	Get(ctx context.Context, id int64) (*Certificate, error)
	Create(ctx context.Context, c *Certificate) error
	Update(ctx context.Context, c *Certificate) error
	Delete(ctx context.Context, id int64) error
	// DueForRenewal returns ACME certificates inside their renewal window.
	DueForRenewal(ctx context.Context, now time.Time) ([]Certificate, error)
}

// UserRepository stores control-plane operator accounts.
type UserRepository interface {
	List(ctx context.Context) ([]User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	GetByID(ctx context.Context, id int64) (*User, error)
	Create(ctx context.Context, u *User) error
	UpdatePassword(ctx context.Context, id int64, passwordHash string) error
	// UpdateRole changes what an account may do. The caller is responsible
	// for refusing to demote the last administrator.
	UpdateRole(ctx context.Context, id int64, role Role) error
	// CountAdmins is what makes that check possible without loading every
	// user.
	CountAdmins(ctx context.Context) (int, error)
	TouchLogin(ctx context.Context, id int64, at time.Time) error
	Delete(ctx context.Context, id int64) error
	Count(ctx context.Context) (int, error)
}

// Resolution is the granularity of a metrics query. Samples are written at the
// raw flush interval and rolled up on read, so no extra tables are needed.
type Resolution string

const (
	ResolutionMinute Resolution = "minute"
	ResolutionHour   Resolution = "hour"
	ResolutionDay    Resolution = "day"
)

func (r Resolution) Valid() bool {
	switch r {
	case ResolutionMinute, ResolutionHour, ResolutionDay:
		return true
	}
	return false
}

// Duration is the time each bucket of a resolution covers.
func (r Resolution) Duration() time.Duration {
	switch r {
	case ResolutionHour:
		return time.Hour
	case ResolutionDay:
		return 24 * time.Hour
	default:
		return time.Minute
	}
}

// MetricsQuery selects a window of history. A zero HostID means all hosts
// aggregated together.
type MetricsQuery struct {
	HostID     int64
	From       time.Time
	To         time.Time
	Resolution Resolution
}

// MetricsRepository persists the rollups that back the historical charts.
type MetricsRepository interface {
	// WriteSamples appends one flush interval's worth of samples.
	WriteSamples(ctx context.Context, samples []Sample) error
	// Query returns samples bucketed at the requested resolution, ordered
	// by time ascending.
	Query(ctx context.Context, q MetricsQuery) ([]Sample, error)
	// Prune deletes samples older than the cutoff and reports how many rows
	// were removed.
	Prune(ctx context.Context, before time.Time) (int64, error)
}
