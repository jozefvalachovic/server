package cache

import (
	"container/heap"
	"errors"
	"hash/maphash"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jozefvalachovic/logger/v4"
)

// ── eviction min-heap ────────────────────────────────────────────────────────

// evictHeapEntry tracks a cache key's insertion time and its current position
// in the min-heap so that arbitrary entries can be removed in O(log n).
type evictHeapEntry struct {
	key       string
	createdAt time.Time
	index     int // maintained by Swap; -1 when not in heap
}

// evictHeap is a min-heap of entries ordered by createdAt (oldest first),
// implementing container/heap.Interface.
type evictHeap []*evictHeapEntry

func (h evictHeap) Len() int           { return len(h) }
func (h evictHeap) Less(i, j int) bool { return h[i].createdAt.Before(h[j].createdAt) }
func (h evictHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *evictHeap) Push(x any) {
	e := x.(*evictHeapEntry)
	e.index = len(*h)
	*h = append(*h, e)
}
func (h *evictHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil // prevent memory leak
	e.index = -1
	*h = old[:n-1]
	return e
}

// CacheStats provides observability into cache performance
type CacheStats struct {
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Sets      int64 `json:"sets"`
	Deletes   int64 `json:"deletes"`
	Evictions int64 `json:"evictions"`
	Size      int64 `json:"size"`
	BytesUsed int64 `json:"bytesUsed"`
}

// CacheConfig holds configuration for cache behavior.
// All fields are required unless stated otherwise.
type CacheConfig struct {
	// MaxSize is the maximum number of entries the cache may hold.
	// When reached, the oldest entries are evicted before a new one is inserted.
	// Must be > 0.
	MaxSize         int           `json:"maxSize"`
	DefaultTTL      time.Duration `json:"defaultTTL"`
	CleanupInterval time.Duration `json:"cleanupInterval"`
	// MaxMemoryMB caps the total estimated memory used by cached values.
	// When reached, the oldest entries are evicted before a new one is inserted.
	// Must be > 0.
	MaxMemoryMB int `json:"maxMemoryMB"`
}

// CachedResponse is the value type stored when caching HTTP responses.
// It captures everything needed to replay a response to subsequent clients.
// Headers contains the full set of response headers (excluding X-Cache, which
// is derived on every response). The complete header map is replayed on a HIT
// so that clients receive all original headers (ETag, Content-Disposition, etc.).
type CachedResponse struct {
	StatusCode int         `json:"statusCode"`
	Headers    http.Header `json:"headers"`
	Body       []byte      `json:"body"`
}

const cacheShards = 16

// cacheShard holds one partition of the cache data. Each shard has its own
// lock, data map, prefix index, and eviction heap so that concurrent
// operations on different keys rarely contend on the same mutex.
type cacheShard struct {
	mu          sync.RWMutex
	data        map[string]cacheEntry
	prefixIndex map[string]map[string]struct{} // prefix → set of keys
	evictQ      evictHeap                      // min-heap ordered by createdAt
	heapIndex   map[string]*evictHeapEntry     // key → heap entry
}

// CacheStore provides thread-safe caching with TTL and size limits.
// Internally it partitions data across 16 shards (like the rate limiter)
// so that concurrent Set/Get/Delete calls on different keys contend on
// independent mutexes.
type CacheStore struct {
	shards [cacheShards]cacheShard
	seed   maphash.Seed
	config CacheConfig

	// Counters — all updated and read atomically; no lock required.
	hits      atomic.Int64
	misses    atomic.Int64
	sets      atomic.Int64
	deletes   atomic.Int64
	evictions atomic.Int64
	size      atomic.Int64 // tracks total entry count across all shards
	bytesUsed atomic.Int64 // estimated total bytes across all stored values

	stopCh   chan struct{} // closed by Stop() to terminate the cleanup goroutine
	stopOnce sync.Once     // ensures Stop() is safe to call multiple times
}

// shardFor returns the shard that owns the given key.
func (cs *CacheStore) shardFor(key string) *cacheShard {
	h := maphash.Bytes(cs.seed, []byte(key))
	return &cs.shards[h%cacheShards]
}

type cacheEntry struct {
	value     any
	createdAt time.Time
	expiresAt time.Time
	bytes     int64 // estimated memory footprint, set once at insertion
}

// NewCacheStore creates a new, independent CacheStore with the given config.
// Unlike GetCache, each call returns a fresh instance — not a singleton.
// The caller is responsible for calling Stop() when done.
// Returns an error if any required field is zero or negative.
func NewCacheStore(cfg CacheConfig) (*CacheStore, error) {
	if cfg.DefaultTTL <= 0 {
		return nil, ErrInvalidTTL
	}
	if cfg.CleanupInterval <= 0 {
		return nil, ErrInvalidCleanupInterval
	}
	if cfg.MaxSize <= 0 {
		return nil, ErrInvalidMaxSize
	}
	if cfg.MaxMemoryMB <= 0 {
		return nil, ErrInvalidMaxMemory
	}
	cs := &CacheStore{
		seed:   maphash.MakeSeed(),
		stopCh: make(chan struct{}),
		config: cfg,
	}
	for i := range cs.shards {
		cs.shards[i] = cacheShard{
			data:        make(map[string]cacheEntry),
			prefixIndex: make(map[string]map[string]struct{}),
			evictQ:      evictHeap{},
			heapIndex:   make(map[string]*evictHeapEntry),
		}
	}
	go cs.cleanupLoop()
	return cs, nil
}

// Set stores a value with TTL
func (cs *CacheStore) Set(key string, value any, customTtl *time.Duration) error {
	if key == "" {
		return ErrInvalidKey
	}

	ttl := cs.config.DefaultTTL
	if customTtl != nil {
		ttl = *customTtl
	}
	if ttl <= 0 {
		return ErrInvalidTTL
	}

	s := cs.shardFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()

	// ── Enforce entry-count limit ────────────────────────────────────────
	for cs.config.MaxSize > 0 && cs.size.Load() >= int64(cs.config.MaxSize) {
		if len(s.evictQ) == 0 {
			// Current shard has nothing to evict; try other shards.
			if !cs.evictFromOtherShard(s) {
				break
			}
			continue
		}
		cs.evictOldestFromShardLocked(s)
	}

	now := time.Now()
	expiresAt := now.Add(ttl)
	entryBytes := estimateBytes(key, value)

	// ── Enforce byte-level memory limit before insert ────────────────────
	memLimit := int64(cs.config.MaxMemoryMB) * 1024 * 1024
	for cs.bytesUsed.Load()+entryBytes > memLimit && len(s.data) > 0 {
		if len(s.evictQ) == 0 {
			break // defensive: heap empty — avoid infinite loop
		}
		cs.evictOldestFromShardLocked(s)
	}
	// If the entry alone exceeds the budget, reject immediately.
	if cs.bytesUsed.Load()+entryBytes > memLimit {
		return ErrEntryTooLarge
	}

	// Store entry, adjusting byte accounting for updates vs new insertions.
	// Preserve the original createdAt on updates so that hot (frequently
	// refreshed) keys are not permanently pinned against eviction.
	existing, alreadyExists := s.data[key]
	createdAt := now
	if alreadyExists {
		createdAt = existing.createdAt
	}
	s.data[key] = cacheEntry{
		value:     value,
		createdAt: createdAt,
		expiresAt: expiresAt,
		bytes:     entryBytes,
	}
	if alreadyExists {
		cs.bytesUsed.Add(entryBytes - existing.bytes)
	} else {
		cs.size.Add(1)
		cs.bytesUsed.Add(entryBytes)
		s.indexKey(key)
		// Register in the eviction heap. Existing entries keep their original
		// heap position (createdAt is preserved on update, so order is stable).
		heapEntry := &evictHeapEntry{key: key, createdAt: createdAt}
		heap.Push(&s.evictQ, heapEntry)
		s.heapIndex[key] = heapEntry
	}

	// ── 90 % threshold warnings ──────────────────────────────────────────
	// Logged at Trace level because this fires on every Set() above 90%
	// capacity — normal operating state for a properly-sized cache.
	sizeRatio := float64(cs.size.Load()) / float64(cs.config.MaxSize)
	if sizeRatio >= 0.9 {
		logger.LogTrace("Cache nearing entry-count limit",
			"size", cs.size.Load(), "maxSize", cs.config.MaxSize,
			"usagePct", int(sizeRatio*100))
	}
	memUsed := cs.bytesUsed.Load()
	if float64(memUsed) >= 0.9*float64(memLimit) {
		logger.LogTrace("Cache nearing memory limit",
			"bytesUsed", memUsed, "limitBytes", memLimit,
			"usagePct", int(float64(memUsed)/float64(memLimit)*100))
	}

	cs.sets.Add(1)

	return nil
}

// Get retrieves a value from cache.
// Returns ErrNotFound if the key does not exist or has expired.
func (cs *CacheStore) Get(key string) (any, error) {
	s := cs.shardFor(key)
	s.mu.RLock()
	entry, exists := s.data[key]
	s.mu.RUnlock()

	if !exists {
		cs.misses.Add(1)
		return nil, ErrNotFound
	}

	if time.Now().After(entry.expiresAt) {
		cs.misses.Add(1)
		// Lazy delete: attempt a non-blocking write lock so the expired
		// entry is removed immediately when there is no contention.
		// If TryLock fails another goroutine holds the lock and the
		// periodic cleanup will collect the entry instead.
		if s.mu.TryLock() {
			if e, ok := s.data[key]; ok && time.Now().After(e.expiresAt) {
				delete(s.data, key)
				s.unindexKey(key)
				s.removeFromHeap(key)
				cs.size.Add(-1)
				cs.bytesUsed.Add(-e.bytes)
			}
			s.mu.Unlock()
		}
		return nil, ErrNotFound
	}

	cs.hits.Add(1)
	return entry.value, nil
}

// Delete removes a value from cache
func (cs *CacheStore) Delete(key string) bool {
	s := cs.shardFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.data[key]
	if !exists {
		return false
	}

	delete(s.data, key)
	s.unindexKey(key)
	s.removeFromHeap(key)
	cs.size.Add(-1)
	cs.bytesUsed.Add(-entry.bytes)

	cs.deletes.Add(1)

	logger.LogTrace("Cache DELETE", "key", key)
	return true
}

// Flush clears all cache data
func (cs *CacheStore) Flush() {
	var total int64
	for i := range cs.shards {
		s := &cs.shards[i]
		s.mu.Lock()
		total += int64(len(s.data))
		s.data = make(map[string]cacheEntry)
		s.prefixIndex = make(map[string]map[string]struct{})
		s.evictQ = evictHeap{}
		s.heapIndex = make(map[string]*evictHeapEntry)
		s.mu.Unlock()
	}
	cs.size.Store(0)
	cs.bytesUsed.Store(0)
	if total > 0 {
		cs.deletes.Add(total)
	}

	logger.LogInfo("Cache flushed")
}

// GetStats returns a consistent snapshot of cache statistics.
// All fields are read atomically so no lock is required.
func (cs *CacheStore) GetStats() CacheStats {
	return CacheStats{
		Hits:      cs.hits.Load(),
		Misses:    cs.misses.Load(),
		Sets:      cs.sets.Load(),
		Deletes:   cs.deletes.Load(),
		Evictions: cs.evictions.Load(),
		Size:      cs.size.Load(),
		BytesUsed: cs.bytesUsed.Load(),
	}
}

// Export returns all cache data for debugging
func (cs *CacheStore) Export() map[string]any {
	exported := make(map[string]any)
	for i := range cs.shards {
		s := &cs.shards[i]
		s.mu.RLock()
		for key, entry := range s.data {
			exported[key] = map[string]any{
				"value":     entry.value,
				"createdAt": entry.createdAt,
				"expiresAt": entry.expiresAt,
				"ttl":       time.Until(entry.expiresAt),
			}
		}
		s.mu.RUnlock()
	}

	return exported
}

// evictOldestFromShardLocked removes the entry with the earliest createdAt
// from the given shard. The caller must hold s.mu in write mode.
// Uses the min-heap for O(log n) time instead of a full scan.
func (cs *CacheStore) evictOldestFromShardLocked(s *cacheShard) {
	if len(s.evictQ) == 0 {
		return
	}

	e := heap.Pop(&s.evictQ).(*evictHeapEntry)
	delete(s.heapIndex, e.key)

	oldestEntry, exists := s.data[e.key]
	if !exists {
		return
	}
	delete(s.data, e.key)
	s.unindexKey(e.key)
	cs.size.Add(-1)
	cs.bytesUsed.Add(-oldestEntry.bytes)
	cs.evictions.Add(1)

	logger.LogTrace("Cache evicted oldest entry", "key", e.key)
}

// evictFromOtherShard tries to evict one entry from any shard other than
// exclude. It uses TryLock to avoid deadlocking when the caller already
// holds exclude.mu. Returns true if an entry was evicted.
func (cs *CacheStore) evictFromOtherShard(exclude *cacheShard) bool {
	for i := range cs.shards {
		other := &cs.shards[i]
		if other == exclude {
			continue
		}
		if !other.mu.TryLock() {
			continue // skip busy shards to avoid deadlock
		}
		if len(other.evictQ) > 0 {
			cs.evictOldestFromShardLocked(other)
			other.mu.Unlock()
			return true
		}
		other.mu.Unlock()
	}
	return false
}

// cleanupLoop periodically removes expired entries.
// It exits when Stop() is called.
func (cs *CacheStore) cleanupLoop() {
	ticker := time.NewTicker(cs.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cs.cleanupExpired()
			// After TTL cleanup, enforce the byte limit in case deletions freed
			// space that now lets over-limit entries remain (e.g. after a burst).
			if cs.config.MaxMemoryMB > 0 &&
				cs.bytesUsed.Load() > int64(cs.config.MaxMemoryMB)*1024*1024 {
				cs.trimToMemoryLimit()
			}
		case <-cs.stopCh:
			return
		}
	}
}

// Stop terminates the background cleanup goroutine.
// Safe to call multiple times; subsequent calls are no-ops.
func (cs *CacheStore) Stop() {
	cs.stopOnce.Do(func() {
		close(cs.stopCh)
	})
}

// cleanupExpired removes expired entries shard by shard.
// Each shard is locked independently so that large caches do not stall
// writers while the full store is scanned.
func (cs *CacheStore) cleanupExpired() {
	const cleanupBatchSize = 512

	now := time.Now()

	for i := range cs.shards {
		s := &cs.shards[i]

		// Phase 1: collect stale keys under RLock.
		s.mu.RLock()
		expiredKeys := make([]string, 0, min(cleanupBatchSize, len(s.data)))
		for key, entry := range s.data {
			if now.After(entry.expiresAt) {
				expiredKeys = append(expiredKeys, key)
				if len(expiredKeys) >= cleanupBatchSize {
					break
				}
			}
		}
		s.mu.RUnlock()

		if len(expiredKeys) == 0 {
			continue
		}

		// Phase 2: delete collected keys under Lock.
		s.mu.Lock()
		var expiredBytes int64
		deleted := 0
		for _, key := range expiredKeys {
			entry, exists := s.data[key]
			if !exists {
				continue // deleted between phases
			}
			// Re-check expiry — entry may have been refreshed between phases.
			if !now.After(entry.expiresAt) {
				continue
			}
			expiredBytes += entry.bytes
			delete(s.data, key)
			s.unindexKey(key)
			s.removeFromHeap(key)
			deleted++
		}
		if deleted > 0 {
			cs.size.Add(-int64(deleted))
			cs.bytesUsed.Add(-expiredBytes)
			cs.deletes.Add(int64(deleted))
			// Reclaim excess heap capacity after batch eviction so the backing
			// array does not retain its peak allocation indefinitely.
			s.evictQ = slices.Clip(s.evictQ)
		}
		s.mu.Unlock()

		if deleted > 0 {
			logger.LogTrace("Cache cleanup", "shard", i, "expired", deleted)
		}
	}
}

// DeleteByPrefix removes all cache entries whose key begins with the given
// prefix (i.e. the portion before the first ':', e.g. "u42_products").
// Returns the number of entries deleted.
// Each shard's prefix index is checked independently.
func (cs *CacheStore) DeleteByPrefix(prefix string) int {
	total := 0
	var totalBytes int64
	for i := range cs.shards {
		s := &cs.shards[i]
		s.mu.Lock()
		keys := s.prefixIndex[prefix]
		if len(keys) > 0 {
			for key := range keys {
				if entry, exists := s.data[key]; exists {
					totalBytes += entry.bytes
					delete(s.data, key)
					s.removeFromHeap(key)
					total++
				}
			}
			delete(s.prefixIndex, prefix)
		}
		s.mu.Unlock()
	}
	if total > 0 {
		cs.size.Add(-int64(total))
		cs.bytesUsed.Add(-totalBytes)
		cs.deletes.Add(int64(total))
	}

	return total
}

// removeFromHeap removes the heap entry for key in O(log n) using the stored
// index. Must be called while holding the shard's write lock.
func (s *cacheShard) removeFromHeap(key string) {
	e, ok := s.heapIndex[key]
	if !ok {
		return
	}
	if e.index >= 0 && e.index < len(s.evictQ) {
		heap.Remove(&s.evictQ, e.index)
	}
	delete(s.heapIndex, key)
}

// indexKey adds key to the shard's prefix index. Called while holding the write lock.
func (s *cacheShard) indexKey(key string) {
	p := cacheKeyPrefix(key)
	if s.prefixIndex[p] == nil {
		s.prefixIndex[p] = make(map[string]struct{})
	}
	s.prefixIndex[p][key] = struct{}{}
}

// unindexKey removes key from the shard's prefix index. Called while holding the write lock.
func (s *cacheShard) unindexKey(key string) {
	p := cacheKeyPrefix(key)
	delete(s.prefixIndex[p], key)
	if len(s.prefixIndex[p]) == 0 {
		delete(s.prefixIndex, p)
	}
}

// cacheKeyPrefix extracts the prefix portion of a key (everything before the first ':').
// Keys without a ':' are their own prefix.
func cacheKeyPrefix(key string) string {
	if before, _, ok := strings.Cut(key, ":"); ok {
		return before
	}
	return key
}

// trimToMemoryLimit evicts the oldest entries across shards in round-robin
// fashion until bytesUsed falls below the configured MaxMemoryMB limit.
func (cs *CacheStore) trimToMemoryLimit() {
	limit := int64(cs.config.MaxMemoryMB) * 1024 * 1024
	for cs.bytesUsed.Load() > limit {
		evicted := false
		for i := range cs.shards {
			if cs.bytesUsed.Load() <= limit {
				return
			}
			s := &cs.shards[i]
			s.mu.Lock()
			if len(s.evictQ) > 0 {
				cs.evictOldestFromShardLocked(s)
				s.evictQ = slices.Clip(s.evictQ)
				evicted = true
			}
			s.mu.Unlock()
		}
		if !evicted {
			break // nothing left to evict
		}
	}
}

// estimateBytes returns a rough byte estimate for the given key-value pair.
// For CachedResponse values the estimate is precise (key + body + content-type).
// For other types a conservative fixed overhead is used.
func estimateBytes(key string, v any) int64 {
	// 16 bytes per Go string header + actual content bytes.
	base := int64(len(key)) + 16
	switch r := v.(type) {
	case CachedResponse:
		headerBytes := int64(0)
		for k, vv := range r.Headers {
			headerBytes += int64(len(k))
			for _, v := range vv {
				headerBytes += int64(len(v))
			}
		}
		return base + int64(len(r.Body)) + headerBytes + 8
	case string:
		return base + int64(len(r))
	case []byte:
		return base + int64(len(r))
	case bool:
		return base + 1
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return base + 8
	default:
		// Unknown composite type: key + conservative fixed overhead.
		// Only CachedResponse, string, []byte, and primitive types are
		// estimated precisely. For accurate memory tracking, prefer
		// storing one of those types.
		return base + 64
	}
}

// Sentinel errors returned by the cache API.
var (
	// ErrInvalidKey is returned when a Set/Get/Delete call receives an empty key.
	ErrInvalidKey = errors.New("invalid key: cannot be empty")
	// ErrInvalidTTL is returned by Validate when TTL is non-positive.
	ErrInvalidTTL = errors.New("invalid TTL: must be greater than zero")
	// ErrInvalidCleanupInterval is returned by Validate when CleanupInterval is non-positive.
	ErrInvalidCleanupInterval = errors.New("invalid CleanupInterval: must be greater than zero")
	// ErrInvalidMaxSize is returned by Validate when MaxSize is non-positive.
	ErrInvalidMaxSize = errors.New("invalid MaxSize: must be greater than zero")
	// ErrInvalidMaxMemory is returned by Validate when MaxMemoryMB is non-positive.
	ErrInvalidMaxMemory = errors.New("invalid MaxMemoryMB: must be greater than zero")
	// ErrEntryTooLarge is returned by Set when the single entry exceeds the
	// configured MaxMemoryMB budget and is therefore evicted immediately.
	ErrEntryTooLarge = errors.New("entry exceeds MaxMemoryMB limit and was evicted immediately")
	// ErrNotFound is returned by Get when the key is absent or has expired.
	ErrNotFound = errors.New("key not found in cache")
)
