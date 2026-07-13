package cache

import (
	"errors"
	"hash/maphash"
	"strconv"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *CacheStore {
	t.Helper()
	cs, err := NewCacheStore(CacheConfig{
		MaxSize:         100,
		DefaultTTL:      time.Minute,
		CleanupInterval: time.Hour,
		MaxMemoryMB:     64,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cs.Stop)
	return cs
}

func TestNewCacheStore_InvalidConfig(t *testing.T) {
	_, err := NewCacheStore(CacheConfig{DefaultTTL: 0, CleanupInterval: time.Second, MaxSize: 10, MaxMemoryMB: 1})
	if !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("want ErrInvalidTTL, got %v", err)
	}

	_, err = NewCacheStore(CacheConfig{DefaultTTL: time.Second, CleanupInterval: 0, MaxSize: 10, MaxMemoryMB: 1})
	if !errors.Is(err, ErrInvalidCleanupInterval) {
		t.Fatalf("want ErrInvalidCleanupInterval, got %v", err)
	}

	_, err = NewCacheStore(CacheConfig{DefaultTTL: time.Second, CleanupInterval: time.Second, MaxSize: 0, MaxMemoryMB: 1})
	if !errors.Is(err, ErrInvalidMaxSize) {
		t.Fatalf("want ErrInvalidMaxSize, got %v", err)
	}

	_, err = NewCacheStore(CacheConfig{DefaultTTL: time.Second, CleanupInterval: time.Second, MaxSize: 10, MaxMemoryMB: 0})
	if !errors.Is(err, ErrInvalidMaxMemory) {
		t.Fatalf("want ErrInvalidMaxMemory, got %v", err)
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
	if !errors.Is(err, ErrNotFound) {
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
	if !errors.Is(err, ErrNotFound) {
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
	if !errors.Is(err, ErrInvalidKey) {
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
		MaxMemoryMB:     64,
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

// keyForShard returns a key that hashes to shard index target for cs. It is a
// white-box helper (same package) used to construct deliberately shard-skewed
// workloads for the byte-limit eviction tests below.
func keyForShard(t *testing.T, cs *CacheStore, prefix string, target uint64) string {
	t.Helper()
	for i := range 100_000 {
		k := prefix + ":" + strconv.Itoa(i)
		if &cs.shards[hashKeyToShard(cs, k)] == &cs.shards[target] {
			return k
		}
	}
	t.Fatalf("could not find a key hashing to shard %d", target)
	return ""
}

// hashKeyToShard mirrors CacheStore.shardFor's index computation so tests can
// assert which shard a key lands on without exporting internals.
func hashKeyToShard(cs *CacheStore, key string) uint64 {
	return maphash.Bytes(cs.seed, []byte(key)) % cacheShards
}

// TestSet_ByteLimit_CrossShardEviction verifies that when the incoming key
// hashes to a shard with nothing to evict, Set reclaims bytes from other
// shards instead of wrongly returning ErrEntryTooLarge. Regression test for
// the byte-limit eviction only walking the current shard.
func TestSet_ByteLimit_CrossShardEviction(t *testing.T) {
	// 1 MiB budget. Large values so a handful fill the budget.
	cs, err := NewCacheStore(CacheConfig{
		MaxSize:         1_000_000, // effectively unlimited entry count
		DefaultTTL:      time.Minute,
		CleanupInterval: time.Hour,
		MaxMemoryMB:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cs.Stop)

	// Pick a target shard for the final insert, then fill *other* shards with
	// large values so the budget is nearly exhausted while the target shard
	// stays empty.
	const targetShard = 0
	big := make([]byte, 200*1024) // 200 KiB each → ~5 fill the 1 MiB budget
	filled := 0
	for i := range 100_000 {
		k := "fill:" + strconv.Itoa(i)
		if hashKeyToShard(cs, k) == targetShard {
			continue // keep the target shard empty
		}
		if err := cs.Set(k, big, nil); err != nil {
			// Budget reached; stop filling.
			break
		}
		filled++
		if cs.GetStats().BytesUsed > 800*1024 {
			break
		}
	}
	if filled == 0 {
		t.Fatal("failed to pre-fill other shards")
	}

	// This key hashes to the empty target shard. With the old single-shard
	// eviction it would return ErrEntryTooLarge; with cross-shard fallback it
	// must succeed by evicting from the populated shards.
	targetKey := keyForShard(t, cs, "target", targetShard)
	if err := cs.Set(targetKey, big, nil); err != nil {
		t.Fatalf("Set on empty target shard should succeed via cross-shard eviction, got %v", err)
	}
	if _, err := cs.Get(targetKey); err != nil {
		t.Fatalf("target key should be present after Set, got %v", err)
	}
	// Budget must still be respected.
	if used, limit := cs.GetStats().BytesUsed, int64(1)*1024*1024; used > limit {
		t.Fatalf("bytesUsed %d exceeds limit %d", used, limit)
	}
}

// TestSet_OversizedEntry_StillRejected confirms that a single entry larger
// than the entire MaxMemoryMB budget is still rejected with ErrEntryTooLarge
// even after the cross-shard eviction fallback.
func TestSet_OversizedEntry_StillRejected(t *testing.T) {
	cs, err := NewCacheStore(CacheConfig{
		MaxSize:         1_000,
		DefaultTTL:      time.Minute,
		CleanupInterval: time.Hour,
		MaxMemoryMB:     1, // 1 MiB budget
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cs.Stop)

	huge := make([]byte, 2*1024*1024) // 2 MiB > 1 MiB budget
	if err := cs.Set("huge", huge, nil); !errors.Is(err, ErrEntryTooLarge) {
		t.Fatalf("want ErrEntryTooLarge for oversized entry, got %v", err)
	}
}
