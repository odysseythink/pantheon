package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Short-term recall result cache (M4.11). Mirrors pipeline/recall/cache.py.
//
// Cache key = (thread_id, query_hash); value = the rendered RecallResult.
// TTL defaults to 60 seconds — long enough to absorb the common pattern of
// "user submits same query twice in a row by accident" and short enough
// that stale results don't poison the prompt after a new atom lands.
//
// Process-local, never persisted. Invalidation is intentionally lazy:
// TTL handles staleness; the promotion path does NOT bust the cache. The
// promotion path does NOT bust the cache — a fresh atom flowing in
// mid-thread is picked up at the next 60-second boundary.

const (
	DefaultTTLSeconds  = 60.0
	DefaultMaxEntries  = 256
)

// HashQuery returns a stable 16-hex-char digest used as the second half of
// the cache key.
func HashQuery(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:])[:16]
}

type cacheEntry struct {
	value     *RecallResult
	expiresAt float64
}

// RecallCache is an in-memory TTL cache for RecallForPrompt results.
type RecallCache struct {
	TTLSeconds float64
	MaxEntries int

	mu    sync.Mutex
	store map[[2]string]cacheEntry
	nowFn func() float64
}

// NewRecallCache builds a cache with library defaults (60s TTL, 256 entries).
func NewRecallCache() *RecallCache {
	return &RecallCache{
		TTLSeconds: DefaultTTLSeconds,
		MaxEntries: DefaultMaxEntries,
		store:      map[[2]string]cacheEntry{},
		nowFn:      func() float64 { return time.Since(processStart).Seconds() },
	}
}

var processStart = time.Now()

// Get returns the cached result for (threadID, query), or nil on miss /
// expiry.
func (c *RecallCache) Get(threadID, query string) *RecallResult {
	key := [2]string{threadID, HashQuery(query)}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.store[key]
	if !ok {
		return nil
	}
	if entry.expiresAt <= c.nowFn() {
		delete(c.store, key)
		return nil
	}
	return entry.value
}

// Set stores the result for (threadID, query) and evicts if over cap.
func (c *RecallCache) Set(threadID, query string, value *RecallResult) {
	key := [2]string{threadID, HashQuery(query)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		c.store = map[[2]string]cacheEntry{}
	}
	c.store[key] = cacheEntry{value: value, expiresAt: c.nowFn() + c.TTLSeconds}
	c.evictIfNeededLocked()
}

// Clear empties the cache.
func (c *RecallCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store = map[[2]string]cacheEntry{}
}

// Stats returns the current entry count.
func (c *RecallCache) Stats() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]int{"size": len(c.store)}
}

// evictIfNeededLocked drops the soonest-expiring entries until back under
// the cap. Caller holds mu.
func (c *RecallCache) evictIfNeededLocked() {
	if c.MaxEntries <= 0 || len(c.store) <= c.MaxEntries {
		return
	}
	overflow := len(c.store) - c.MaxEntries
	for i := 0; i < overflow; i++ {
		var oldestKey [2]string
		var oldestExp float64
		first := true
		for k, e := range c.store {
			if first || e.expiresAt < oldestExp {
				oldestKey, oldestExp, first = k, e.expiresAt, false
			}
		}
		delete(c.store, oldestKey)
	}
}
