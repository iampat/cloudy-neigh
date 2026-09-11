**1. The `go1.27` build split gates nothing.** `distance_simd.go:1` sets `//go:build go1.27`, but the file holds no SIMD and no 1.27 feature. It is unrolled plain Go that compiles on every Go version. The split creates three copies of each function, plus `distance_fallback.go`, `export_test.go`, and `TestParity`. On the 1.26 SDK the parity test compares the pure code with itself, so it proves nothing there. Guideline: one implementation, never one per backend (`docs/guidelines/go.md:31`). Fix: delete `distance_fallback.go`, `distance_pure.go`, `export_test.go`, and `TestParity`. Drop the build tag and keep the unrolled file as the only implementation. Add the split when real SIMD code lands.

**2. Unreachable `dist < 0` guard.** `distance_pure.go:43-45` and `distance_simd.go:116-118`. The code clamps `sim` to at most 1 two lines earlier, so `dist = 1 - sim` is never negative. Delete both blocks.

**3. Unreachable `denom == 0` and `norm == 0` guards.** `distance_pure.go:33-35`, `distance_pure.go:58-60`, `distance_simd.go:106-108`, `distance_simd.go:128-130`. At each site the input sum is a nonzero `float32`, so it is at least 1.4e-45. `math.Sqrt` runs in float64, and the result converts back to a nonzero `float32` in every case. The zero-sum check above already catches the only zero state. Delete all four blocks.

**4. `Normalize` repeats the empty check and hand-rolls the clone.** `distance.go:43-53`. `NormalizeInPlace` returns the same `ErrEmptyVector`. Replace the body:

```go
func Normalize(v []float32) ([]float32, error) {
	out := slices.Clone(v)
	if err := NormalizeInPlace(out); err != nil {
		return nil, err
	}
	return out, nil
}
```

**5. Benchmark sink code is dead weight.** `distance_test.go:253-263`, `273-283`, `293-303`. `b.Loop` already keeps call results alive, so the compiler cannot remove the body. Delete `var total float32`, `total += res`, and the `if total == 0` blocks. The three benchmarks then differ only in the function under test. Fold them into one table-driven benchmark with named cases:

```go
func BenchmarkDistance(b *testing.B) {
	funcs := []struct {
		name string
		fn   func(a, b []float32) (float32, error)
	}{
		{"L2Squared", distance.L2Squared},
		{"DotProduct", distance.DotProduct},
		{"Cosine", distance.Cosine},
	}
	for _, f := range funcs {
		for _, dim := range []int{128, 768, 1024} {
			b.Run(f.name+"/"+strconv.Itoa(dim), func(b *testing.B) {
				v1 := randomVector(dim, 1)
				v2 := randomVector(dim, 2)
				for b.Loop() {
					if _, err := f.fn(v1, v2); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
```

**6. `ErrEmptyVector` rejects a state the zero value can carry.** `distance.go:10` and the four length checks. `L2Squared` and `DotProduct` of two empty vectors are 0, which is correct. `Cosine` and `Normalize` on empty input already fail with `ErrZeroVector` through the zero-sum check. Delete the sentinel and the length checks. The zero-values rule (`docs/guidelines/go.md:97`) says reject only a state the contract has no meaning for. Low severity, and it changes the API, so decide before callers exist.
