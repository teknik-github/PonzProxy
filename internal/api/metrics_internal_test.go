package api

import (
	"math"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

// TestPartialBucketReportsItsRealRate guards the artefact that made the
// history chart look like a cliff at its right-hand edge: the newest bucket is
// still filling, so its raw count is a fraction of what it will hold.
func TestPartialBucketReportsItsRealRate(t *testing.T) {
	// A minute bucket that started 20 seconds ago with 100 requests in it.
	// The true rate is 5/s; dividing by the nominal 60s would say 1.67/s.
	now := time.Now().UTC().Truncate(time.Minute).Add(20 * time.Second)
	bucket := now.Truncate(time.Minute)

	points := toSeries([]domain.Sample{
		{Timestamp: bucket.Add(-time.Minute), Requests: 300},
		{Timestamp: bucket, Requests: 100},
	}, domain.ResolutionMinute, now, 10*time.Second)

	if len(points) != 2 {
		t.Fatalf("got %d points, want 2", len(points))
	}
	if points[0].Partial {
		t.Error("a finished bucket was marked partial")
	}
	if got := points[0].RequestsPerSec; got != 5 {
		t.Errorf("finished bucket rate = %v, want 5", got)
	}

	if !points[1].Partial {
		t.Fatal("the still-filling bucket was not marked partial")
	}
	// 100 requests over the 20 seconds that have elapsed is 5/s, the same
	// as its neighbour. Dividing by the nominal 60 would say 1.67/s, which
	// is the cliff this whole change exists to remove.
	if got := points[1].RequestsPerSec; got < 4.9 || got > 5.1 {
		t.Errorf("partial bucket rate = %v, want about 5 — the same traffic, not a third of it", got)
	}
	// The count itself stays honest: it really is only 100 so far, which is
	// exactly why the chart plots a rate and leaves this bucket out.
	if points[1].Requests != 100 {
		t.Errorf("partial bucket count = %d, want the raw 100", points[1].Requests)
	}
}

// TestPartialSpanStaysInsideItsBucket pins the bounds of the estimate. It is
// deliberately approximate — see partialSpan — which is why the chart marks
// the bucket rather than drawing it.
func TestPartialSpanStaysInsideItsBucket(t *testing.T) {
	start := time.Date(2026, 9, 16, 15, 26, 0, 0, time.UTC)
	const width = time.Minute
	const flush = 10 * time.Second

	cases := []struct {
		name    string
		through time.Time
		want    time.Duration
	}{
		{"30s in", start.Add(30 * time.Second), 30 * time.Second},
		{"never more than the bucket", start.Add(90 * time.Second), width},
		{"never less than one flush", start.Add(time.Second), flush},
		{"before it started", start.Add(-time.Second), flush},
	}
	for _, c := range cases {
		if got := partialSpan(start, c.through, width, flush); got != c.want {
			t.Errorf("%s: span = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestBucketAtTheVeryStartOfItsWindowDoesNotDivideByZero covers the first
// instant of a bucket, where the elapsed span is zero.
func TestBucketAtTheVeryStartOfItsWindowDoesNotDivideByZero(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	points := toSeries([]domain.Sample{{Timestamp: now, Requests: 0}},
		domain.ResolutionMinute, now, 10*time.Second)

	if len(points) != 1 {
		t.Fatalf("got %d points, want 1", len(points))
	}
	if math.IsInf(points[0].RequestsPerSec, 0) || math.IsNaN(points[0].RequestsPerSec) {
		t.Errorf("rate = %v, which would render as a blank chart", points[0].RequestsPerSec)
	}
}
