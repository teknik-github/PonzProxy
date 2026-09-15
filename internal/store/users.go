package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

type userRepo Store

const userColumns = `id, username, password_hash, role, created_at, last_login_at`

func (r *userRepo) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY username`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	users := make([]domain.User, 0, 4)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, translateErr(rows.Err())
}

func (r *userRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	return scanUser(r.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ?`,
		domain.NormalizeUsername(username)))
}

func (r *userRepo) GetByID(ctx context.Context, id int64) (*domain.User, error) {
	return scanUser(r.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

func (r *userRepo) Create(ctx context.Context, u *domain.User) error {
	u.Username = domain.NormalizeUsername(u.Username)
	u.CreatedAt = time.Now().UTC()

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, created_at)
		VALUES (?,?,?,?)`,
		u.Username, u.PasswordHash, string(u.Role), u.CreatedAt.Unix())
	if err != nil {
		return translateErr(err)
	}
	u.ID, err = res.LastInsertId()
	return err
}

func (r *userRepo) UpdatePassword(ctx context.Context, id int64, passwordHash string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id)
	if err != nil {
		return translateErr(err)
	}
	return requireOneRow(res)
}

// TouchLogin records a successful sign-in. A failure here must never fail the
// login itself, so callers log the error and continue.
func (r *userRepo) TouchLogin(ctx context.Context, id int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE users SET last_login_at = ? WHERE id = ?`, at.Unix(), id)
	return translateErr(err)
}

func (r *userRepo) UpdateRole(ctx context.Context, id int64, role domain.Role) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE users SET role = ? WHERE id = ?`, string(role), id)
	if err != nil {
		return translateErr(err)
	}
	return requireOneRow(res)
}

func (r *userRepo) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = ?`, string(domain.RoleAdmin)).Scan(&n)
	return n, translateErr(err)
}

func (r *userRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return translateErr(err)
	}
	return requireOneRow(res)
}

func (r *userRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, translateErr(err)
}

func scanUser(sc scanner) (*domain.User, error) {
	var (
		u         domain.User
		role      string
		created   int64
		lastLogin sql.NullInt64
	)
	if err := sc.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &created, &lastLogin); err != nil {
		return nil, translateErr(err)
	}
	u.Role = domain.Role(role)
	u.CreatedAt = time.Unix(created, 0).UTC()
	u.LastLoginAt = nullTime(lastLogin)
	return &u, nil
}

// requireOneRow turns "the UPDATE matched nothing" into ErrNotFound, which is
// what every caller actually wants to distinguish.
func requireOneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
