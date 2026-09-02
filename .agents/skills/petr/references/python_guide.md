# Modern Python (up to 3.13.5) Algorithmic & Systems Guide

This reference guides algorithmic problem solving and data structure implementation in Python, adhering to the **Google Python Style Guide**, leveraging modern Python features (up to Python 3.13.5), and enforcing **profiling-driven optimization**.

---

## 1. Core Philosophy: Readability & Explainability First

1. **Clarity and Ease of Explanation:** Code should be structured so that its correctness and invariants are obvious. Prefer clean, standard Python constructs over clever micro-hacks.
2. **Resist Premature Optimization:** Do NOT sacrifice readability upfront by flattening 2D matrices into 1D arrays, packing bitmasks manually, or rewriting clean recursion into complex manual stacks unless asymptotic limits or profiling require it.
3. **Evidence-Based Optimization:** Always write the clean, self-documenting implementation first. If latency or memory exceeds bounds, profile before changing code.

---

## 2. Environment & Tooling: Astral & Virtual Envs

* **Always Use Virtual Environments:** Unless explicitly asked otherwise, always isolate Python execution in a virtual environment (`.venv`). Never install packages or run standalone scripts in the global environment.
* **Astral Tooling Standard:** Use Astral's toolchain as the default for Python workflows:
  * **Environment & Package Management:** Use `uv` (`uv venv`, `uv pip install`, `uv run`).
  * **Linting & Formatting:** Use `ruff` (`ruff check`, `ruff format`) for rapid PEP 8 enforcement and Google Python style consistency.

---

## 3. Google Python Style Guide Alignment

* **Formatting & Conventions:** Follow PEP 8 (4-space indentation, `snake_case` functions/variables, `PascalCase` classes, `UPPER_SNAKE` constants).
* **Explicit is Better than Implicit:** Avoid magic methods, dynamic attribute assignment, or dense nested comprehensions (limit comprehensions to simple transformations; use explicit loops when logic branches).
* **Modern Type Annotations:** Use standard built-in generics (`list[int]`, `dict[str, Node]`, `tuple[int, ...]`) without importing legacy types from `typing`.
* **Docstrings and Invariants:** Document the algorithmic premise, time/space complexity, and non-obvious invariants in concise Google-format docstrings.
* **Reference:** [Google Python Style Guide](https://google.github.io/styleguide/pyguide.html).

---

## 4. Modern Python Features (Python 3.12 – Python 3.13.5)

Python 3.12 and 3.13 introduce powerful typing and runtime improvements that enhance code clarity:

### A. PEP 695 Type Parameter Syntax (Python 3.12+)
Declare generics directly on classes, functions, and type aliases without importing `TypeVar` or `Generic`:
```python
from collections.abc import Iterable

# Generic function syntax
def find_minimum[T](items: Iterable[T]) -> T | None:
    iterator = iter(items)
    try:
        min_val = next(iterator)
    except StopIteration:
        return None
    for item in iterator:
        if item < min_val:
            min_val = item
    return min_val

# Clean generic class syntax
class TreeNode[T]:
    def __init__(self, val: T, left: 'TreeNode[T] | None' = None, right: 'TreeNode[T] | None' = None) -> None:
        self.val = val
        self.left = left
        self.right = right

# New 'type' statement for type aliases
type Graph[V] = dict[V, list[V]]
```

### B. `typing.TypeIs` for Bidirectional Type Narrowing (PEP 742, Python 3.13+)
Use `TypeIs` instead of `TypeGuard` when writing custom type-predicate guards; `TypeIs` correctly narrows types in both the `if` and `else` branches:
```python
from typing import TypeIs

def is_integer_node(node: TreeNode[object]) -> TypeIs[TreeNode[int]]:
    return isinstance(node.val, int)
```

### C. Python 3.13 Runtime Enhancements
* **Improved REPL & Tracebacks:** Multi-line editing and colorized tracebacks help fast debugging of complex algorithmic failures.
* **Experimental Free-Threaded Mode (PEP 703):** Optional no-GIL build (`--disable-gil`) allows true multi-core CPU parallelism with Python threads.
* **Preliminary JIT Compiler (PEP 744):** Tier 1/2 copy-and-patch JIT speeds up repetitive bytecode loops in CPU-heavy algorithms.
* **Python 3.13.5 Maintenance:** Stable maintenance release resolving generator expression type checking and bitwise handling in `random.getrandbits()`.

---

## 5. Profiling-Driven Performance Discipline

When an algorithm must handle large scale:

### Step 1: Write the Clean Baseline
Use standard, readable constructs (`dataclass`, standard recursion, descriptive names).

### Step 2: Profile CPU with `cProfile`
Never guess where time is spent. Run a quick profiling harness:
```python
import cProfile
import pstats

profiler = cProfile.Profile()
profiler.enable()

# Execute algorithm with representative load
result = solve(large_test_input)

profiler.disable()
stats = pstats.Stats(profiler).sort_stats('cumtime')
stats.print_stats(15)
```

### Step 3: Profile Memory with `tracemalloc`
If memory limit is a concern:
```python
import tracemalloc

tracemalloc.start()
result = solve(large_test_input)
current, peak = tracemalloc.get_traced_memory()
tracemalloc.stop()

print(f"Peak memory: {peak / (1024 * 1024):.2f} MB")
```

### Step 4: Targeted Optimization (Only When Backed by Profile)
* **Algorithmic Complexity First:** Check if $O(N^2)$ can become $O(N \log N)$ or $O(N)$.
* **Built-in C-Accelerated Modules:**
  * Use `collections.deque` instead of `list.pop(0)` ($O(1)$ vs $O(N)$).
  * Use `heapq` for priority queues (in-place array heap).
  * Use `bisect.bisect_left` / `bisect_right` for fast binary searches in sorted lists.
* **Fast I/O for Large Data:** If I/O is the bottleneck in competitive/bulk pipelines:
  ```python
  import sys
  tokens = sys.stdin.read().split()
  ```
* **Recursion Stack Safety:** If inputs risk exceeding recursion limits (`sys.setrecursionlimit` risks C-stack overflow), convert the recursive DFS to an explicit `while stack:` loop with minimal state tracking.
