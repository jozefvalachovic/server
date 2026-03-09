package config

import "time"

const (
	// Controller timeout constants for consistency
	DefaultReadTimeout   = 30 * time.Second  // Regular GET operations
	DefaultWriteTimeout  = 60 * time.Second  // POST/PUT/DELETE operations
	DefaultStreamTimeout = 290 * time.Second // Streaming (10s safety margin) — available for caller use

	// Safety margins — exported for callers building custom timeout logic.
	StreamSafetyMargin = 10 * time.Second
	WriteSafetyMargin  = 5 * time.Second

	// Pagination limits
	MaxPageLimit = 1_000
	MaxOffset    = 10_000
)
