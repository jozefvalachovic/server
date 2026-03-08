package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCacheStore_ConcurrentSetGetDelete(t *testing.T) {
	cs := newTestStore(t)

	var wg sync.WaitGroup
	const goroutines = 50
	const keysPerGoroutine = 100

	// Concurrent writers.
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for k := range keysPerGoroutine {
				key := fmt.Sprintf("race:%d:%d", g, k)
				if err := cs.Set(key, "value", nil); err != nil {
					t.Errorf("Set(%q): %v", key, err)
				}
			}
		}(g)
	}

	// Concurrent readers.
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for k := range keysPerGoroutine {
				key := fmt.Sprintf("race:%d:%d", g, k)
				_, _ = cs.Get(key)
			}
		}(g)
	}

	// Concurrent deleters.
	for g := range goroutines / 5 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for k := range keysPerGoroutine {
				key := fmt.Sprintf("race:%d:%d", g, k)
				cs.Delete(key)
			}
		}(g)
	}

	// Concurrent GetStats calls.
	for range goroutines / 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range keysPerGoroutine {
				_ = cs.GetStats()
			}
		}()
	}

	wg.Wait()
}

func TestCacheStore_ConcurrentPrefixInvalidation(t *testing.T) {
	cs := newTestStore(t)

	var wg sync.WaitGroup
	const writers = 20
	const keysPerWriter = 50

	// Seed keys with a shared prefix.
	for g := range writers {
		for k := range keysPerWriter {
			key := fmt.Sprintf("products:GET:/list:%d_%d", g, k)
			if err := cs.Set(key, "data", nil); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
	}

	// Concurrent reads + prefix invalidation.
	for g := range writers {
		wg.Add(2)
		go func(g int) {
			defer wg.Done()
			for k := range keysPerWriter {
				key := fmt.Sprintf("products:GET:/list:%d_%d", g, k)
				_, _ = cs.Get(key)
			}
		}(g)
		go func(g int) {
			defer wg.Done()
			cs.DeleteByPrefix("products")
		}(g)
	}

	wg.Wait()
}

func TestCacheStore_ConcurrentEviction(t *testing.T) {
	cs, err := NewCacheStore(CacheConfig{
		MaxSize:         20, // tiny limit to force evictions
		DefaultTTL:      time.Minute,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cs.Stop)

	var wg sync.WaitGroup
	const goroutines = 30
	const keysPerGoroutine = 50

	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for k := range keysPerGoroutine {
				key := fmt.Sprintf("evict:%d:%d", g, k)
				_ = cs.Set(key, "payload", nil)
				_, _ = cs.Get(key)
			}
		}(g)
	}

	wg.Wait()

	stats := cs.GetStats()
	if stats.Evictions == 0 {
		t.Fatal("expected evictions to have occurred")
	}
}
