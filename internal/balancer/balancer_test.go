package balancer

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/ponzproxy/ponzproxy/internal/domain"
)

func upstreams(weights ...int) []domain.Upstream {
	out := make([]domain.Upstream, len(weights))
	for i, w := range weights {
		out[i] = domain.Upstream{
			ID:      int64(i + 1),
			Scheme:  "http",
			Address: fmt.Sprintf("10.0.0.%d:8080", i+1),
			Weight:  w,
			Enabled: true,
		}
	}
	return out
}

// pickSequence records which backend index served each of n requests.
func pickSequence(t *testing.T, p *Pool, clientIP string, n int) []int {
	t.Helper()
	index := make(map[*Backend]int, len(p.Backends()))
	for i, b := range p.Backends() {
		index[b] = i
	}
	seq := make([]int, 0, n)
	for range n {
		b, err := p.Pick(clientIP, nil)
		if err != nil {
			t.Fatalf("pick: %v", err)
		}
		seq = append(seq, index[b])
	}
	return seq
}

func TestRoundRobinCyclesInOrder(t *testing.T) {
	p := NewPool(1, domain.RoundRobin, upstreams(1, 1, 1), nil)

	got := pickSequence(t, p, "203.0.113.5", 7)
	want := []int{0, 1, 2, 0, 1, 2, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequence = %v, want %v", got, want)
		}
	}
}

func TestRoundRobinSkipsUnhealthy(t *testing.T) {
	p := NewPool(1, domain.RoundRobin, upstreams(1, 1, 1), nil)
	p.Backends()[1].SetHealthy(false)

	for _, idx := range pickSequence(t, p, "203.0.113.5", 20) {
		if idx == 1 {
			t.Fatal("picked an unhealthy backend")
		}
	}
}

func TestWeightedRoundRobinIsSmoothAndProportional(t *testing.T) {
	p := NewPool(1, domain.WeightedRoundRobin, upstreams(5, 1, 1), nil)

	// One full cycle of smooth WRR over weights 5/1/1 is seven requests and
	// must never place the heavy backend more than twice in a row.
	seq := pickSequence(t, p, "", 7)
	counts := map[int]int{}
	run := 0
	for i, idx := range seq {
		counts[idx]++
		if i > 0 && idx == seq[i-1] {
			run++
			if run >= 2 {
				t.Fatalf("backend %d served 3 consecutive requests: %v", idx, seq)
			}
		} else {
			run = 0
		}
	}
	if counts[0] != 5 || counts[1] != 1 || counts[2] != 1 {
		t.Fatalf("distribution = %v, want 5/1/1 over one cycle (%v)", counts, seq)
	}

	// The cycle must repeat exactly, which is what makes WRR predictable.
	if next := pickSequence(t, p, "", 7); fmt.Sprint(next) != fmt.Sprint(seq) {
		t.Errorf("second cycle = %v, want a repeat of %v", next, seq)
	}
}

func TestLeastConnectionsPicksIdlest(t *testing.T) {
	p := NewPool(1, domain.LeastConnections, upstreams(1, 1, 1), nil)
	b := p.Backends()

	b[0].Acquire()
	b[0].Acquire()
	b[1].Acquire()
	// b[2] is idle and must win.

	got, err := p.Pick("", nil)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got != b[2] {
		t.Fatalf("picked %s, want the idle backend", got.Key())
	}

	// With the counts level, the heavier backend breaks the tie.
	p2 := NewPool(1, domain.LeastConnections, upstreams(1, 5), nil)
	if got := mustPick(t, p2); got != p2.Backends()[1] {
		t.Errorf("tie broke to %s, want the heavier backend", got.Key())
	}
}

func TestLeastConnectionsTracksReleases(t *testing.T) {
	p := NewPool(1, domain.LeastConnections, upstreams(1, 1), nil)
	b := p.Backends()

	b[0].Acquire()
	if got := mustPick(t, p); got != b[1] {
		t.Fatal("expected the idle backend while b0 is busy")
	}
	b[0].Release(5 * time.Millisecond)
	if b[0].ActiveConns() != 0 {
		t.Fatalf("active conns = %d after release, want 0", b[0].ActiveConns())
	}
}

func TestIPHashIsStickyPerClient(t *testing.T) {
	p := NewPool(1, domain.IPHash, upstreams(1, 1, 1), nil)

	for _, ip := range []string{"203.0.113.5", "198.51.100.9", "192.0.2.1"} {
		first := mustPickIP(t, p, ip)
		for range 50 {
			if got := mustPickIP(t, p, ip); got != first {
				t.Fatalf("client %s moved from %s to %s", ip, first.Key(), got.Key())
			}
		}
	}
}

func TestIPHashSpreadsClientsAcrossBackends(t *testing.T) {
	p := NewPool(1, domain.IPHash, upstreams(1, 1, 1), nil)

	counts := map[string]int{}
	const clients = 3000
	for i := range clients {
		counts[mustPickIP(t, p, fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256)).Key()]++
	}
	if len(counts) != 3 {
		t.Fatalf("used %d backends, want 3", len(counts))
	}
	// Rendezvous hashing is probabilistic; allow a generous band and only
	// fail on a genuinely broken distribution.
	for key, n := range counts {
		if n < clients/6 {
			t.Errorf("backend %s got %d of %d clients, which is too few", key, n, clients)
		}
	}
}

// TestIPHashOnlyMovesTheAffectedClients is the reason rendezvous hashing is
// used instead of modulo: losing one backend must not reshuffle the clients
// pinned to the survivors.
func TestIPHashOnlyMovesTheAffectedClients(t *testing.T) {
	p := NewPool(1, domain.IPHash, upstreams(1, 1, 1, 1), nil)

	const clients = 2000
	ips := make([]string, clients)
	before := make([]*Backend, clients)
	for i := range clients {
		ips[i] = fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		before[i] = mustPickIP(t, p, ips[i])
	}

	downed := p.Backends()[2]
	downed.SetHealthy(false)

	moved, shouldHaveMoved := 0, 0
	for i, ip := range ips {
		if before[i] == downed {
			shouldHaveMoved++
			continue
		}
		if mustPickIP(t, p, ip) != before[i] {
			moved++
		}
	}
	if moved != 0 {
		t.Errorf("%d of %d unaffected clients were remapped; only the %d pinned to the "+
			"downed backend should move", moved, clients-shouldHaveMoved, shouldHaveMoved)
	}
}

func TestIPHashHonoursWeight(t *testing.T) {
	p := NewPool(1, domain.IPHash, upstreams(3, 1), nil)

	counts := map[string]int{}
	const clients = 20000
	for i := range clients {
		counts[mustPickIP(t, p, fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256)).Key()]++
	}
	heavy := counts[p.Backends()[0].Key()]
	light := counts[p.Backends()[1].Key()]
	ratio := float64(heavy) / float64(light)
	if math.Abs(ratio-3) > 0.45 {
		t.Errorf("weight 3 vs 1 produced a %.2f:1 split (%d vs %d), want about 3:1",
			ratio, heavy, light)
	}
}

func TestPickErrorsAreDistinct(t *testing.T) {
	empty := NewPool(1, domain.RoundRobin, nil, nil)
	if _, err := empty.Pick("", nil); !errors.Is(err, ErrNoBackends) {
		t.Errorf("empty pool error = %v, want ErrNoBackends", err)
	}

	us := upstreams(1, 1)
	us[0].Enabled, us[1].Enabled = false, false
	disabled := NewPool(1, domain.RoundRobin, us, nil)
	if _, err := disabled.Pick("", nil); !errors.Is(err, ErrNoBackends) {
		t.Errorf("all-disabled error = %v, want ErrNoBackends", err)
	}

	unhealthy := NewPool(1, domain.RoundRobin, upstreams(1, 1), nil)
	for _, b := range unhealthy.Backends() {
		b.SetHealthy(false)
	}
	if _, err := unhealthy.Pick("", nil); !errors.Is(err, ErrNoHealthyBackend) {
		t.Errorf("all-unhealthy error = %v, want ErrNoHealthyBackend", err)
	}
}

func TestMaxConnsTakesBackendOutOfRotation(t *testing.T) {
	us := upstreams(1, 1)
	us[0].MaxConns = 1
	p := NewPool(1, domain.RoundRobin, us, nil)

	p.Backends()[0].Acquire() // now at its limit
	for range 10 {
		if mustPick(t, p) == p.Backends()[0] {
			t.Fatal("picked a backend that is at MaxConns")
		}
	}

	if _, err := p.Pick("", p.Backends()[1:]); !errors.Is(err, ErrNoHealthyBackend) {
		t.Error("excluding the only other backend should leave nothing available")
	}
}

func TestExcludeSkipsAlreadyTriedBackends(t *testing.T) {
	p := NewPool(1, domain.RoundRobin, upstreams(1, 1, 1), nil)
	tried := []*Backend{p.Backends()[0], p.Backends()[2]}

	for range 10 {
		b, err := p.Pick("", tried)
		if err != nil {
			t.Fatalf("pick: %v", err)
		}
		if b != p.Backends()[1] {
			t.Fatalf("picked %s, want the only untried backend", b.Key())
		}
	}
}

func TestPoolInheritsStateAcrossReload(t *testing.T) {
	old := NewPool(1, domain.RoundRobin, upstreams(1, 1), nil)
	old.Backends()[0].SetHealthy(false)
	old.Backends()[0].RecordError(errors.New("connection refused"))
	old.Backends()[1].Release(20 * time.Millisecond)

	// Reload with a different weight and a different algorithm.
	us := upstreams(9, 9)
	fresh := NewPool(1, domain.LeastConnections, us, old)

	if fresh.Backends()[0].Healthy() {
		t.Error("health state was not carried across the reload")
	}
	if got := fresh.Backends()[0].LastError(); got != "connection refused" {
		t.Errorf("last error = %q, want it preserved", got)
	}
	if snap := fresh.Backends()[1].Snapshot(); snap.TotalRequests != 1 {
		t.Errorf("total requests = %d, want the counter preserved", snap.TotalRequests)
	}
	if fresh.Backends()[0].Upstream.Weight != 9 {
		t.Error("new configuration was not applied")
	}

	// A backend that disappears from the config must not come back.
	shrunk := NewPool(1, domain.RoundRobin, upstreams(1), old)
	if len(shrunk.Backends()) != 1 {
		t.Errorf("got %d backends after shrinking, want 1", len(shrunk.Backends()))
	}
}

func TestHealthyCount(t *testing.T) {
	us := upstreams(1, 1, 1)
	us[2].Enabled = false
	p := NewPool(1, domain.RoundRobin, us, nil)
	p.Backends()[0].SetHealthy(false)

	up, total := p.HealthyCount()
	if up != 1 || total != 2 {
		t.Errorf("HealthyCount = %d/%d, want 1/2 (disabled backends excluded)", up, total)
	}
}

// TestSelectorsAreConcurrencySafe is meant to be run under -race: every
// selector is shared by all requests for its host.
func TestSelectorsAreConcurrencySafe(t *testing.T) {
	for _, alg := range domain.Algorithms() {
		t.Run(string(alg), func(t *testing.T) {
			p := NewPool(1, alg, upstreams(1, 2, 3), nil)

			var wg sync.WaitGroup
			for g := range 16 {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					for i := range 200 {
						b, err := p.Pick(fmt.Sprintf("10.0.0.%d", g), nil)
						if err != nil {
							t.Errorf("pick: %v", err)
							return
						}
						b.Acquire()
						if i%3 == 0 {
							b.SetHealthy(true)
						}
						b.Release(time.Millisecond)
					}
				}(g)
			}
			wg.Wait()
		})
	}
}

func mustPick(t *testing.T, p *Pool) *Backend {
	t.Helper()
	return mustPickIP(t, p, "")
}

func mustPickIP(t *testing.T, p *Pool, ip string) *Backend {
	t.Helper()
	b, err := p.Pick(ip, nil)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	return b
}

// --------------------------------------------------------------- passive ---

func passiveConfig() domain.PassiveHealth {
	return domain.PassiveHealth{Enabled: true, MaxFails: 3, EjectFor: 30 * time.Second}
}

func TestPassiveHealthEjectsAfterConsecutiveFailures(t *testing.T) {
	p := NewPool(1, domain.RoundRobin, upstreams(1, 1), nil)
	b := p.Backends()[0]
	cfg := passiveConfig()
	now := time.Now()

	// Below the threshold the backend keeps serving: one blip must not
	// take a backend out.
	for i := 1; i < cfg.MaxFails; i++ {
		if changed := b.RecordProxyResult(false, cfg, now); changed {
			t.Fatalf("ejected after only %d failures, want %d", i, cfg.MaxFails)
		}
		if !b.Available(now) {
			t.Fatalf("backend unavailable after %d of %d failures", i, cfg.MaxFails)
		}
	}

	if changed := b.RecordProxyResult(false, cfg, now); !changed {
		t.Fatal("reaching the failure threshold did not eject the backend")
	}
	if b.Available(now) {
		t.Fatal("an ejected backend is still available")
	}
	// The active probe never ran, sohealth state is untouched: the two
	// reasons a backend leaves rotation stay distinguishable.
	if !b.Healthy() {
		t.Error("passive ejection changed the active probe's health flag")
	}

	ejected, remaining := b.Ejected(now)
	if !ejected || remaining <= 0 {
		t.Errorf("Ejected = %v, %v; want an ejection with time left", ejected, remaining)
	}

	// Pick must skip it entirely.
	for range 10 {
		if got := mustPick(t, p); got == b {
			t.Fatal("Pick returned an ejected backend")
		}
	}
}

func TestEjectionLapsesAfterTheWindow(t *testing.T) {
	p := NewPool(1, domain.RoundRobin, upstreams(1), nil)
	b := p.Backends()[0]
	cfg := passiveConfig()
	now := time.Now()

	for range cfg.MaxFails {
		b.RecordProxyResult(false, cfg, now)
	}
	if b.Available(now) {
		t.Fatal("backend was not ejected")
	}

	// Still out just before the deadline, back in just after.
	if b.Available(now.Add(cfg.EjectFor - time.Second)) {
		t.Error("ejection ended early")
	}
	if !b.Available(now.Add(cfg.EjectFor + time.Second)) {
		t.Error("ejection never lapsed")
	}
}

func TestSuccessClearsEjectionAndStreak(t *testing.T) {
	p := NewPool(1, domain.RoundRobin, upstreams(1), nil)
	b := p.Backends()[0]
	cfg := passiveConfig()
	now := time.Now()

	// A success partway through resets the streak, so failures must be
	// consecutive to count.
	b.RecordProxyResult(false, cfg, now)
	b.RecordProxyResult(false, cfg, now)
	b.RecordProxyResult(true, cfg, now)
	b.RecordProxyResult(false, cfg, now)
	b.RecordProxyResult(false, cfg, now)
	if !b.Available(now) {
		t.Fatal("non-consecutive failures ejected the backend")
	}

	// Once ejected, a success inside the window releases it early.
	b.RecordProxyResult(false, cfg, now)
	if b.Available(now) {
		t.Fatal("backend was not ejected by consecutive failures")
	}
	if changed := b.RecordProxyResult(true, cfg, now); !changed {
		t.Error("a success inside the window did not report clearing the ejection")
	}
	if !b.Available(now) {
		t.Error("a success inside the window did not restore the backend")
	}
}

func TestPassiveHealthCanBeDisabled(t *testing.T) {
	p := NewPool(1, domain.RoundRobin, upstreams(1), nil)
	b := p.Backends()[0]
	cfg := domain.PassiveHealth{Enabled: false, MaxFails: 1, EjectFor: time.Minute}
	now := time.Now()

	for range 20 {
		if changed := b.RecordProxyResult(false, cfg, now); changed {
			t.Fatal("ejected a backend while passive health is disabled")
		}
	}
	if !b.Available(now) {
		t.Error("backend left rotation while passive health is disabled")
	}
}

func TestEjectionIsReportedOncePerTransition(t *testing.T) {
	b := NewBackend(upstreams(1)[0])
	cfg := passiveConfig()
	now := time.Now()

	transitions := 0
	for range cfg.MaxFails * 4 {
		if b.RecordProxyResult(false, cfg, now) {
			transitions++
		}
	}
	if transitions != 1 {
		t.Errorf("reported %d transitions during one outage, want 1", transitions)
	}
}

func TestClearEjectionRestoresImmediately(t *testing.T) {
	b := NewBackend(upstreams(1)[0])
	cfg := passiveConfig()
	now := time.Now()

	for range cfg.MaxFails {
		b.RecordProxyResult(false, cfg, now)
	}
	if b.Available(now) {
		t.Fatal("backend was not ejected")
	}

	// This is the path the active health checker uses when a probe passes.
	b.ClearEjection()
	if !b.Available(now) {
		t.Error("ClearEjection did not put the backend back in rotation")
	}
}

func TestEjectionSurvivesAReload(t *testing.T) {
	old := NewPool(1, domain.RoundRobin, upstreams(1, 1), nil)
	cfg := passiveConfig()
	now := time.Now()
	for range cfg.MaxFails {
		old.Backends()[0].RecordProxyResult(false, cfg, now)
	}

	// An unrelated edit must not hand traffic back to a backend that is
	// still refusing connections.
	fresh := NewPool(1, domain.LeastConnections, upstreams(4, 4), old)
	if fresh.Backends()[0].Available(now) {
		t.Error("a configuration reload cleared the ejection")
	}
	if !fresh.Backends()[1].Available(now) {
		t.Error("the healthy backend was ejected by the reload")
	}
}

func TestEjectedBackendCountsAsDown(t *testing.T) {
	p := NewPool(1, domain.RoundRobin, upstreams(1, 1), nil)
	cfg := passiveConfig()
	now := time.Now()
	for range cfg.MaxFails {
		p.Backends()[0].RecordProxyResult(false, cfg, now)
	}

	up, total := p.HealthyCount()
	if up != 1 || total != 2 {
		t.Errorf("HealthyCount = %d/%d, want 1/2", up, total)
	}
	snap := p.Backends()[0].Snapshot()
	if !snap.Ejected {
		t.Error("the snapshot does not report the ejection")
	}
	if snap.EjectedForSeconds <= 0 {
		t.Errorf("EjectedForSeconds = %d, want the remaining window", snap.EjectedForSeconds)
	}
	if !snap.Healthy {
		t.Error("the snapshot conflated ejection with an active-probe failure")
	}
}

func TestAllBackendsEjectedYieldsNoHealthyBackend(t *testing.T) {
	p := NewPool(1, domain.RoundRobin, upstreams(1, 1), nil)
	cfg := passiveConfig()
	now := time.Now()
	for _, b := range p.Backends() {
		for range cfg.MaxFails {
			b.RecordProxyResult(false, cfg, now)
		}
	}
	if _, err := p.Pick("", nil); !errors.Is(err, ErrNoHealthyBackend) {
		t.Errorf("Pick = %v, want ErrNoHealthyBackend", err)
	}
}

func TestPassiveHealthIsConcurrencySafe(t *testing.T) {
	p := NewPool(1, domain.RoundRobin, upstreams(1, 1, 1), nil)
	cfg := passiveConfig()

	var wg sync.WaitGroup
	for g := range 16 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 300 {
				b, err := p.Pick("10.0.0.1", nil)
				if err != nil {
					continue // every backend ejected at this instant
				}
				b.RecordProxyResult((g+i)%4 != 0, cfg, time.Now())
				b.Snapshot()
			}
		}(g)
	}
	wg.Wait()
}
