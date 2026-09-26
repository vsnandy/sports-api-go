package cache

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestSetGetExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[string](func() time.Time { return now })
	if _, ok := c.Get("k"); ok {
		t.Fatal("empty cache should miss")
	}
	c.Set("k", "v", time.Minute)
	if v, ok := c.Get("k"); !ok || v != "v" {
		t.Fatalf("Get = %q, %v; want v, true", v, ok)
	}
	now = now.Add(59 * time.Second)
	if _, ok := c.Get("k"); !ok {
		t.Fatal("should still hit before TTL")
	}
	now = now.Add(time.Second)
	if _, ok := c.Get("k"); ok {
		t.Fatal("should miss at TTL")
	}
}

func TestGetOrLoadCachesValuesNotErrors(t *testing.T) {
	c := New[int](nil)
	calls := 0
	boom := errors.New("boom")
	failing := func(context.Context) (int, error) { calls++; return 0, boom }
	if _, err := c.GetOrLoad(context.Background(), "k", time.Minute, failing); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	ok := func(context.Context) (int, error) { calls++; return 7, nil }
	for range 2 {
		v, err := c.GetOrLoad(context.Background(), "k", time.Minute, ok)
		if err != nil || v != 7 {
			t.Fatalf("GetOrLoad = %d, %v", v, err)
		}
	}
	if calls != 2 {
		t.Fatalf("loader calls = %d, want 2 (one failure, one success)", calls)
	}
}

func TestSweepDropsExpiredEntries(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[int](func() time.Time { return now })
	for i := range sweepThreshold {
		c.Set(fmt.Sprint(i), i, time.Second)
	}
	now = now.Add(2 * time.Second)
	c.Set("fresh", 1, time.Minute)
	if n := c.Len(); n != 1 {
		t.Fatalf("Len after sweep = %d, want 1", n)
	}
}
