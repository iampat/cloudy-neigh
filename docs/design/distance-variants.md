# Distance variants

**Status:** Accepted, 2026-09-24 [#157]

This note answers one question: which distance code does the query server run,
and what must a change to it keep true? It covers the `-distance` flag, the row
storage per variant, cosine scoring, and the AVX-512 kernels. The numbers come
from `docs/benchmarks/distance.md`.

The query scan is memory bound. At 10,000 rows of D=1024 float32, one query
streams 41 MB from L3 at about 29 GB/s on one core. The AVX-512 dot kernel
costs 27 to 32 ns per row, and the scan costs 139 to 146 ns per row. A faster
kernel does not move the scan. Fewer bytes per row do.

## Variant config

`cloudy query -distance=<variant>` selects the row format and the kernel set.
The default is `simd`.

| Value  | Rows    | AVX-512F amd64           | Other amd64     | arm64           |
| ------ | ------- | ------------------------ | --------------- | --------------- |
| `pure` | float32 | scalar Go                | scalar Go       | scalar Go       |
| `simd` | float32 | `simd/archsimd`          | portable `simd` | portable `simd` |
| `fp16` | fp16    | Go assembly, `VCVTPH2PS` | startup error   | startup error   |

`vector.Variant` is an int enum. Its zero value is `pure`, so a zero `Table`
runs on every CPU. `ParseVariant` rejects an unknown name.

`simd` picks its kernel once per CPU. It runs the AVX-512 kernels when the CPU
has AVX-512F, and the portable `simd` package kernels otherwise. The portable
package picks its vector width the same way. `Kernels.Name` names the choice,
`avx512` or `portable`, and the startup log prints it in the `kernel` field.

`Variant.Kernels` returns an error when the CPU cannot run the variant.
`fp16` needs AVX-512F. `cloudy query` calls `Kernels` once at startup and exits
on error. The kernel
choice inside `simd` is the design. A variant never falls back to another
row format, because a silent change of format hides a recall and latency change
behind a healthy process.

`main` resolves the kernel set once and injects it. The loader never sees it.

```
main: ParseVariant, Variant.Kernels
└── query.NewEngine(store, interval, kernels)
    └── NewTable(kernels)          # one per branch
        └── Table.Clone            # keeps kernels
```

`Variant.Kernels` returns the kernel set: `Dot` and `L2` for float32 rows,
`Dot16`, `L216` and `Decode16` for fp16 rows. The table holds it and calls it
per row. The package functions `vector.DotProduct`, `L2Squared`, `Cosine` and
`NormalizeInPlace` do not read the variant. On amd64 they use AVX-512 when the
CPU has it.

## Row storage

`flatVectorCol` holds `data []float32` for a float32 variant and
`data16 []uint16` for `fp16`. The other slice stays nil. The table stores
16-bit rows when the kernel set has `Dot16`. `Upsert` encodes the
float32 input into `data16` with `vector.EncodeFP16`.

| Format    | Exponent, mantissa bits | Rounding           | Range limit |
| --------- | ----------------------- | ------------------ | ----------- |
| `Float32` | 8, 23                   | none               | float32     |
| `Float16` | 5, 10                   | nearest, ties even | 65504       |

fp16 has a small range. `EncodeFP16` returns `vector.ErrFloat16Overflow` for a
value that rounds past 65504, and for Inf and NaN. `Upsert` then fails with
that error. It encodes every input vector before the first change, so a
rejected upsert leaves the table unchanged.

The query stays float32. It is 4 KB and sits in L1. Only the row side widens.

`Get` and hit materialization decode the stored row with `Kernels.Decode16`,
so a caller sees the rounded values that the scan used. The decoder is
AVX-512 assembly. A scalar fp16 decoder cost about 25 us per query in the
256-row benchmark.

The fp16 rows halve the scan. On one core the 10K scan drops from 146 to
72 ns per row (EMR) and from 139 to 71 ns per row (GNR). The cost is
precision. On 2000 random unit vectors of D=64, recall@10 against the exact
float32 top 10 is 1.000 for fp16. The scalar fp16 encoder, decoder and dot
stay in the package as the reference for the fp16 kernel tests.

bf16 rows and int8 rows are roadmap items. The removed bf16 variants
measured recall@10 0.9987 on the Cohere corpus and ran no faster than fp16.

## Cosine scoring

A COSINE search does one dot product per row. `Upsert` stores
`invNorms[row] = 1 / |v|`, computed from the float32 input before any rounding.
`Search` computes the query inverse norm once.

```
if invNorms[row] == 0: skip row           # zero-norm row
sim   = dot(query, row) * invQ * invNorms[row]
sim   = clamp(sim, -1, 1)
score = 1 - sim
```

A zero query returns `vector.ErrZeroVector`. A zero-norm row is skipped under
COSINE and scored under L2 and DOT. `fp16` has no cosine kernel.
It uses `Dot16` and the same inverse norms.

The three-stream cosine kernel costs 1.9x (GNR) to 2.3x (EMR) the dot kernel
at D=1024. In the cache-resident 256-row benchmark, one dot per row cut the
per-row cost 1.26x to 1.27x on both VMs. The 10K scan did not move, because it
is memory bound. The cost is one float32 per row and a second small memory
stream.

## AVX-512 kernels

The float32 kernels in `distance_amd64.go` use `archsimd.Float32x16`. The
build tag is `goexperiment.simd && amd64`.

- Dot and L2 keep eight accumulators and consume 128 floats per iteration.
  Then a 64-float loop with four accumulators, then a 16-float loop.
- Cosine keeps six accumulators, two each for dot, |a|² and |b|². It consumes
  64 floats per iteration, so each accumulator gets two FMAs.
- A masked load (`LoadFloat32x16Part`) handles the tail.
- The reduction stays in registers: `GetLo`, `GetHi`, `Add`, then two
  `ConcatAddPairs`.
- The fp16 kernels are Go assembly in `distance_fp16_amd64.s`, because
  archsimd has no `VCVTPH2PS`. Go handles the tail past the last 16-element
  block.

Each loop advances by reslice, `a, b = a[128:], b[128:]`, and guards
`len(a)` and `len(b)`. This is the only loop shape that compiles without a
bounds check. An index loop kept 27 bounds checks in the dot kernel, because
the Go 1.27 prove pass does not carry `i+32 <= n` to each slice expression.
Without the `len(b)` guard the prover loses `len(a) == len(b)` and leaves five
checks.

More accumulators do not always help. A 3x16 cosine with nine accumulators
spilled one register per iteration and ran 4% slower on GNR. The register
budget, not the FMA latency, sets the unroll.

The kernels replace the portable `simd` kernels on AVX-512. At D=1024 the dot
kernel went from 283 to 32 ns on EMR and from 207 to 27 ns on GNR.

### Upper-state invariant

Every AVX-512 kernel calls `archsimd.ClearAVXUpperBits()` before it returns to
scalar code. This includes the decoders. The fp16 assembly ends with
`VZEROUPPER`.

The Go compiler never emits `VZEROUPPER`. Without it, the dirty ZMM upper state
makes the next legacy SSE instruction pay a state transition. The Go scalar
epilogue uses SSE encodings. The portable kernels paid about 180 ns per call
for this. The portable dot at D=128 cost 194 ns on EMR, and pure Go cost 86 ns.

Store the reduced vector to a stack array before the clear, and read the scalar
after it. `VZEROUPPER` clobbers every vector register, so a value kept in a
register across the call gets spilled or lost.

```go
s.ConcatAddPairs(s).ConcatAddPairs(s).Store(out[:])
archsimd.ClearAVXUpperBits()
return out[0]
```

## Open questions

On the 1M Cohere corpus, `fp16` returns the float32 results, with recall@10
1.0000. `docs/benchmarks/distance.md` has the full table.

`CONSIDER(ali):` Try int8 quantization. It halves the row stream again, to
about 10 MB per 10K×1024 query. It needs a per-row or per-dimension scale and
a recall measurement.
