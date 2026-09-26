package players

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

func p(id, name, pos, team, espn string) domain.Player {
	ids := map[string]string{"sleeper": id}
	if espn != "" {
		ids["espn"] = espn
	}
	return domain.Player{ID: &id, Name: name, Position: pos, NFLTeam: team, PlatformIDs: ids}
}

var sample = []domain.Player{
	p("4046", "Patrick Mahomes", "QB", "KC", "3139477"),
	p("6794", "Justin Jefferson", "WR", "MIN", "4262921"),
	p("KC", "Kansas City Chiefs", "DEF", "KC", ""),
}

type fakeSource struct {
	calls atomic.Int32
	mu    sync.Mutex
	err   error
}

func (f *fakeSource) Players(context.Context) ([]domain.Player, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return sample, nil
}

type fakeStore struct {
	now    func() time.Time
	data   []byte
	mod    time.Time
	getErr error
	puts   int
	key    string
}

func (s *fakeStore) Get(_ context.Context, key string) ([]byte, time.Time, error) {
	s.key = key
	if s.getErr != nil {
		return nil, time.Time{}, s.getErr
	}
	if s.data == nil {
		return nil, time.Time{}, domain.ErrNotFound
	}
	return s.data, s.mod, nil
}

func (s *fakeStore) Put(_ context.Context, key string, data []byte) error {
	s.key, s.data, s.mod = key, data, s.now()
	s.puts++
	return nil
}

type harness struct {
	now   time.Time
	src   *fakeSource
	store *fakeStore
	ix    *Index
}

func newHarness() *harness {
	h := &harness{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC), src: &fakeSource{}}
	clock := func() time.Time { return h.now }
	h.store = &fakeStore{now: clock}
	h.ix = New(h.src, h.store, clock)
	return h
}

func (h *harness) seedStore(t *testing.T, age time.Duration) {
	t.Helper()
	data, err := json.Marshal(toSlim(sample))
	if err != nil {
		t.Fatal(err)
	}
	h.store.data, h.store.mod = data, h.now.Add(-age)
}

func mustGet(t *testing.T, ix *Index, id string) domain.Player {
	t.Helper()
	pl, ok, err := ix.Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("Get(%s) = %v, %v", id, ok, err)
	}
	return pl
}

func TestLoadsFromSourceWhenStoreEmpty(t *testing.T) {
	h := newHarness()
	if got := mustGet(t, h.ix, "4046"); got.Name != "Patrick Mahomes" {
		t.Fatalf("got %+v", got)
	}
	if h.src.calls.Load() != 1 || h.store.puts != 1 || h.store.key != "players/nfl.json" {
		t.Fatalf("calls=%d puts=%d key=%q", h.src.calls.Load(), h.store.puts, h.store.key)
	}
	if !strings.Contains(string(h.store.data), "3139477") {
		t.Fatal("stored slim dump should include espn ids")
	}
}

func TestUsesFreshStore(t *testing.T) {
	h := newHarness()
	h.seedStore(t, time.Hour)
	mustGet(t, h.ix, "6794")
	if h.src.calls.Load() != 0 {
		t.Fatal("fresh S3 copy should avoid a Sleeper fetch")
	}
}

func TestStaleStoreRefetches(t *testing.T) {
	h := newHarness()
	h.seedStore(t, 25*time.Hour)
	mustGet(t, h.ix, "6794")
	if h.src.calls.Load() != 1 {
		t.Fatal("stale S3 copy should trigger a Sleeper fetch")
	}
}

func TestStoreReadErrorFallsBack(t *testing.T) {
	h := newHarness()
	h.store.getErr = errors.New("AccessDenied")
	mustGet(t, h.ix, "4046")
	if h.src.calls.Load() != 1 {
		t.Fatal("store read errors should fall back to Sleeper")
	}
}

func TestCorruptStoreFallsBack(t *testing.T) {
	h := newHarness()
	h.store.data, h.store.mod = []byte("not json"), h.now
	mustGet(t, h.ix, "4046")
	if h.src.calls.Load() != 1 {
		t.Fatal("corrupt store data should fall back to Sleeper")
	}
}

func TestMemoryReuseAndDailyRefresh(t *testing.T) {
	h := newHarness()
	mustGet(t, h.ix, "4046")
	mustGet(t, h.ix, "6794")
	if h.src.calls.Load() != 1 {
		t.Fatal("second Get within 24h should use memory")
	}
	h.now = h.now.Add(25 * time.Hour)
	mustGet(t, h.ix, "4046")
	if h.src.calls.Load() != 2 {
		t.Fatalf("calls = %d, want refresh after 24h", h.src.calls.Load())
	}
}

func TestRefreshFailureServesStale(t *testing.T) {
	h := newHarness()
	mustGet(t, h.ix, "4046")
	h.now = h.now.Add(25 * time.Hour)
	h.src.err = errors.New("sleeper down")
	mustGet(t, h.ix, "4046")
}

func TestInitialFailureThenRetry(t *testing.T) {
	h := newHarness()
	h.src.err = errors.New("sleeper down")
	if _, _, err := h.ix.Get(context.Background(), "4046"); err == nil {
		t.Fatal("first load failure with nothing cached should error")
	}
	h.src.mu.Lock()
	h.src.err = nil
	h.src.mu.Unlock()
	mustGet(t, h.ix, "4046")
}

func TestConcurrentGetsLoadOnce(t *testing.T) {
	h := newHarness()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); h.ix.Get(context.Background(), "4046") }()
	}
	wg.Wait()
	if h.src.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", h.src.calls.Load())
	}
}

func TestResolve(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	tests := []struct {
		name   string
		ref    domain.PlayerRef
		wantID string
		ok     bool
	}{
		{"sleeper id", domain.PlayerRef{Platform: domain.PlatformSleeper, ID: "4046"}, "4046", true},
		{"espn id", domain.PlayerRef{Platform: domain.PlatformESPN, ID: "4262921"}, "6794", true},
		{"espn D/ST by team", domain.PlayerRef{Platform: domain.PlatformESPN, ID: "-16012", Position: "DEF", NFLTeam: "KC"}, "KC", true},
		{"unmapped espn", domain.PlayerRef{Platform: domain.PlatformESPN, ID: "9999999"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := h.ix.Resolve(ctx, tt.ref)
			if err != nil || ok != tt.ok || (ok && *got.ID != tt.wantID) {
				t.Fatalf("Resolve = %+v, %v, %v", got, ok, err)
			}
		})
	}
	dst, _, _ := h.ix.Resolve(ctx, domain.PlayerRef{Platform: domain.PlatformESPN, ID: "-16012", Position: "DEF", NFLTeam: "KC"})
	if dst.PlatformIDs["espn"] != "-16012" {
		t.Errorf("resolved D/ST should carry its espn id: %v", dst.PlatformIDs)
	}
	if kc := mustGet(t, h.ix, "KC"); kc.PlatformIDs["espn"] != "" {
		t.Errorf("Resolve must not mutate the index entry: %v", kc.PlatformIDs)
	}
}
