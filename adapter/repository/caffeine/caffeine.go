package caffeine

import (
	"container/list"
	"errors"
	"sync"
	"time"
)

// Loader is an async function to load missing values.
type Loader func(key string) (any, error)

// EvictionReason describes why an entry was evicted.
type EvictionReason string

const (
	ReasonSize    EvictionReason = "size"
	ReasonExpired EvictionReason = "expired"
	ReasonManual  EvictionReason = "manual"
	ReasonClear   EvictionReason = "clear"
)

// EvictionListener is called when an entry is evicted.
type EvictionListener func(key string, value any, reason EvictionReason)

// CacheStats stores statistics about cache usage.
type CacheStats struct {
	Hits        int64
	Misses      int64
	LoadSuccess int64
	LoadFailure int64
	Evictions   int64
}

// entry is an internal cache record.
type entry struct {
	key        string
	value      any
	createdAt  time.Time
	lastAccess time.Time
	element    *list.Element
	refreshing bool
}

// CaffeineCache is the main cache object.
type CaffeineCache struct {
	maxSize           int
	expireAfterWrite  time.Duration
	expireAfterAccess time.Duration
	refreshAfterWrite time.Duration
	loader            Loader
	evictionListener  EvictionListener

	mu      sync.Mutex
	entries map[string]*entry
	order   *list.List // LRU order (back = MRU, front = LRU)
	stats   CacheStats
	loading map[string]*sync.WaitGroup
}

type CaffeineCacheConfig struct {
	MaxSize           int
	ExpireAfterWrite  time.Duration
	ExpireAfterAccess time.Duration
	RefreshAfterWrite time.Duration
	Loader            Loader
	EvictionListener  EvictionListener
}

// NewCache creates a new cache.
func NewCache(cfg CaffeineCacheConfig) *CaffeineCache {

	return &CaffeineCache{
		maxSize:           cfg.MaxSize,
		expireAfterWrite:  cfg.ExpireAfterWrite,
		expireAfterAccess: cfg.ExpireAfterAccess,
		refreshAfterWrite: cfg.RefreshAfterWrite,
		loader:            cfg.Loader,
		evictionListener:  cfg.EvictionListener,
		entries:           make(map[string]*entry),
		order:             list.New(),
		loading:           make(map[string]*sync.WaitGroup),
	}
}

// Get returns a value, using loader if necessary.
func (c *CaffeineCache) Get(key string) (any, error) {
	c.mu.Lock()
	e, ok := c.entries[key]
	if ok && !c.isExpired(e) {
		c.stats.Hits++
		e.lastAccess = time.Now()
		c.promote(e)
		val := e.value
		c.mu.Unlock()
		return val, nil
	}
	if ok {
		// expired
		c.removeEntry(e, ReasonExpired)
	}
	c.stats.Misses++

	// no loader configured
	if c.loader == nil {
		c.mu.Unlock()
		return nil, errors.New("loader not configured and value missing")
	}

	// deduplicate loads
	wg, loading := c.loading[key]
	if !loading {
		wg = &sync.WaitGroup{}
		wg.Add(1)
		c.loading[key] = wg
		c.mu.Unlock()

		val, err := c.loader(key)

		c.mu.Lock()
		if err != nil {
			c.stats.LoadFailure++
			delete(c.loading, key)
			wg.Done()
			c.mu.Unlock()
			return nil, err
		}
		c.stats.LoadSuccess++
		c.putInternal(key, val)
		delete(c.loading, key)
		wg.Done()
		c.mu.Unlock()
		return val, nil
	}
	// wait for other loader
	c.mu.Unlock()
	wg.Wait()
	return c.Get(key)
}

// Put inserts a value.
func (c *CaffeineCache) Put(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putInternal(key, value)
}

func (c *CaffeineCache) putInternal(key string, value any) {
	if e, ok := c.entries[key]; ok {
		c.order.Remove(e.element)
	}
	e := &entry{
		key:        key,
		value:      value,
		createdAt:  time.Now(),
		lastAccess: time.Now(),
	}
	e.element = c.order.PushBack(e)
	c.entries[key] = e
	c.enforceSize()
	if c.refreshAfterWrite > 0 && c.loader != nil {
		go c.scheduleRefresh(e)
	}
}

func (c *CaffeineCache) scheduleRefresh(e *entry) {
	timer := time.NewTimer(c.refreshAfterWrite)
	<-timer.C
	c.mu.Lock()
	// ensure entry still exists
	if _, ok := c.entries[e.key]; !ok {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	val, err := c.loader(e.key)
	if err != nil {
		c.mu.Lock()
		c.stats.LoadFailure++
		c.mu.Unlock()
		return
	}

	c.mu.Lock()
	c.stats.LoadSuccess++
	c.putInternal(e.key, val)
	c.mu.Unlock()
}

func (c *CaffeineCache) isExpired(e *entry) bool {
	now := time.Now()
	if c.expireAfterWrite > 0 && now.Sub(e.createdAt) >= c.expireAfterWrite {
		return true
	}
	if c.expireAfterAccess > 0 && now.Sub(e.lastAccess) >= c.expireAfterAccess {
		return true
	}
	return false
}

func (c *CaffeineCache) promote(e *entry) {
	c.order.MoveToBack(e.element)
}

func (c *CaffeineCache) enforceSize() {
	for len(c.entries) > c.maxSize {
		front := c.order.Front()
		if front == nil {
			return
		}
		e := front.Value.(*entry)
		c.removeEntry(e, ReasonSize)
	}
}

func (c *CaffeineCache) removeEntry(e *entry, reason EvictionReason) {
	delete(c.entries, e.key)
	c.order.Remove(e.element)
	c.stats.Evictions++
	if c.evictionListener != nil {
		go c.evictionListener(e.key, e.value, reason)
	}
}

// Delete removes a key manually.
func (c *CaffeineCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		c.removeEntry(e, ReasonManual)
	}
}

// Stats returns current statistics.
func (c *CaffeineCache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}
