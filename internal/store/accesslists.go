package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// accessListRepo is a view over Store, like the other repositories.
type accessListRepo Store

// AccessLists returns the access list repository.
func (s *Store) AccessLists() domain.AccessListRepository { return (*accessListRepo)(s) }

const accessListColumns = `id, name, satisfy_any, created_at, updated_at`

func (r *accessListRepo) List(ctx context.Context) ([]domain.AccessList, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+accessListColumns+` FROM access_lists ORDER BY name, id`)
	if err != nil {
		return nil, translateErr(err)
	}
	defer rows.Close()

	lists := make([]domain.AccessList, 0, 8)
	for rows.Next() {
		l, err := scanAccessList(rows)
		if err != nil {
			return nil, err
		}
		lists = append(lists, *l)
	}
	if err := rows.Err(); err != nil {
		return nil, translateErr(err)
	}

	// Index after the slice stops growing: appending can move the backing
	// array, which would leave earlier pointers dangling.
	byID := make(map[int64]*domain.AccessList, len(lists))
	for i := range lists {
		byID[lists[i].ID] = &lists[i]
	}
	if err := r.attachChildren(ctx, byID); err != nil {
		return nil, err
	}
	return lists, nil
}

func (r *accessListRepo) Get(ctx context.Context, id int64) (*domain.AccessList, error) {
	l, err := scanAccessList(r.db.QueryRowContext(ctx,
		`SELECT `+accessListColumns+` FROM access_lists WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if err := r.attachChildren(ctx, map[int64]*domain.AccessList{l.ID: l}); err != nil {
		return nil, err
	}
	return l, nil
}

func (r *accessListRepo) Create(ctx context.Context, l *domain.AccessList) error {
	now := time.Now().UTC()
	l.CreatedAt, l.UpdatedAt = now, now

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO access_lists (name, satisfy_any, created_at, updated_at)
			VALUES (?,?,?,?)`,
			l.Name, l.SatisfyAny, l.CreatedAt.Unix(), l.UpdatedAt.Unix())
		if err != nil {
			return translateErr(err)
		}
		if l.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		return writeAccessListChildren(ctx, tx, l)
	})
}

func (r *accessListRepo) Update(ctx context.Context, l *domain.AccessList) error {
	l.UpdatedAt = time.Now().UTC()

	return (*Store)(r).withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE access_lists SET name = ?, satisfy_any = ?, updated_at = ?
			WHERE id = ?`,
			l.Name, l.SatisfyAny, l.UpdatedAt.Unix(), l.ID)
		if err != nil {
			return translateErr(err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return domain.ErrNotFound
		}

		// Children are replaced wholesale, as for a host's upstreams. Rule
		// and user ids are reassigned as a result; nothing keys runtime
		// state on them, because evaluation reads the whole list at once.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM access_list_rules WHERE access_list_id = ?`, l.ID); err != nil {
			return translateErr(err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM access_list_users WHERE access_list_id = ?`, l.ID); err != nil {
			return translateErr(err)
		}
		return writeAccessListChildren(ctx, tx, l)
	})
}

func (r *accessListRepo) Delete(ctx context.Context, id int64) error {
	// hosts.access_list_id is ON DELETE RESTRICT, so this fails as ErrInUse
	// while any host still relies on the list to keep traffic out. The
	// rules and users cascade.
	res, err := r.db.ExecContext(ctx, `DELETE FROM access_lists WHERE id = ?`, id)
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

func (r *accessListRepo) HostsUsing(ctx context.Context, id int64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hosts WHERE access_list_id = ?`, id).Scan(&n)
	return n, translateErr(err)
}

// attachChildren loads the rules and users for every list in one query each,
// rather than one query per list, so listing stays O(3) round trips.
func (r *accessListRepo) attachChildren(ctx context.Context, byID map[int64]*domain.AccessList) error {
	if len(byID) == 0 {
		return nil
	}

	rules, err := r.db.QueryContext(ctx, `
		SELECT id, access_list_id, action, cidr
		FROM access_list_rules ORDER BY access_list_id, position, id`)
	if err != nil {
		return translateErr(err)
	}
	defer rules.Close()

	for rules.Next() {
		var (
			rule   domain.AccessRule
			action string
		)
		if err := rules.Scan(&rule.ID, &rule.AccessListID, &action, &rule.CIDR); err != nil {
			return err
		}
		rule.Action = domain.AccessAction(action)
		if l, ok := byID[rule.AccessListID]; ok {
			l.Rules = append(l.Rules, rule)
		}
	}
	if err := rules.Err(); err != nil {
		return translateErr(err)
	}

	users, err := r.db.QueryContext(ctx, `
		SELECT id, access_list_id, username, password_hash
		FROM access_list_users ORDER BY access_list_id, position, id`)
	if err != nil {
		return translateErr(err)
	}
	defer users.Close()

	for users.Next() {
		var u domain.BasicAuthUser
		if err := users.Scan(&u.ID, &u.AccessListID, &u.Username, &u.PasswordHash); err != nil {
			return err
		}
		if l, ok := byID[u.AccessListID]; ok {
			l.BasicAuth = append(l.BasicAuth, u)
		}
	}
	return translateErr(users.Err())
}

// writeAccessListChildren inserts the rules and users of a list that has just
// been created or had its children cleared.
func writeAccessListChildren(ctx context.Context, tx *sql.Tx, l *domain.AccessList) error {
	for i := range l.Rules {
		rule := &l.Rules[i]
		rule.AccessListID = l.ID
		res, err := tx.ExecContext(ctx, `
			INSERT INTO access_list_rules (access_list_id, action, cidr, position)
			VALUES (?,?,?,?)`,
			rule.AccessListID, string(rule.Action), rule.CIDR, i)
		if err != nil {
			return ruleConflict(err, rule.CIDR)
		}
		if rule.ID, err = res.LastInsertId(); err != nil {
			return err
		}
	}

	for i := range l.BasicAuth {
		u := &l.BasicAuth[i]
		u.AccessListID = l.ID
		res, err := tx.ExecContext(ctx, `
			INSERT INTO access_list_users (access_list_id, username, password_hash, position)
			VALUES (?,?,?,?)`,
			u.AccessListID, u.Username, u.PasswordHash, i)
		if err != nil {
			return userConflict(err, u.Username)
		}
		if u.ID, err = res.LastInsertId(); err != nil {
			return err
		}
	}
	return nil
}

// ruleConflict and userConflict name the offending entry, since that is the
// only detail an operator needs to fix a duplicate.
func ruleConflict(err error, cidr string) error {
	translated := translateErr(err)
	if translated != nil && errorIsConflict(translated) {
		return fmt.Errorf("%w: %s is listed twice with the same action", domain.ErrConflict, cidr)
	}
	return translated
}

func userConflict(err error, username string) error {
	translated := translateErr(err)
	if translated != nil && errorIsConflict(translated) {
		return fmt.Errorf("%w: user %q is listed twice", domain.ErrConflict, username)
	}
	return translated
}

func scanAccessList(sc scanner) (*domain.AccessList, error) {
	var (
		l                domain.AccessList
		created, updated int64
	)
	if err := sc.Scan(&l.ID, &l.Name, &l.SatisfyAny, &created, &updated); err != nil {
		return nil, translateErr(err)
	}
	l.CreatedAt = time.Unix(created, 0).UTC()
	l.UpdatedAt = time.Unix(updated, 0).UTC()
	return &l, nil
}
