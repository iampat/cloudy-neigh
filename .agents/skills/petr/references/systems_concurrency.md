# In-Memory Systems & Concurrency Coding Guide

This reference guides the implementation of systems data structures and concurrency-safe primitives in Go (up to 1.26) and Python (up to 3.13.5), balancing **Google Style**, clear API contracts, and **anti-premature optimization**.

---

## 1. Core Principles for Systems Coding Interviews

1. **API Contract First:** Always define the public interface before writing internal synchronization or data layout.
2. **Defensive Concurrency:**
   * Clarify read vs. write frequencies.
   * Protect mutable state completely; do not leave "read-only" fields unprotected if they can be modified concurrently.
   * In Go, always run tests with `-race` (`go test -race`).
   * In Python, understand what operations are thread-safe and distinguish between standard GIL behavior and free-threaded CPython (Python 3.13).
3. **Back-of-the-Envelope Sanity Checks:**
   * **Memory Sizing:** Struct size + collection overhead $\times$ number of items.
     * *Example:* 10M keys in Go: struct (32B) + map overhead (~48B/entry) $\approx$ 800 MB (fits comfortably in RAM).
   * **Lock Contention Math:** If a critical section takes $2\,\mu\text{s}$, a single mutex can handle at most $1 / 2\,\mu\text{s} = 500{,}000$ ops/sec under zero queueing. If target QPS is $> 100{,}000$, introduce **lock striping/sharding**.
4. **Resist Premature Complexity:** Start with a clean `sync.Mutex` (Go) or `threading.Lock` (Python). Do not jump to lock-free CAS atomics or complex ring buffers unless profiling or scale targets require it.

---

## 2. Canonical Systems Implementations

### Pattern A: Thread-Safe LRU Cache

Combines an $O(1)$ Hash Map with a Doubly Linked List, protected by a clean lock.

#### Go (up to 1.26)
```go
package lru

import "sync"

type entry[K comparable, V any] struct {
	key   K
	val   V
	prev  *entry[K, V]
	next  *entry[K, V]
}

type LRUCache[K comparable, V any] struct {
	mu       sync.Mutex
	capacity int
	items    map[K]*entry[K, V]
	head     *entry[K, V] // dummy head (most recently used)
	tail     *entry[K, V] // dummy tail (least recently used)
}

func New[K comparable, V any](capacity int) *LRUCache[K, V] {
	head := &entry[K, V]{}
	tail := &entry[K, V]{}
	head.next = tail
	tail.prev = head
	return &LRUCache[K, V]{
		capacity: capacity,
		items:    make(map[K]*entry[K, V], capacity),
		head:     head,
		tail:     tail,
	}
}

func (c *LRUCache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	node, exists := c.items[key]
	if !exists {
		var zero V
		return zero, false
	}
	c.moveToHead(node)
	return node.val, true
}

func (c *LRUCache[K, V]) Put(key K, val V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if node, exists := c.items[key]; exists {
		node.val = val
		c.moveToHead(node)
		return
	}

	newNode := &entry[K, V]{key: key, val: val}
	c.items[key] = newNode
	c.addToHead(newNode)

	if len(c.items) > c.capacity {
		lru := c.tail.prev
		c.removeNode(lru)
		delete(c.items, lru.key)
	}
}

func (c *LRUCache[K, V]) addToHead(node *entry[K, V]) {
	node.prev = c.head
	node.next = c.head.next
	c.head.next.prev = node
	c.head.next = node
}

func (c *LRUCache[K, V]) removeNode(node *entry[K, V]) {
	node.prev.next = node.next
	node.next.prev = node.prev
}

func (c *LRUCache[K, V]) moveToHead(node *entry[K, V]) {
	c.removeNode(node)
	c.addToHead(node)
}
```

#### Python (up to 3.13.5)
```python
import threading
from typing import Generic, TypeVar

K = TypeVar("K")
V = TypeVar("V")

class _Node(Generic[K, V]):
    __slots__ = ("key", "val", "prev", "next")

    def __init__(self, key: K, val: V) -> None:
        self.key: K = key
        self.val: V = val
        self.prev: _Node[K, V] | None = None
        self.next: _Node[K, V] | None = None

class ThreadSafeLRUCache(Generic[K, V]):
    def __init__(self, capacity: int) -> None:
        if capacity <= 0:
            raise ValueError("Capacity must be positive")
        self._capacity = capacity
        self._items: dict[K, _Node[K, V]] = {}
        self._lock = threading.Lock()

        # Dummy sentinel nodes
        self._head: _Node[K, V] = _Node(None, None)  # type: ignore
        self._tail: _Node[K, V] = _Node(None, None)  # type: ignore
        self._head.next = self._tail
        self._tail.prev = self._head

    def get(self, key: K) -> V | None:
        with self._lock:
            node = self._items.get(key)
            if node is None:
                return None
            self._move_to_head(node)
            return node.val

    def put(self, key: K, val: V) -> None:
        with self._lock:
            if key in self._items:
                node = self._items[key]
                node.val = val
                self._move_to_head(node)
                return

            new_node = _Node(key, val)
            self._items[key] = new_node
            self._add_to_head(new_node)

            if len(self._items) > self._capacity:
                lru = self._tail.prev
                assert lru is not None and lru is not self._head
                self._remove_node(lru)
                del self._items[lru.key]

    def _add_to_head(self, node: _Node[K, V]) -> None:
        assert self._head.next is not None
        node.prev = self._head
        node.next = self._head.next
        self._head.next.prev = node
        self._head.next = node

    def _remove_node(self, node: _Node[K, V]) -> None:
        assert node.prev is not None and node.next is not None
        node.prev.next = node.next
        node.next.prev = node.prev

    def _move_to_head(self, node: _Node[K, V]) -> None:
        self._remove_node(node)
        self._add_to_head(node)
```

---

### Pattern B: Token Bucket Rate Limiter

Models high-performance rate limiting without background refill threads. Lazy refill calculated on access.

#### Go (up to 1.26)
```go
package ratelimit

import (
	"sync"
	"time"
)

type TokenBucket struct {
	mu           sync.Mutex
	rate         float64   // tokens added per second
	capacity     float64   // maximum burst tokens
	tokens       float64   // current available tokens
	lastRefillAt time.Time // timestamp of last token refill
}

func NewTokenBucket(rate, capacity float64) *TokenBucket {
	return &TokenBucket{
		rate:         rate,
		capacity:     capacity,
		tokens:       capacity,
		lastRefillAt: time.Now(),
	}
}

func (tb *TokenBucket) Allow() bool {
	return tb.AllowN(1)
}

func (tb *TokenBucket) AllowN(n float64) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefillAt).Seconds()
	tb.lastRefillAt = now

	// Lazy refill based on elapsed wall-clock time
	tb.tokens = min(tb.capacity, tb.tokens+elapsed*tb.rate)

	if tb.tokens >= n {
		tb.tokens -= n
		return true
	}
	return false
}
```

---

### Pattern C: Worker Pool with Bounded Queue & Graceful Shutdown

Controls concurrency, prevents memory exhaustion under burst traffic, and handles cleanup cleanly.

#### Go (up to 1.26)
```go
package workerpool

import (
	"context"
	"sync"
)

type Job func(ctx context.Context)

type Pool struct {
	jobs chan Job
	wg   sync.WaitGroup
}

func New(workerCount, queueCapacity int) *Pool {
	p := &Pool{
		jobs: make(chan Job, queueCapacity),
	}
	p.wg.Add(workerCount)
	for range workerCount {
		go func() {
			defer p.wg.Done()
			for job := range p.jobs {
				job(context.Background())
			}
		}()
	}
	return p
}

// Submit enqueues a job. Returns false if the pool is closed or queue is full.
func (p *Pool) Submit(job Job) bool {
	select {
	case p.jobs <- job:
		return true
	default:
		return false // Queue full: reject or apply backpressure
	}
}

// Shutdown drains the queue and waits for all workers to finish.
func (p *Pool) Shutdown() {
	close(p.jobs)
	p.wg.Wait()
}
```

---

## 3. High-QPS Optimization: Lock Striping / Sharding

When a single mutex becomes a contention bottleneck ($> 100\text{k}$ QPS across multiple cores):

```go
type ShardedMap[V any] struct {
	shards    []*shard[V]
	shardMask uint64
}

type shard[V any] struct {
	mu   sync.RWMutex
	data map[string]V
}

func NewShardedMap[V any](shardCount int) *ShardedMap[V] {
	// Ensure shardCount is a power of two for fast bitwise masking
	shards := make([]*shard[V], shardCount)
	for i := range shardCount {
		shards[i] = &shard[V]{data: make(map[string]V)}
	}
	return &ShardedMap[V]{
		shards:    shards,
		shardMask: uint64(shardCount - 1),
	}
}
```
