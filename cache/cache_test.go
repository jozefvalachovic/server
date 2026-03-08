package cache

import (
	"testing"
	"time"
)

func newTestStore(t *testing.T) *CacheStore {
	t.Helper()
	cs, err := NewCacheStore(CacheConfig{
		MaxSize:         100,
		DefaultTTL:      time.Minute,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cs.Stop)
	return cs
}

func TestNewCacheStore_InvalidConfig(t *testing.T) {
	_, err := NewCacheStore(CacheConfig{DefaultTTL: 0, CleanupInterval: time.Second})
	if err != ErrInvalidTTL {
		t.Fatalf("want ErrInvalidTTL, got %v", err)
	}

	_, err = NewCacheStore(CacheConfig{DefaultTTL: time.Second, CleanupInterval: 0})
	if err != ErrInvalidCleanupInterval {
		t.Fatalf("want ErrInvalidCleanupInterval, got %v", err)
	}
}

func TestSetAndGet(t *testing.T) {
	cs := newTestStore(t)

	if err := cs.Set("k1", "v1", nil); err != nil {
		t.Fatal(err)
	}
	v, err := cs.Get("k1")
	if err != nil {
		t.Fatal(err)
	}
	if v != "v1" {
		t.Fatalf("want v1, got %v", v)
	}
}

func TestGet_NotFound(t *testing.T) {
	cs := newTestStore(t)
	_, err := cs.Get("missing")
	if err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGet_Expired(t *testing.T) {
	cs := newTestStore(t)
	ttl := time.Millisecond
	if err := cs.Set("ex", "val", &ttl); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	_, err := cs.Get("ex")
	if err != ErrNotFound {
		t.Fatalf("want ErrNotFound for expired key, got %v", err)
	}
	stats := cs.GetStats()
	if stats.Size != 0 {
		t.Fatalf("expected size 0 after lazy delete, got %d", stats.Size)
	}
}

func TestSetEmptyKey(t *testing.T) {
	cs := newTestStore(t)
	err := cs.Set("", "v", nil)
	if err != ErrInvalidKey {
		t.Fatalf("want ErrInvalidKey, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	cs := newTestStore(t)
	_ = cs.Set("k1", "v1", nil)
	if !cs.Delete("k1") {
		t.Fatal("expected Delete to return true")
	}
	if cs.Delete("k1") {
		t.Fatal("expected Delete to return false for missing key")
	}
}

func TestFlush(t *testing.T) {
	cs := newTestStore(t)
	for i := range 5 {
		_ = cs.Set("k"+string(rune('0'+i)), i, nil)
	}
	cs.Flush()
	stats := cs.GetStats()
	if stats.Size != 0 {
		t.Fatalf("size after flush: want 0, got %d", stats.Size)
	}
}

func TestEviction(t *testing.T) {
	cs, err := NewCacheStore(CacheConfig{
		MaxSize:         3,
		DefaultTTL:      time.Minute,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Stop()
	_ = cs.Set("a", 1, nil)
	_ = cs.Set("b", 2, nil)
	_ = cs.Set("c", 3, nil)
	_ = cs.Set("d", 4, nil)
	stats := cs.GetStats()
	if stats.Evictions != 1 {
		t.Fatalf("want 1 eviction, got %d", stats.Evictions)
	}
	if stats.Size != 3 {
		t.Fatalf("want size 3, got %d", stats.Size)
	}
}

func TestDeleteByPrefix(t *testing.T) {
	cs := newTestStore(t)
	_ = cs.Set("user:1", "a", nil)
	_ = cs.Set("user:2", "b", nil)
	_ = cs.Set("product:1", "c", nil)
	n := cs.DeleteByPrefix("user")
	if n != 2 {
		t.Fatalf("want 2 deleted, got %d", n)
	}
	_, err := cs.Get("product:1")
	if err != nil {
		t.Fatal("product:1 should still exist")
	}
}

func TestGetStats(t *testing.T) {
	cs := newTestStore(t)
	_ = cs.Set("k", "v", nil)
	_, _ = cs.Get("k")
	_, _ = cs.Get("missing")
	s := cs.GetStats()
	if s.Sets != 1 {
		t.Fatalf("sets: want 1, got %d", s.Sets)
	}
	if s.Hits != 1 {
		t.Fatalf("hits: want 1, got %d", s.Hits)
	}
	if s.Misses != 1 {
		t.Fatalf("misses: want 1, got %d", s.Misses)
	}
}

func TestExport(t *testing.T) {
	cs := newTestStore(t)
	_ = cs.Set("a", 1, nil)
	_ = cs.Set("b", 2, nil)
	exported := cs.Export()
	if len(exported) != 2 {
		t.Fatalf("want 2 exported entries, got %d", len(exported))
	}
}

func TestCleanupExpired(t *testing.T) {
	cs := newTestStore(t)
	ttl := time.Millisecond
	_ = cs.Set("expire1", "v", &ttl)
	_ = cs.Set("expire2", "v", &ttl)
	_ = cs.Set("keep", "v", nil)
	time.Sleep(5 * time.Millisecond)
	cs.cleanupExpired()
	if cs.GetStats().Size != 1 {
		t.Fatalf("want 1 remaining entry, got %d", cs.GetStats().Size)
	}
}
