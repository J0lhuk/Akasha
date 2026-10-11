package cache

import (
	"sync"
	"testing"
)

func TestBoundsAndConcurrentEviction(t *testing.T) {
	c := New[string](10)
	c.Put("a", "a", 6)
	c.Put("b", "b", 6)
	if _, ok := c.Get("a"); ok {
		t.Fatal("old value retained")
	}
	c.Put("large", "large", 11)
	if _, ok := c.Get("large"); ok {
		t.Fatal("oversized entry")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				c.Put("c", "c", 2)
				c.Get("b")
			}
		})
	}
	wg.Wait()
	if s := c.Stats(); s.Bytes > 10 || s.Evictions == 0 {
		t.Fatal(s)
	}
	c.Clear()
	if c.Stats().Bytes != 0 {
		t.Fatal("clear")
	}
}
