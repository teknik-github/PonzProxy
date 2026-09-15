package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

func logEntry(hostID int64, at time.Time, path string, status int) domain.AccessLogEntry {
	return domain.AccessLogEntry{
		Timestamp: at, HostID: hostID, Method: "GET", Path: path,
		Status: status, DurationMS: 12, BytesOut: 1024,
		ClientIP: "203.0.113.9", Upstream: "http://10.0.0.1:8080",
		UserAgent: "curl/8",
	}
}

func TestAccessLogWriteAndQuery(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	repo := s.AccessLog()

	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	batch := []domain.AccessLogEntry{
		logEntry(1, base, "/one", 200),
		logEntry(1, base.Add(time.Minute), "/two", 404),
		logEntry(2, base.Add(2*time.Minute), "/three", 503),
	}
	if err := repo.Write(ctx, batch); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, total, err := repo.Query(ctx, domain.AccessLogQuery{From: base.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if total != 3 || len(got) != 3 {
		t.Fatalf("got %d entries (total %d), want 3", len(got), total)
	}
	// Newest first is what an operator wants without asking.
	if got[0].Path != "/three" {
		t.Errorf("first entry is %q, want the newest", got[0].Path)
	}
	if got[0].Upstream != "http://10.0.0.1:8080" || got[0].UserAgent != "curl/8" {
		t.Errorf("fields did not round trip: %+v", got[0])
	}
}

func TestAccessLogFilters(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	repo := s.AccessLog()

	base := time.Now().Add(-time.Hour)
	failed := logEntry(1, base.Add(3*time.Minute), "/boom", 502)
	failed.Error = "connection refused"

	if err := repo.Write(ctx, []domain.AccessLogEntry{
		logEntry(1, base, "/alpha", 200),
		logEntry(1, base.Add(time.Minute), "/beta", 404),
		logEntry(2, base.Add(2*time.Minute), "/alpha", 500),
		failed,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	from := base.Add(-time.Minute)
	cases := []struct {
		name  string
		query domain.AccessLogQuery
		want  int
	}{
		{"by host", domain.AccessLogQuery{From: from, HostID: 1}, 3},
		{"by status class", domain.AccessLogQuery{From: from, StatusClass: 5}, 2},
		{"by 4xx", domain.AccessLogQuery{From: from, StatusClass: 4}, 1},
		{"failed only", domain.AccessLogQuery{From: from, FailedOnly: true}, 1},
		{"search path", domain.AccessLogQuery{From: from, Search: "alpha"}, 2},
		{"search client", domain.AccessLogQuery{From: from, Search: "203.0.113"}, 4},
		{"search miss", domain.AccessLogQuery{From: from, Search: "nothing"}, 0},
		{"host and status", domain.AccessLogQuery{From: from, HostID: 1, StatusClass: 5}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, total, err := repo.Query(ctx, tc.query)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if total != tc.want {
				t.Errorf("total = %d, want %d", total, tc.want)
			}
		})
	}
}

// TestAccessLogSearchTreatsWildcardsLiterally guards the LIKE escaping: without
// an ESCAPE clause a search for "100%" matches every row.
func TestAccessLogSearchTreatsWildcardsLiterally(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	repo := s.AccessLog()

	base := time.Now().Add(-time.Hour)
	if err := repo.Write(ctx, []domain.AccessLogEntry{
		logEntry(1, base, "/discount/100%off", 200),
		logEntry(1, base.Add(time.Minute), "/plain", 200),
		logEntry(1, base.Add(2*time.Minute), "/under_score", 200),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	from := base.Add(-time.Minute)
	if _, total, err := repo.Query(ctx, domain.AccessLogQuery{From: from, Search: "100%"}); err != nil {
		t.Fatalf("query: %v", err)
	} else if total != 1 {
		t.Errorf("searching for %q matched %d rows, want 1", "100%", total)
	}
	// "_" is LIKE's single-character wildcard and would otherwise match
	// "/plain" is not relevant, but "under_score" vs "underXscore" is.
	if _, total, err := repo.Query(ctx, domain.AccessLogQuery{From: from, Search: "under_score"}); err != nil {
		t.Fatalf("query: %v", err)
	} else if total != 1 {
		t.Errorf("searching for an underscore matched %d rows, want 1", total)
	}
}

func TestAccessLogPagination(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	repo := s.AccessLog()

	base := time.Now().Add(-time.Hour)
	batch := make([]domain.AccessLogEntry, 0, 25)
	for i := range 25 {
		batch = append(batch, logEntry(1, base.Add(time.Duration(i)*time.Second),
			fmt.Sprintf("/p%02d", i), 200))
	}
	if err := repo.Write(ctx, batch); err != nil {
		t.Fatalf("write: %v", err)
	}

	from := base.Add(-time.Minute)
	first, total, err := repo.Query(ctx, domain.AccessLogQuery{From: from, Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if total != 25 {
		t.Errorf("total = %d, want 25 — the count must ignore the page size", total)
	}
	if len(first) != 10 {
		t.Fatalf("page holds %d, want 10", len(first))
	}

	second, _, err := repo.Query(ctx, domain.AccessLogQuery{From: from, Limit: 10, Offset: 10})
	if err != nil {
		t.Fatalf("query page 2: %v", err)
	}
	if second[0].Path == first[0].Path {
		t.Error("the second page repeats the first")
	}
}

func TestAccessLogPruneByAgeAndRowCap(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	repo := s.AccessLog()

	now := time.Now()
	batch := []domain.AccessLogEntry{
		logEntry(1, now.Add(-48*time.Hour), "/old", 200),
		logEntry(1, now.Add(-1*time.Hour), "/recent", 200),
		logEntry(1, now.Add(-2*time.Hour), "/recent2", 200),
	}
	if err := repo.Write(ctx, batch); err != nil {
		t.Fatalf("write: %v", err)
	}

	// By age.
	if n, err := repo.Prune(ctx, now.Add(-24*time.Hour), 0); err != nil {
		t.Fatalf("prune by age: %v", err)
	} else if n != 1 {
		t.Errorf("age prune removed %d rows, want 1", n)
	}

	// By row cap: age alone cannot bound the table, so the cap is what makes
	// the worst case predictable.
	if n, err := repo.Prune(ctx, now.Add(-24*time.Hour), 1); err != nil {
		t.Fatalf("prune by cap: %v", err)
	} else if n != 1 {
		t.Errorf("cap prune removed %d rows, want 1", n)
	}

	remaining, total, err := repo.Query(ctx, domain.AccessLogQuery{From: now.Add(-72 * time.Hour)})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if total != 1 {
		t.Fatalf("%d rows survived, want 1", total)
	}
	// The newest is the one that should be kept.
	if remaining[0].Path != "/recent" {
		t.Errorf("kept %q, want the newest row", remaining[0].Path)
	}
}

func TestAccessLogQueryIsBounded(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	// A caller asking for an absurd page must be clamped, not obeyed.
	q := domain.AccessLogQuery{Limit: 100_000}
	q.Normalize()
	if q.Limit != domain.DefaultAccessLogPage {
		t.Errorf("limit = %d, want it clamped to %d", q.Limit, domain.DefaultAccessLogPage)
	}
	if _, _, err := s.AccessLog().Query(ctx, domain.AccessLogQuery{Limit: 100_000}); err != nil {
		t.Fatalf("query: %v", err)
	}
}
