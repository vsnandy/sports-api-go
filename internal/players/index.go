// Package players maintains the Sleeper player index used to normalize player IDs.
// The dump is loaded lazily and refreshed daily: memory, then S3, then Sleeper.
package players

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/vsnandy/sports-api-go/internal/domain"
)

const (
	storeKey = "players/nfl.json"
	maxAge   = 24 * time.Hour
)

type Source interface {
	Players(ctx context.Context) ([]domain.Player, error)
}

// Store persists the slimmed dump. Get returns domain.ErrNotFound when the key is absent.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, time.Time, error)
	Put(ctx context.Context, key string, data []byte) error
}

// NoStore is a Store that never holds anything, for local runs without a bucket.
type NoStore struct{}

func (NoStore) Get(context.Context, string) ([]byte, time.Time, error) {
	return nil, time.Time{}, domain.ErrNotFound
}
func (NoStore) Put(context.Context, string, []byte) error { return nil }

type slimPlayer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Pos    string `json:"pos"`
	Team   string `json:"team"`
	ESPNID string `json:"espn_id,omitempty"`
}

type snapshot struct {
	loadedAt time.Time
	byID     map[string]domain.Player
	byESPN   map[string]string // espn id -> sleeper id
}

type Index struct {
	src   Source
	store Store
	now   func() time.Time

	mu   sync.Mutex
	snap *snapshot
}

func New(src Source, store Store, now func() time.Time) *Index {
	return &Index{src: src, store: store, now: now}
}

func (ix *Index) Get(ctx context.Context, id string) (domain.Player, bool, error) {
	s, err := ix.snapshot(ctx)
	if err != nil {
		return domain.Player{}, false, err
	}
	p, ok := s.byID[id]
	return p, ok, nil
}

// Resolve maps a provider's player reference to a Sleeper player. ESPN team
// defenses are matched by NFL team, since Sleeper keys defenses by team abbreviation.
func (ix *Index) Resolve(ctx context.Context, ref domain.PlayerRef) (domain.Player, bool, error) {
	s, err := ix.snapshot(ctx)
	if err != nil {
		return domain.Player{}, false, err
	}
	switch ref.Platform {
	case domain.PlatformSleeper:
		p, ok := s.byID[ref.ID]
		return p, ok, nil
	case domain.PlatformESPN:
		if ref.Position == "DEF" {
			p, ok := s.byID[ref.NFLTeam]
			if !ok {
				return domain.Player{}, false, nil
			}
			p.PlatformIDs = maps.Clone(p.PlatformIDs)
			p.PlatformIDs["espn"] = ref.ID
			return p, true, nil
		}
		id, ok := s.byESPN[ref.ID]
		if !ok {
			return domain.Player{}, false, nil
		}
		return s.byID[id], true, nil
	}
	return domain.Player{}, false, nil
}

// snapshot returns the current index, loading or refreshing it when older than
// maxAge. A failed refresh keeps serving the previous snapshot.
func (ix *Index) snapshot(ctx context.Context) (*snapshot, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.snap != nil && ix.now().Sub(ix.snap.loadedAt) < maxAge {
		return ix.snap, nil
	}
	s, err := ix.load(ctx)
	if err != nil {
		if ix.snap != nil {
			slog.WarnContext(ctx, "players refresh failed; serving stale index", "err", err)
			return ix.snap, nil
		}
		return nil, err
	}
	ix.snap = s
	return s, nil
}

func (ix *Index) load(ctx context.Context) (*snapshot, error) {
	data, mod, err := ix.store.Get(ctx, storeKey)
	switch {
	case err == nil && ix.now().Sub(mod) < maxAge:
		var slim []slimPlayer
		if err := json.Unmarshal(data, &slim); err == nil {
			return build(fromSlim(slim), mod), nil
		}
		slog.WarnContext(ctx, "players cache is corrupt; refetching")
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		slog.WarnContext(ctx, "players cache read failed; refetching", "err", err)
	}

	ps, err := ix.src.Players(ctx)
	if err != nil {
		return nil, err
	}
	if data, err := json.Marshal(toSlim(ps)); err == nil {
		if err := ix.store.Put(ctx, storeKey, data); err != nil {
			slog.WarnContext(ctx, "players cache write failed", "err", err)
		}
	}
	return build(ps, ix.now()), nil
}

func build(ps []domain.Player, loadedAt time.Time) *snapshot {
	s := &snapshot{loadedAt: loadedAt, byID: make(map[string]domain.Player, len(ps)), byESPN: map[string]string{}}
	for _, p := range ps {
		if p.ID == nil {
			continue
		}
		s.byID[*p.ID] = p
		if e := p.PlatformIDs["espn"]; e != "" {
			s.byESPN[e] = *p.ID
		}
	}
	return s
}

func toSlim(ps []domain.Player) []slimPlayer {
	out := make([]slimPlayer, 0, len(ps))
	for _, p := range ps {
		if p.ID != nil {
			out = append(out, slimPlayer{ID: *p.ID, Name: p.Name, Pos: p.Position, Team: p.NFLTeam, ESPNID: p.PlatformIDs["espn"]})
		}
	}
	return out
}

func fromSlim(slim []slimPlayer) []domain.Player {
	out := make([]domain.Player, 0, len(slim))
	for _, s := range slim {
		ids := map[string]string{"sleeper": s.ID}
		if s.ESPNID != "" {
			ids["espn"] = s.ESPNID
		}
		out = append(out, domain.Player{ID: &s.ID, Name: s.Name, Position: s.Pos, NFLTeam: s.Team, PlatformIDs: ids})
	}
	return out
}
