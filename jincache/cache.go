package jincache

import (
	"distributed-cache-demo/lru"
	"sync"
)

// 并发控制的cache
type cache struct {
	mu         sync.Mutex
	lru        *lru.Cache
	cacheBytes int64 // 最大缓存大小
}

func (c *cache) add(key string, value ByteView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru == nil {
		c.lru = lru.New(c.cacheBytes, nil)
	}
	c.lru.Add(key, value)
}

func (c *cache) get(key string) (value ByteView, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru == nil {
		return
	}

	if v, ok := c.lru.Get(key); ok {
		return v.(ByteView), ok
	}

	return
}

// Keys returns all keys in the cache
func (c *cache) Keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru == nil {
		return []string{}
	}
	return c.lru.Keys()
}

// Remove removes a key from the cache
func (c *cache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru != nil {
		c.lru.Remove(key)
	}
}
