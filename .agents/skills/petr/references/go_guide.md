# Modern Go (up to 1.26) Algorithmic & Systems Guide

This reference guides the implementation of algorithms and data structures in Go, balancing **Google Go Style**, modern Go language features (up to Go 1.26), and **profiling-driven optimization**.

---

## 1. Core Philosophy: Readability & Maintainability First

1. **Clarity Over Cleverness:** Write code that is easy to explain in an interview or code review, simple to debug, and trivial to extend.
2. **Never Prematurely Optimize:** Do not start with complex micro-optimizations (custom memory arenas, bit-packing, manual pointer tricks) unless a concrete asymptotic bound or profiling measurement demands it.
3. **Evidence-Based Optimization:** Always establish a clean baseline first. If performance is suspect, profile before optimizing.

---

## 2. Google Go Style Guide Alignment

* **Formatting & Consistency:** Code must be formatted with `gofmt`.
* **Naming Conventions:**
  * Short, scoped names: Variable names should be short when their scope is small (e.g., `i`, `n`, `buf`), and more descriptive as scope grows.
  * Avoid repetition: A package named `tree` should export `Node`, not `TreeNode`.
  * Visibility: PascalCase for exported, camelCase for unexported.
* **Error Handling:**
  * Handle errors immediately; avoid deep nesting via early returns (guard clauses).
  * Wrap errors with contextual information using `fmt.Errorf("...: %w", err)`.
* **Package Design:** Keep APIs minimal, cohesive, and easy to test. Avoid package-level global mutable state.
* **Reference:** [Google Go Style Guide](https://google.github.io/styleguide/go/) and [Go Style Decisions](https://google.github.io/styleguide/go/decisions).

---

## 3. Modern Go Features (Go 1.22 – Go 1.26)

Keep implementations modern and idiomatic by utilizing features up to Go 1.26:

### A. Range Over Integers (Go 1.22+)
Replace clunky index loops when the index is just a counter:
```go
// Clean and idiomatic
for i := range n {
    process(i)
}
```

### B. Iterators and `range-over-func` (Go 1.23+)
Standardize collection traversals without exposing internal representation or allocating intermediate slices:
```go
import "iter"

type Tree[T any] struct {
    val   T
    left  *Tree[T]
    right *Tree[T]
}

// InOrder returns an iterator yielding values in-order.
func (t *Tree[T]) InOrder() iter.Seq[T] {
    return func(yield func(T) bool) {
        var walk func(n *Tree[T]) bool
        walk = func(n *Tree[T]) bool {
            if n == nil {
                return true
            }
            return walk(n.left) && yield(n.val) && walk(n.right)
        }
        walk(t)
    }
}

// Usage is clean, readable, and standard:
// for v := range tree.InOrder() { ... }
```

### C. Standard Collection Libraries (`slices`, `maps`) (Go 1.21–1.24+)
Avoid re-implementing sorting, binary searching, or map iteration:
```go
import (
    "maps"
    "slices"
)

// Sorting with custom comparator
slices.SortFunc(items, func(a, b Item) int {
    return cmp.Compare(a.Priority, b.Priority)
})

// Binary search on sorted slice
idx, found := slices.BinarySearch(sortedValues, target)

// Keys and Values iterators
for k := range maps.Keys(m) { ... }
```

### D. Benchmark Loop (`testing.B.Loop`) (Go 1.24+)
When benchmarking, replace the error-prone `for i := 0; i < b.N; i++` with `b.Loop()`:
```go
func BenchmarkAlgorithm(b *testing.B) {
    input := prepareInput()
    b.ResetTimer()
    for b.Loop() {
        RunAlgorithm(input)
    }
}
```

### E. Initialized `new` Expressions & Generics (Go 1.26)
* In Go 1.26, `new` accepts expression values directly: e.g., `p := new(int64(42))` instead of helper functions.
* Self-referential generics are supported directly in type parameter constraints.
* Runtime includes the Green Tea GC (lower latency) and Swiss Tables map implementation (faster lookup).

---

## 4. Profiling-Driven Performance Discipline

When an algorithm or data structure must scale:

1. **Step 1: Clean Baseline:** Implement the cleanest, most readable algorithm with standard structs and slices.
2. **Step 2: Benchmark:** Write a benchmark using `testing.B.Loop()` with realistic input sizes.
3. **Step 3: Profile:** Collect CPU and memory profiles:
   ```bash
   go test -bench=. -benchmem -cpuprofile=cpu.prof -memprofile=mem.prof
   go tool pprof -top -cum cpu.prof
   ```
4. **Step 4: Targeted Optimization:** Only optimize identified bottlenecks:
   * **Allocation Hotspots:** Pre-allocate slices (`make([]T, 0, cap)`), reuse buffers via `sync.Pool` or local scratch buffers.
   * **Pointer Chasing / GC Pressure:** If and only if profiling shows GC or cache misses as the bottleneck, flatten structures into indexed slices (`[]Node` with integer indices).
   * **I/O Bottlenecks:** Use `bufio.Reader` / `bufio.Writer` instead of unbuffered `fmt.Scan`.
