// Package singleflight provides a duplicate function-call suppression
// mechanism.
//
// It is a minimal, dependency-free reimplementation of the subset of
// golang.org/x/sync/singleflight used internally by the HTTP cache
// middleware. Only Do is implemented — DoChan and Forget are intentionally
// omitted because the cache-miss path has no need for cancellation or
// explicit key eviction (misses are brief and self-clearing).
//
// Concurrent calls to Do with the same key share a single execution of fn.
// The first caller runs fn; all later callers block until fn returns and
// receive the same (value, error) result. The shared flag reports whether
// the call was deduplicated (true for followers, false for the first caller).
//
// # Re-entrancy
//
// Do is NOT re-entrant. A Do invocation whose fn recursively calls Do on the
// same Group with the same key will deadlock: fn holds the follower WaitGroup
// while waiting for itself. Callers must ensure the work inside fn resolves
// without re-entering the Group on the identical key — which is the natural
// invariant for cache-miss paths (fetch once, populate, return).
package singleflight

import "sync"

// call is an in-flight or completed singleflight call.
type call struct {
	wg  sync.WaitGroup
	val any
	err error
	// dups counts the number of duplicate callers (excluding the leader)
	// and is snapshotted into the returned shared flag for each follower.
	dups int
}

// Group represents a class of work and forms a namespace in which units of
// work can be executed with duplicate suppression.
// The zero value of Group is ready for use.
type Group struct {
	mu sync.Mutex       // protects m
	m  map[string]*call // lazily initialised
}

// Do executes and returns the results of fn, making sure that only one
// execution is in-flight for a given key at a time. If a duplicate call
// comes in, the duplicate caller waits for the original call to complete
// and receives the same results. The shared return value indicates whether
// v was given to multiple callers.
func (g *Group) Do(key string, fn func() (any, error)) (v any, err error, shared bool) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok {
		c.dups++
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err, true
	}
	c := &call{}
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	dups := c.dups
	g.mu.Unlock()

	return c.val, c.err, dups > 0
}
