package inventory

import (
	"sync"
	"time"
)

// Cache keeps looked-up values for TTL.
type Cache struct {
	mutex   sync.Mutex
	TTL     time.Duration
	Now     func() time.Time
	entries map[string]entry
}

type entry struct {
	value   int
	expires time.Time
}

// NewCache is an empty cache.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{TTL: ttl, Now: time.Now, entries: map[string]entry{}}
}

// Put keeps value under key until the TTL passes.
func (cache *Cache) Put(key string, value int) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	cache.entries[key] = entry{value: value, expires: cache.Now().Add(cache.TTL)}
}

// Get is the value under key while it has not expired.
func (cache *Cache) Get(key string) (int, bool) {
	found, ok := cache.entries[key]
	if !ok {
		return 0, false
	}
	if found.expires.After(cache.Now()) {
		return 0, false
	}
	return found.value, true
}
