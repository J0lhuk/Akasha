// Package cache provides a process-wide byte-bounded LRU for immutable derived
// objects. Scope and source revision must be included in every key.
package cache

import (
	"container/list"
	"sync"
)

type item[T any] struct {
	key   string
	value T
	bytes int64
}
type Stats struct {
	Entries   int    `json:"entries"`
	Bytes     int64  `json:"bytes"`
	Budget    int64  `json:"budget_bytes"`
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Evictions uint64 `json:"evictions"`
}
type Cache[T any] struct {
	mu    sync.Mutex
	items map[string]*list.Element
	order *list.List
	stats Stats
}

func New[T any](bytes int64) *Cache[T] {
	return &Cache[T]{items: map[string]*list.Element{}, order: list.New(), stats: Stats{Budget: max(0, bytes)}}
}
func (c *Cache[T]) Get(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.items[key]; e != nil {
		c.order.MoveToFront(e)
		c.stats.Hits++
		return e.Value.(item[T]).value, true
	}
	c.stats.Misses++
	var zero T
	return zero, false
}
func (c *Cache[T]) Put(key string, value T, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if bytes < 0 || bytes > c.stats.Budget {
		return
	}
	if e := c.items[key]; e != nil {
		c.remove(e)
	}
	for c.stats.Bytes+bytes > c.stats.Budget && c.order.Len() > 0 {
		c.remove(c.order.Back())
		c.stats.Evictions++
	}
	c.items[key] = c.order.PushFront(item[T]{key, value, bytes})
	c.stats.Bytes += bytes
	c.stats.Entries = len(c.items)
}
func (c *Cache[T]) remove(e *list.Element) {
	v := e.Value.(item[T])
	delete(c.items, v.key)
	c.stats.Bytes -= v.bytes
	c.order.Remove(e)
	c.stats.Entries = len(c.items)
}
func (c *Cache[T]) Stats() Stats { c.mu.Lock(); defer c.mu.Unlock(); return c.stats }
func (c *Cache[T]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = map[string]*list.Element{}
	c.order.Init()
	c.stats.Bytes = 0
	c.stats.Entries = 0
}
