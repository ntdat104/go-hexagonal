package caffeine

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCachePutGet(t *testing.T) {
	cache := NewCache(CaffeineCacheConfig{
		MaxSize: 10,
	})

	cache.Put("a", 1)
	v, err := cache.Get("a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.(int) != 1 {
		t.Fatalf("expected 1, got %v", v)
	}
}

func TestCacheExpireAfterWrite(t *testing.T) {
	cache := NewCache(CaffeineCacheConfig{
		MaxSize:          10,
		ExpireAfterWrite: 50 * time.Millisecond,
	})

	cache.Put("a", 1)
	time.Sleep(60 * time.Millisecond)
	_, err := cache.Get("a")
	if err == nil {
		t.Fatalf("expected error for expired entry")
	}
}

func TestCacheExpireAfterAccess(t *testing.T) {
	cache := NewCache(CaffeineCacheConfig{
		MaxSize:           10,
		ExpireAfterAccess: 50 * time.Millisecond,
	})

	cache.Put("a", 1)
	time.Sleep(30 * time.Millisecond)
	_, _ = cache.Get("a") // touch to reset access
	time.Sleep(30 * time.Millisecond)
	if _, err := cache.Get("a"); err != nil {
		t.Fatalf("entry should still be valid")
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := cache.Get("a"); err == nil {
		t.Fatalf("expected expired entry")
	}
}

func TestCacheEvictionBySize(t *testing.T) {
	cache := NewCache(CaffeineCacheConfig{
		MaxSize: 2,
	})

	cache.Put("a", 1)
	cache.Put("b", 2)
	cache.Put("c", 3) // should evict "a"

	if _, err := cache.Get("a"); err == nil {
		t.Fatalf("expected 'a' to be evicted")
	}
	if _, err := cache.Get("c"); err != nil {
		t.Fatalf("expected 'c' to exist")
	}
}

func TestCacheLoader(t *testing.T) {
	loaderCalls := 0
	cache := NewCache(CaffeineCacheConfig{
		MaxSize: 2,
		Loader: func(key string) (any, error) {
			loaderCalls++
			if key == "x" {
				return 42, nil
			}
			return nil, errors.New("not found")
		},
	})

	v, err := cache.Get("x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.(int) != 42 {
		t.Fatalf("expected 42, got %v", v)
	}
	if loaderCalls != 1 {
		t.Fatalf("expected loader to be called once, got %d", loaderCalls)
	}

	// Should hit cache, not call loader again
	_, _ = cache.Get("x")
	if loaderCalls != 1 {
		t.Fatalf("expected loader call count to stay 1, got %d", loaderCalls)
	}
}

func TestCacheLoaderDeduplication(t *testing.T) {
	var mu sync.Mutex
	loaderCalls := 0
	cache := NewCache(CaffeineCacheConfig{
		MaxSize: 2,
		Loader: func(key string) (any, error) {
			mu.Lock()
			loaderCalls++
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			return 99, nil
		},
	})

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cache.Get("dup")
		}()
	}
	wg.Wait()

	if loaderCalls != 1 {
		t.Fatalf("expected loader to be called once, got %d", loaderCalls)
	}
}

func TestEvictionListener(t *testing.T) {
	evicted := make(chan string, 1)
	cache := NewCache(CaffeineCacheConfig{
		MaxSize: 1,
		EvictionListener: func(key string, value any, reason EvictionReason) {
			evicted <- key
		},
	})

	cache.Put("a", 1)
	cache.Put("b", 2)

	select {
	case key := <-evicted:
		if key != "a" {
			t.Fatalf("expected 'a' to be evicted, got %s", key)
		}
	case <-time.After(time.Second):
		t.Fatalf("eviction listener not called")
	}
}

func TestStats(t *testing.T) {
	cache := NewCache(CaffeineCacheConfig{
		MaxSize: 1,
		Loader: func(key string) (any, error) {
			return 123, nil
		},
	})

	_, _ = cache.Get("a") // miss + load
	_, _ = cache.Get("a") // hit

	stats := cache.Stats()
	if stats.Hits != 1 || stats.Misses != 1 || stats.LoadSuccess != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}
