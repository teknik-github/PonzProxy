package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

func sampleAccessList(name string) *domain.AccessList {
	l := &domain.AccessList{
		Name: name,
		Rules: []domain.AccessRule{
			{Action: domain.AccessAllow, CIDR: "10.0.0.0/8"},
			{Action: domain.AccessDeny, CIDR: "10.4.0.0/16"},
			{Action: domain.AccessAllow, CIDR: "2001:db8::/32"},
		},
		BasicAuth: []domain.BasicAuthUser{
			{Username: "alice", PasswordHash: "$2a$04$notarealhashbutlongenough"},
		},
	}
	l.Normalize()
	return l
}

func TestAccessListRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	l := sampleAccessList("office")
	if err := s.AccessLists().Create(ctx, l); err != nil {
		t.Fatalf("create: %v", err)
	}
	if l.ID == 0 {
		t.Fatal("create did not assign an id")
	}
	for i, r := range l.Rules {
		if r.ID == 0 || r.AccessListID != l.ID {
			t.Errorf("rule %d = %+v, want an id and the list's id", i, r)
		}
	}
	if l.BasicAuth[0].ID == 0 {
		t.Error("basic auth user did not get an id")
	}

	got, err := s.AccessLists().Get(ctx, l.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "office" || got.SatisfyAny {
		t.Errorf("unexpected list read back: %+v", got)
	}
	// Rules come back in the order they were written, which is what the UI
	// shows even though evaluation does not depend on it.
	want := []string{"10.0.0.0/8", "10.4.0.0/16", "2001:db8::/32"}
	if len(got.Rules) != len(want) {
		t.Fatalf("got %d rules, want %d", len(got.Rules), len(want))
	}
	for i, cidr := range want {
		if got.Rules[i].CIDR != cidr {
			t.Errorf("rule %d = %q, want %q", i, got.Rules[i].CIDR, cidr)
		}
	}
	if got.Rules[1].Action != domain.AccessDeny {
		t.Errorf("rule 1 action = %q, want deny", got.Rules[1].Action)
	}
	if len(got.BasicAuth) != 1 || got.BasicAuth[0].Username != "alice" {
		t.Fatalf("basic auth = %+v, want one user", got.BasicAuth)
	}
	if got.BasicAuth[0].PasswordHash != l.BasicAuth[0].PasswordHash {
		t.Error("the stored password hash did not survive the round trip")
	}
}

func TestAccessListUpdateReplacesChildren(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	l := sampleAccessList("office")
	if err := s.AccessLists().Create(ctx, l); err != nil {
		t.Fatalf("create: %v", err)
	}

	l.Name = "office vpn"
	l.SatisfyAny = true
	l.Rules = []domain.AccessRule{{Action: domain.AccessAllow, CIDR: "192.0.2.0/24"}}
	if err := s.AccessLists().Update(ctx, l); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.AccessLists().Get(ctx, l.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "office vpn" || !got.SatisfyAny {
		t.Errorf("list = %+v, want the renamed satisfy-any list", got)
	}
	if len(got.Rules) != 1 || got.Rules[0].CIDR != "192.0.2.0/24" {
		t.Errorf("rules = %+v, want only the new one", got.Rules)
	}
	if len(got.BasicAuth) != 1 {
		t.Errorf("basic auth = %+v, want the untouched user to survive", got.BasicAuth)
	}
}

func TestAccessListListIsScopedPerList(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	for _, name := range []string{"office", "admin"} {
		l := sampleAccessList(name)
		if err := s.AccessLists().Create(ctx, l); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	lists, err := s.AccessLists().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(lists) != 2 {
		t.Fatalf("got %d lists, want 2", len(lists))
	}
	// Ordered by name, and each one carries only its own children.
	if lists[0].Name != "admin" {
		t.Errorf("first list = %q, want the alphabetically first", lists[0].Name)
	}
	for _, l := range lists {
		if len(l.Rules) != 3 || len(l.BasicAuth) != 1 {
			t.Errorf("%q has %d rules and %d users, want 3 and 1",
				l.Name, len(l.Rules), len(l.BasicAuth))
		}
	}
}

func TestAccessListDuplicateRuleConflicts(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	// Normalize collapses duplicates, so reaching the constraint takes a
	// list that was never normalised — a corrupted client, in practice.
	l := &domain.AccessList{
		Name: "dupes",
		Rules: []domain.AccessRule{
			{Action: domain.AccessAllow, CIDR: "10.0.0.0/8"},
			{Action: domain.AccessAllow, CIDR: "10.0.0.0/8"},
		},
	}
	err := s.AccessLists().Create(ctx, l)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("create = %v, want ErrConflict", err)
	}
}

// TestAccessListInUseCannotBeDeleted drives hosts.access_list_id with SQL
// rather than through the host repository, because the column is written by
// the host aggregate and this test is about the constraint behind it.
func TestAccessListInUseCannotBeDeleted(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	l := sampleAccessList("office")
	if err := s.AccessLists().Create(ctx, l); err != nil {
		t.Fatalf("create list: %v", err)
	}
	h := sampleHost("api", "api.example.com")
	if err := s.Hosts().Create(ctx, h); err != nil {
		t.Fatalf("create host: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE hosts SET access_list_id = ? WHERE id = ?`, l.ID, h.ID); err != nil {
		t.Fatalf("attach list to host: %v", err)
	}

	n, err := s.AccessLists().HostsUsing(ctx, l.ID)
	if err != nil || n != 1 {
		t.Fatalf("hosts using = %d, %v; want 1, nil", n, err)
	}
	if err := s.AccessLists().Delete(ctx, l.ID); !errors.Is(err, domain.ErrInUse) {
		t.Fatalf("delete = %v, want ErrInUse", err)
	}

	// Once nothing points at it, the same delete succeeds.
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE hosts SET access_list_id = NULL WHERE id = ?`, h.ID); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if err := s.AccessLists().Delete(ctx, l.ID); err != nil {
		t.Fatalf("delete after detaching: %v", err)
	}
}

func TestAccessListGetMissing(t *testing.T) {
	_, err := open(t).AccessLists().Get(context.Background(), 404)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("get = %v, want ErrNotFound", err)
	}
}

func TestAccessListDelete(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	l := sampleAccessList("office")
	if err := s.AccessLists().Create(ctx, l); err != nil {
		t.Fatalf("create: %v", err)
	}

	n, err := s.AccessLists().HostsUsing(ctx, l.ID)
	if err != nil || n != 0 {
		t.Fatalf("hosts using a fresh list = %d, %v; want 0, nil", n, err)
	}

	if err := s.AccessLists().Delete(ctx, l.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.AccessLists().Delete(ctx, l.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}
