package cache

import (
	"container/heap"
	"errors"
	"net/http"
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

// CacheStore provides thread-safe caching with TTL and size limits
type CacheStore struct {
	mu          sync.RWMutex
	data        map[string]cacheEntry
	prefixIndex map[string]map[string]struct{} // prefix → set of keys; enables O(k) prefix invalidation
	evictQ      evictHeap                      // min-heap ordered by createdAt for O(log n) eviction
	heapIndex   map[string]*evictHeapEntry     // key → heap entry; enables O(log n) arbitrary removal
	config      CacheConfig

	// Counters — all updated and read atomically; no lock required.
	hits      atomic.Int64
	misses    atomic.Int64
	sets      atomic.Int64
	deletes   atomic.Int64
	evictions atomic.Int64
	size      atomic.Int64 // tracks len(data)
	bytesUsed atomic.Int64 // estimated total bytes across all stored values

	stopCh   chan struct{} // closed by Stop() to terminate the cleanup goroutine
	stopOnce sync.Once     // ensures Stop() is safe to call multiple times
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
		data:        make(map[string]cacheEntry),
		prefixIndex: make(map[string]map[string]struct{}),
		evictQ:      evictHeap{},
		heapIndex:   make(map[string]*evictHeapEntry),
		stopCh:      make(chan struct{}),
		config:      cfg,
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

	cs.mu.Lock()
	defer cs.mu.Unlock()

	// ── Enforce entry-count limit ────────────────────────────────────────
	for cs.config.MaxSize > 0 && len(cs.data) >= cs.config.MaxSize {
		if len(cs.evictQ) == 0 {
			break // defensive: heap empty but data remains — avoid infinite loop
		}
		cs.evictOldest()
	}

	now := time.Now()
	expiresAt := now.Add(ttl)
	entryBytes := estimateBytes(key, value)

	// ── Enforce byte-level memory limit before insert ────────────────────
	memLimit := int64(cs.config.MaxMemoryMB) * 1024 * 1024
	for cs.bytesUsed.Load()+entryBytes > memLimit && len(cs.data) > 0 {
		if len(cs.evictQ) == 0 {
			break // defensive: heap empty — avoid infinite loop
		}
		cs.evictOldest()
	}
	// If the entry alone exceeds the budget, reject immediately.
	if cs.bytesUsed.Load()+entryBytes > memLimit {
		return ErrEntryTooLarge
	}

	// Store entry, adjusting byte accounting for updates vs new insertions.
	// Preserve the original createdAt on updates so that hot (frequently
	// refreshed) keys are not permanently pinned against eviction.
	existing, alreadyExists := cs.data[key]
	createdAt := now
	if alreadyExists {
		createdAt = existing.createdAt
	}
	cs.data[key] = cacheEntry{
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
		cs.indexKey(key)
		// Register in the eviction heap. Existing entries keep their original
		// heap position (createdAt is preserved on update, so order is stable).
		heapEntry := &evictHeapEntry{key: key, createdAt: createdAt}
		heap.Push(&cs.evictQ, heapEntry)
		cs.heapIndex[key] = heapEntry
	}

	// ── 90 % threshold warnings ──────────────────────────────────────────
	sizeRatio := float64(len(cs.data)) / float64(cs.config.MaxSize)
	if sizeRatio >= 0.9 {
		logger.LogWarn("Cache nearing entry-count limit",
			"size", len(cs.data), "maxSize", cs.config.MaxSize,
			"usagePct", int(sizeRatio*100))
	}
	memUsed := cs.bytesUsed.Load()
	if float64(memUsed) >= 0.9*float64(memLimit) {
		logger.LogWarn("Cache nearing memory limit",
			"bytesUsed", memUsed, "limitBytes", memLimit,
			"usagePct", int(float64(memUsed)/float64(memLimit)*100))
	}

	cs.sets.Add(1)

	return nil
}

// Get retrieves a value from cache.
// Returns ErrNotFound if the key does not exist or has expired.
func (c *CacheStore) Get(key string) (any, error) {
	c.mu.RLock()
	entry, exists := c.data[key]
	c.mu.RUnlock()

	if !exists {
		c.misses.Add(1)
		return nil, ErrNotFound
	}

	if time.Now().After(entry.expiresAt) {
		c.misses.Add(1)
		// Lazy delete: attempt a non-blocking write lock so the expired
		// entry is removed immediately when there is no contention.
		// If TryLock fails another goroutine holds the lock and the
		// periodic cleanup will collect the entry instead.
		if c.mu.TryLock() {
			if e, ok := c.data[key]; ok && time.Now().After(e.expiresAt) {
				delete(c.data, key)
				c.unindexKey(key)
				c.removeFromHeap(key)
				c.size.Add(-1)
				c.bytesUsed.Add(-e.bytes)
			}
			c.mu.Unlock()
		}
		return nil, ErrNotFound
	}

	c.hits.Add(1)
	return entry.value, nil
}

// Delete removes a value from cache
func (cs *CacheStore) Delete(key string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	entry, exists := cs.data[key]
	if !exists {
		return false
	}

	delete(cs.data, key)
	cs.unindexKey(key)
	cs.removeFromHeap(key)
	cs.size.Add(-1)
	cs.bytesUsed.Add(-entry.bytes)

	cs.deletes.Add(1)

	logger.LogTrace("Cache DELETE", "key", key)
	return true
}

// Flush clears all cache data
func (c *CacheStore) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Count the entries being flushed so that GetStats().Deletes stays accurate.
	n := int64(len(c.data))

	// Clear maps
	c.data = make(map[string]cacheEntry)
	c.prefixIndex = make(map[string]map[string]struct{})
	c.evictQ = evictHeap{}
	c.heapIndex = make(map[string]*evictHeapEntry)
	c.size.Store(0)
	c.bytesUsed.Store(0)
	if n > 0 {
		c.deletes.Add(n)
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
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	exported := make(map[string]any)
	for key, entry := range cs.data {
		exported[key] = map[string]any{
			"value":     entry.value,
			"createdAt": entry.createdAt,
			"expiresAt": entry.expiresAt,
			"ttl":       time.Until(entry.expiresAt),
		}
	}

	return exported
}

// evictOldest removes the entry with the earliest createdAt while holding
// the write lock. Uses the min-heap for O(log n) time instead of a full scan.
func (cs *CacheStore) evictOldest() {
	if len(cs.evictQ) == 0 {
		return
	}

	e := heap.Pop(&cs.evictQ).(*evictHeapEntry)
	delete(cs.heapIndex, e.key)

	oldestEntry, exists := cs.data[e.key]
	if !exists {
		return
	}
	delete(cs.data, e.key)
	cs.unindexKey(e.key)
	cs.size.Add(-1)
	cs.bytesUsed.Add(-oldestEntry.bytes)
	cs.evictions.Add(1)

	logger.LogTrace("Cache evicted oldest entry", "key", e.key)
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
				cs.mu.Lock()
				cs.trimToMemoryLimitLocked()
				cs.mu.Unlock()
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

// cleanupExpired removes expired entries.
// The scan runs under an RLock to collect up to cleanupBatchSize expired keys,
// then a brief Lock deletes them. This bounds the critical section so that
// large caches do not stall writers for the full O(n) scan. If more expired
// entries remain, the next ticker cycle will continue.
func (cs *CacheStore) cleanupExpired() {
	const cleanupBatchSize = 512

	now := time.Now()

	// Phase 1: collect stale keys under RLock.
	cs.mu.RLock()
	expiredKeys := make([]string, 0, min(cleanupBatchSize, len(cs.data)))
	for key, entry := range cs.data {
		if now.After(entry.expiresAt) {
			expiredKeys = append(expiredKeys, key)
			if len(expiredKeys) >= cleanupBatchSize {
				break
			}
		}
	}
	cs.mu.RUnlock()

	if len(expiredKeys) == 0 {
		return
	}

	// Phase 2: delete collected keys under Lock.
	cs.mu.Lock()
	var expiredBytes int64
	deleted := 0
	for _, key := range expiredKeys {
		entry, exists := cs.data[key]
		if !exists {
			continue // deleted between phases
		}
		// Re-check expiry — entry may have been refreshed between phases.
		if !now.After(entry.expiresAt) {
			continue
		}
		expiredBytes += entry.bytes
		delete(cs.data, key)
		cs.unindexKey(key)
		cs.removeFromHeap(key)
		deleted++
	}
	if deleted > 0 {
		cs.size.Add(-int64(deleted))
		cs.bytesUsed.Add(-expiredBytes)
		cs.deletes.Add(int64(deleted))
	}
	cs.mu.Unlock()

	if deleted > 0 {
		logger.LogTrace("Cache cleanup", "expired", deleted)
	}
}

// DeleteByPrefix removes all cache entries whose key begins with the given
// prefix (i.e. the portion before the first ':', e.g. "u42_products").
// Returns the number of entries deleted.
// This is O(k) in the number of matching keys, not O(n) in the total store size.
func (cs *CacheStore) DeleteByPrefix(prefix string) int {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	keys := cs.prefixIndex[prefix]
	if len(keys) == 0 {
		return 0
	}

	count := 0
	var totalBytes int64
	for key := range keys {
		if entry, exists := cs.data[key]; exists {
			totalBytes += entry.bytes
			delete(cs.data, key)
			cs.removeFromHeap(key)
			count++
		}
	}
	delete(cs.prefixIndex, prefix)
	cs.size.Add(-int64(count))
	cs.bytesUsed.Add(-totalBytes)
	cs.deletes.Add(int64(count))

	return count
}

// removeFromHeap removes the heap entry for key in O(log n) using the stored
// index. Must be called while holding the write lock.
func (cs *CacheStore) removeFromHeap(key string) {
	e, ok := cs.heapIndex[key]
	if !ok {
		return
	}
	if e.index >= 0 && e.index < len(cs.evictQ) {
		heap.Remove(&cs.evictQ, e.index)
	}
	delete(cs.heapIndex, key)
}

// indexKey adds key to the prefix index. Called while holding the write lock.
func (cs *CacheStore) indexKey(key string) {
	p := cacheKeyPrefix(key)
	if cs.prefixIndex[p] == nil {
		cs.prefixIndex[p] = make(map[string]struct{})
	}
	cs.prefixIndex[p][key] = struct{}{}
}

// unindexKey removes key from the prefix index. Called while holding the write lock.
func (cs *CacheStore) unindexKey(key string) {
	p := cacheKeyPrefix(key)
	delete(cs.prefixIndex[p], key)
	if len(cs.prefixIndex[p]) == 0 {
		delete(cs.prefixIndex, p)
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

// trimToMemoryLimitLocked evicts the oldest entries until bytesUsed falls
// below the configured MaxMemoryMB limit. Must be called while holding the
// write lock (cs.mu).
func (cs *CacheStore) trimToMemoryLimitLocked() {
	limit := int64(cs.config.MaxMemoryMB) * 1024 * 1024
	for cs.bytesUsed.Load() > limit && len(cs.data) > 0 {
		cs.evictOldest()
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

// Error definitions
var (
	ErrInvalidKey             = errors.New("invalid key: cannot be empty")
	ErrInvalidTTL             = errors.New("invalid TTL: must be greater than zero")
	ErrInvalidCleanupInterval = errors.New("invalid CleanupInterval: must be greater than zero")
	ErrInvalidMaxSize         = errors.New("invalid MaxSize: must be greater than zero")
	ErrInvalidMaxMemory       = errors.New("invalid MaxMemoryMB: must be greater than zero")
	ErrEntryTooLarge          = errors.New("entry exceeds MaxMemoryMB limit and was evicted immediately")
	ErrNotFound               = errors.New("key not found in cache")
)
