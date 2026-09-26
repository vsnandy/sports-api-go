// Package cache is a small in-memory TTL cache. It lives only as long as a warm
// Lambda container.
package cache

import (
	"context"
	"sync"
	"time"
)

// sweepThreshold is the entry count above which Set drops expired entries.
const sweepThreshold = 1000

type entry[V any] struct {
	v   V
	exp time.Time
}

type Cache[V any] struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]entry[V]
}

func New[V any](now func() time.Time) *Cache[V] {
	if now == nil {
		now = time.Now
	}
	return &Cache[V]{now: now, m: map[string]entry[V]{}}
}

func (c *Cache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || !c.now().Before(e.exp) {
		var zero V
		return zero, false
	}
	return e.v, true
}

func (c *Cache[V]) Set(key string, v V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.m) >= sweepThreshold {
		for k, e := range c.m {
			if !now.Before(e.exp) {
				delete(c.m, k)
			}
		}
	}
	c.m[key] = entry[V]{v: v, exp: now.Add(ttl)}
}

// Len reports the number of stored entries, including expired ones not yet swept.
func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// GetOrLoad returns the cached value for key, or calls load and caches its result.
// Errors from load are returned and never cached.
func (c *Cache[V]) GetOrLoad(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (V, error)) (V, error) {
	if v, ok := c.Get(key); ok {
		return v, nil
	}
	v, err := load(ctx)
	if err != nil {
		var zero V
		return zero, err
	}
	c.Set(key, v, ttl)
	return v, nil
}
