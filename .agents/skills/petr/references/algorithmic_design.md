# Algorithmic Design, Invariants & Verification

This guide outlines principles for writing clean, explainable, and easily extendable algorithms, with rigorous verification techniques and anti-premature optimization safeguards.

---

## 1. Principles of Explainable Algorithm Design

1. **State the Invariant Explicitly:** Every loop or recursive step should maintain a simple, provable condition. If you cannot explain the invariant in one sentence, the design is too complex.
2. **Decompose Responsibilities:** Separate problem modeling (e.g. building an adjacency list) from the core algorithmic traversal (e.g. topological sort or Dijkstra).
3. **Extendability:** Design data structures so adding a new property (e.g., node weights, timestamps, colors) does not require rewriting the underlying traversal.
4. **Resist Premature Optimization:**
   * Write clean, self-documenting code first.
   * Do not introduce bitmasks, flat arrays, or zero-allocation memory pools unless profiling proves the clean implementation is a bottleneck.

---

## 2. Canonical Invariant Patterns

### A. Discrete Monotonic Binary Search
Binary search bugs almost always arise from ambiguous boundary conditions (`<=` vs `<`, `mid - 1` vs `mid`). Maintain a strict **half-open interval `[lo, hi)`**:

* **Invariant:** 
  * `lo - 1` is always known to evaluate to `false` (or does not satisfy the condition).
  * `hi` is always known to evaluate to `true` (or satisfies the condition).
* **Loop Termination:** Terminate when `lo == hi`. The answer is `lo`.

**Go Implementation (Readable & Standard):**
```go
// FindFirstTrue finds the smallest index in [0, n) where predicate(i) is true.
// Returns n if predicate is false for all elements in [0, n).
func FindFirstTrue(n int, predicate func(int) bool) int {
    lo, hi := 0, n
    for lo < hi {
        mid := lo + (hi-lo)/2
        if predicate(mid) {
            hi = mid // maintain invariant: predicate(hi) == true
        } else {
            lo = mid + 1 // maintain invariant: predicate(lo - 1) == false
        }
    }
    return lo
}
```

**Python Implementation:**
```python
from collections.abc import Callable

def find_first_true(n: int, predicate: Callable[[int], bool]) -> int:
    """Finds the smallest index in [0, n) satisfying predicate. Returns n if none."""
    lo, hi = 0, n
    while lo < hi:
        mid = lo + (hi - lo) // 2
        if predicate(mid):
            hi = mid
        else:
            lo = mid + 1
    return lo
```

---

## 3. Disjoint Set Union (DSU / Union-Find)

Clean, readable implementation with path compression and union by rank/size. Easy to explain, $O(\alpha(N))$ nearly linear time:

**Go:**
```go
type DSU struct {
    parent []int
    size   []int
}

func NewDSU(n int) *DSU {
    p := make([]int, n)
    s := make([]int, n)
    for i := range n {
        p[i] = i
        s[i] = 1
    }
    return &DSU{parent: p, size: s}
}

func (d *DSU) Find(i int) int {
    if d.parent[i] != i {
        d.parent[i] = d.Find(d.parent[i]) // path compression
    }
    return d.parent[i]
}

func (d *DSU) Union(i, j int) bool {
    rootI, rootJ := d.Find(i), d.Find(j)
    if rootI == rootJ {
        return false
    }
    // Union by size: attach smaller component to larger
    if d.size[rootI] < d.size[rootJ] {
        rootI, rootJ = rootJ, rootI
    }
    d.parent[rootJ] = rootI
    d.size[rootI] += d.size[rootJ]
    return true
}
```

---

## 4. Verification: Differential Testing Against an Oracle

When debugging or proving correctness, do not guess. Construct a simple, un-optimized $O(N^2)$ or brute-force oracle and test thousands of randomized small instances with a fixed seed.

### Python Stress-Test Harness
```python
import random

def solve_oracle(arr: list[int]) -> int:
    """Simple, obviously correct brute-force baseline."""
    res = 0
    for i in range(len(arr)):
        for j in range(i, len(arr)):
            res = max(res, sum(arr[i:j+1]))
    return res

def stress_test(iterations: int = 1000, seed: int = 42) -> None:
    rng = random.Random(seed)
    for test_idx in range(1, iterations + 1):
        n = rng.randint(1, 20)
        test_case = [rng.randint(-50, 50) for _ in range(n)]
        
        expected = solve_oracle(test_case)
        actual = solve_optimal(test_case)
        
        if expected != actual:
            print(f"FAILED on test #{test_idx}!")
            print(f"Input: {test_case}")
            print(f"Expected: {expected}, Got: {actual}")
            return
    print(f"All {iterations} randomized tests passed against oracle!")
```

---

## 5. Coding Interview Anti-Patterns

Avoid these common failure modes during technical interviews:

* **Over-Abstraction (The Design Pattern Trap):** Never write Strategy, Factory, or Visitor hierarchies for single-use logic. Write the simplest function or struct first. Introduce abstractions only when a second concrete requirement demands it.
* **Monolithic Code Dumps:** Do not implement all features at once. Progress in verifiable milestones: (1) clean working baseline, (2) synchronization and edge handling, (3) scale-up and optimization.
* **Patching Without Reproducing:** When an edge case fails, never guess or tweak boundary operators randomly. Write the minimal test isolating the broken invariant first, verify it fails, then apply the surgical fix.

