# Distance and retrieval benchmarks

**Status:** 2026-10-01, branch `ali/e2e-bench-two-tier` [#171]

On one core, the AVX-512 kernels cut the per-call cost 3x to 20x against the
portable `simd` kernels. The 10K×1024 cosine scan is memory bound. It gains
2.2x to 2.5x with float32 rows, and 4.1x to 5.2x with AVX-512 16-bit rows. The design
sits in `docs/design/query.md`.

Three distance variants remain:

- `pure`: scalar Go implementation.
- `simd`: AVX-512 on an AVX-512 CPU, NEON on ARM64, and portable SIMD elsewhere.
- `fp16`: AVX-512 with float16 row storage and AVX-512 FP16 arithmetic.

## Machines

| Name | CPU                                                    | GCE shape       | vCPUs | Zone        |
| ---- | ------------------------------------------------------ | --------------- | ----- | ----------- |
| Mac  | Apple M3 Max                                           | local           | 16    | -           |
| EMR  | Xeon Platinum 8581C @ 2.30GHz (model 207)              | `c4-standard-8` | 8     | us-east1-c  |
| GNR  | Xeon 6985P-C @ 2.30GHz (model 173)                     | `c4-highcpu-8`  | 8     | us-central1 |

EMR is Emerald Rapids and GNR is Granite Rapids. Both have AVX-512F, FMA,
VPCLMULQDQ, AVX512_BF16, and AVX512_FP16. Both run linux/amd64 under KVM.

## Method

- Cross-compile test binaries on the Mac: `-c opt`, race off,
  `--platforms=@rules_go//go/toolchain:linux_amd64`, Go 1.27.1 with
  `GOEXPERIMENT=simd`.
- Run microbenchmarks hermetically via `task bench:microbench`. The task
  compiles, deploys, and verifies tests before running benchmarks.
- Pin one core: `GOMAXPROCS=1`, `-test.cpu=1`, `taskset -c <cpu>`.
- Run 7 samples per cell (`-test.count=7`). Report the median in ns/op.
- Search per row is the `BenchmarkSearch*_Table` time divided by the row count.
  The metric is COSINE and D=1024.
- `Search10K1024` streams 41 MB of float32 rows per query and runs from L3.
  `Search256x1024` stays in L2.
- recall@10 uses 2000 random unit vectors, D=64, 20 queries, against the exact
  float32 top 10.

The raw microbenchmark files live under `bench/<run>/<machine>/`. `bench/` is not in git.

## Two costs in the portable kernels

The portable `simd` package picks its vector width at startup. On amd64 it uses
512 bits only when `cpu.X86.HasAVX512VPCLMULQDQ` is set, and 256 bits only
with `HasVPCLMULQDQ` (Go 1.27.1 `src/simd/midway_amd64.go`). Otherwise it drops to 128 bits. The
Cascade Lake `n2-highmem-4` has AVX-512F and no VPCLMULQDQ, so its portable
kernels ran at 128 bits. EMR and GNR run them at 512 bits.

The portable kernels pay about 180 ns per call. The compiler never emits
`VZEROUPPER`, and the scalar epilogue in SSE encodings then pays an upper-state
transition. Portable Dot at D=128 costs 194 ns on EMR, and pure Go costs 89 ns.
The AVX-512 kernels call `archsimd.ClearAVXUpperBits()` and do not pay it.

## EMR results

Kernels, median ns/op. Ratio is portable over avx512 in the same run. The 00
baseline portable cells are within 1.2% of this column.

| Kernel | D    | pure  | simd, portable | simd, avx512 | ratio |
| ------ | ---- | ----- | -------------- | ------------ | ----- |
| Dot    | 128  | 88.5  | 194.6          | 10.0         | 19.5x |
| Dot    | 1024 | 693.2 | 281.8          | 32.2         | 8.8x  |
| Dot    | 4096 | 2836  | 594.2          | 111.0        | 5.4x  |
| L2     | 128  | 66.7  | 193.5          | 10.1         | 19.2x |
| L2     | 1024 | 689.4 | 286.8          | 33.8         | 8.5x  |
| L2     | 4096 | 2816  | 607.8          | 113.5        | 5.4x  |
| Cosine | 128  | 108.6 | 213.6          | 17.4         | 12.3x |
| Cosine | 1024 | 816.4 | 329.8          | 73.3         | 4.5x  |
| Cosine | 4096 | 3271  | 727.3          | 231.6        | 3.1x  |

Search per row, ns. The baseline is the 00 portable cosine scan at 369.8 ns per
row. The 00 run has no 256-row benchmark.

| Variant                  | 10K×1024 | 256×1024 | 10K vs baseline | recall@10 |
| ------------------------ | -------- | -------- | --------------- | --------- |
| `pure`                   | 714.8    | 803.8    | 0.52x           | 1.000     |
| `simd` (portable)        | 348.0    | 393.4    | 1.06x           | 1.000     |
| `simd` (avx512)          | 145.9    | 138.2    | 2.53x           | 1.000     |
| `pure-bf16` (removed)    | 759.5    | 842.3    | 0.49x           | 0.995     |
| `pure-fp16` (removed)    | 2941.7   | 2982.8   | 0.13x           | 1.000     |
| `avx512-bf16` (removed)  | 83.0     | 154.4    | 4.46x           | 0.995     |
| `avx512-bf16p` (removed) | 72.2     | 140.7    | 5.12x           | 0.995     |
| `fp16`                   | 71.7     | 144.6    | 5.16x           | 1.000     |

## GNR results

Kernels, median ns/op. Same columns as EMR.

| Kernel | D    | pure  | simd, portable | simd, avx512 | ratio |
| ------ | ---- | ----- | -------------- | ------------ | ----- |
| Dot    | 128  | 54.0  | 142.0          | 7.3          | 19.5x |
| Dot    | 1024 | 484.6 | 207.0          | 27.3         | 7.6x  |
| Dot    | 4096 | 1987  | 435.2          | 94.7         | 4.6x  |
| L2     | 128  | 55.0  | 141.1          | 7.5          | 18.7x |
| L2     | 1024 | 490.5 | 210.1          | 30.9         | 6.8x  |
| L2     | 4096 | 1994  | 445.9          | 112.8        | 4.0x  |
| Cosine | 128  | 78.6  | 157.6          | 12.5         | 12.6x |
| Cosine | 1024 | 566.6 | 242.7          | 52.2         | 4.6x  |
| Cosine | 4096 | 2259  | 534.8          | 178.0        | 3.0x  |

Search per row, ns. The baseline is 303.8 ns per row.

| Variant                  | 10K×1024 | 256×1024 | 10K vs baseline | recall@10 |
| ------------------------ | -------- | -------- | --------------- | --------- |
| `pure`                   | 534.5    | 582.8    | 0.57x           | 1.000     |
| `simd` (portable)        | 289.4    | 289.7    | 1.05x           | 1.000     |
| `simd` (avx512)          | 138.7    | 103.7    | 2.19x           | 1.000     |
| `pure-bf16` (removed)    | 503.7    | 596.9    | 0.60x           | 0.995     |
| `pure-fp16` (removed)    | 2005.4   | 2081.3   | 0.15x           | 1.000     |
| `avx512-bf16` (removed)  | 73.7     | 112.6    | 4.12x           | 0.995     |
| `avx512-bf16p` (removed) | 70.7     | 106.0    | 4.30x           | 0.995     |
| `fp16`                   | 70.5     | 102.5    | 4.31x           | 1.000     |

## Reading the Search tables

- The 10K scan is memory bound on both VMs. `simd` (avx512) runs the dot
  kernel at 27 to 32 ns per row, but the scan costs 139 to 146 ns per row.
- The 16-bit rows halve the stream to about 20 MB per query. `fp16` and
  `avx512-bf16p` tie, within 1%, as the fastest variants on both VMs.
- In the 256-row cell, `simd` (avx512) and `fp16` tie. About half of that cell
  is the fixed cost to materialize ten hits.
- The 00 baseline scan ran the portable cosine kernel, three FMA streams per
  row. The current scan does one dot product per row with stored inverse norms.
  Thus `simd` (portable) beats the baseline by 5% to 6% with the same kernel
  family.

## 1M scan split

The 1M scan is DRAM bound on both VMs. The server time matches the 1M `Search`
benchmark within 5%. GC and the handler take at most 3% of the server CPU.

The benchmark runs on Mac (Apple M3 Max) and EMR (`c4-standard-8` in us-east1-c).
All runs set `GOMAXPROCS=1`. The VM pins the server to one vCPU with an idle SMT sibling.

Each step adds one cost. The unit is ns per row, D=1024, 1M rows.

- Cached: `BenchmarkDistance/DotProduct` at D=1024. The data stays in L1.
- Streaming: `BenchmarkScanKernel/<variant>/1M`. The same kernel over a 1M-row slice.
- Scan1M: `BenchmarkScan1M_Table/<variant>` divided by 1M rows.
- Server p50 and p99: top10-all server time in ms, which equals ns per row.
- DRAM is streaming minus cached. Overhead is Scan1M minus streaming. Rest is
  Server minus Scan1M.

| Machine | Variant | Cached | Stream | Scan1M | Server p50 | Server p99 | DRAM | Overhead | Server - Scan1M |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Mac | `pure` | 895.8 | 900.7 | 919.0 | 901.0 | 945.0 | 4.9 | 18.3 | -18.0 |
| Mac | `simd` | 235.6 | 245.9 | 250.7 | 249.5 | 256.2 | 10.3 | 4.8 | -1.1 |
| EMR | `pure` | 484.4 | 1241.0 | 1374.9 | 1333.2 | 1447.2 | 756.6 | 133.9 | -41.7 |
| EMR | `simd` | 26.5 | 254.3 | 250.0 | 253.5 | 260.7 | 227.8 | -4.3 | 3.5 |
| EMR | `fp16` | 30.0 | 116.4 | 157.6 | 155.5 | 161.0 | 86.4 | 41.2 | -2.1 |

- On the VM, DRAM is 90% of the `simd` row cost. One core streams float32 rows at 16.1 GB/s.
- `fp16` cuts the server time by 39% on the VM.
- The Mac hides DRAM. Its SIMD kernel is compute bound at 236 ns per row.
- `fp16` achieves 155.5 ns per row on EMR, outperforming all float32 variants.

## End-to-end retrieval

The suite executes in two tiers.

### Tier 1: Zero-Network Latency Benchmark (1M Documents)

Client and server execute on the same host over `localhost:50052`.
The corpus holds 1M Cohere Wikipedia documents (1024-dim, COSINE, 700k `en`, 100k each `de`, `es`, `fr`).
Each run executes 300 queries after 20 warmup queries with serial calls.
The server runs on one core with `GOMAXPROCS=1`.

Top10 with no filter, values in milliseconds:

| Machine & Backend | Variant | Total p50 | Total p99 | Server p50 | Server p99 | C-S p50 | C-S p99 | Recall@k |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| emr-gcp | `fp16` | 161.8 | 171.2 | 153.1 | 162.3 | 8.8 | 9.1 | 1.0000 |
| emr-gcp | `simd` | 260.4 | 264.0 | 251.5 | 255.0 | 8.9 | 9.2 | 1.0000 |
| emr-gcp | `pure` | 1331.3 | 1491.0 | 1321.9 | 1472.3 | 9.3 | 17.5 | 1.0000 |
| emr-local | `fp16` | 164.6 | 170.0 | 155.5 | 161.0 | 9.1 | 9.5 | 1.0000 |
| emr-local | `simd` | 262.7 | 270.0 | 253.5 | 260.7 | 9.1 | 9.7 | 1.0000 |
| emr-local | `pure` | 1342.8 | 1456.6 | 1333.2 | 1447.2 | 9.3 | 17.8 | 1.0000 |
| mac-gcp | `simd` | 255.9 | 262.2 | 248.2 | 254.2 | 7.8 | 8.2 | 1.0000 |
| mac-gcp | `pure` | 912.3 | 967.8 | 904.2 | 960.0 | 7.9 | 10.3 | 1.0000 |
| mac-local | `simd` | 257.4 | 264.3 | 249.5 | 256.2 | 7.8 | 8.2 | 1.0000 |
| mac-local | `pure` | 909.0 | 952.9 | 901.0 | 945.0 | 8.0 | 12.9 | 1.0000 |

- `fp16` on AVX-512 is the fastest variant at 153.1 ms server p50 and 162.3 ms server p99.
- SIMD on Mac (NEON) and EMR (AVX-512) match closely: 248.2 ms vs 251.5 ms server p50.
- Client-server localhost overhead stays under 10 ms for top10 queries.
- top100 increases client-server time by ~65 to 85 ms due to hit payload serialization.
- `lang=en` adds ~150 ms on Mac and ~280 to 310 ms on EMR due to proto equality filtering.

Recall against exact float32 ground truth across all 1M documents:

| Variant | top10, no filter | top10, `lang=en` | top100, no filter | top100, `lang=en` | Max score error |
| --- | ---: | ---: | ---: | ---: | ---: |
| `pure` | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 0.0 |
| `simd` | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 1.5e-6 |
| `fp16` | 1.0000 | 1.0000 | 1.0000 | 1.0000 | 2.0e-6 |

All variants achieve perfect `1.0000` recall on the 1M document Cohere corpus.

### Tier 2: Format Compatibility Sanity Check

The sanity check verifies binary segment and manifest compatibility between ARM64 and x86_64 across GCS.

| Check | Ingest Host | Query Server | Client | Segments | Queries | Status |
| --- | --- | --- | --- | ---: | ---: | --- |
| Mac -> VM | Mac (ARM64) | VM (x86_64) | Mac | 100 | 32 | **PASS** |
| VM -> Mac | VM (x86_64) | Mac (ARM64) | Mac | 100 | 32 | **PASS** |

### Load and Cold Start

- Demoload wrote 1M documents (1000 segments, 8.7 GiB) in 629 s.
- Mac local disk cold start takes 10.8 s (first sync: 10.0 s).
- EMR local disk cold start takes 17.1 s (first sync: 16.6 s).
- GCS cold start takes 224.8 s (EMR `fp16`) to 306.7 s (Mac `pure`) across regions.
- Steady query RSS is 7.1 to 7.2 GiB for `fp16`, and 10.4 to 16.7 GiB for float32.

The raw files live in `bench/2026-09-29-e2e/`. `report.md` holds all scenario splits and profiles.

## M3 Max, pre-change arm64 reference

Apple M3 Max, 16 cores, darwin/arm64, Go 1.27.0 with `GOEXPERIMENT=simd`.
These numbers predate PR #134, which removed the assembly Neon kernels. The 1M split holds current Mac numbers. Unit: ns/op.

| Kernel | D | pure | simd, portable |
| --- | --- | ---: | ---: |
| Dot | 128 | 85.7 | 29.0 |
| Dot | 1024 | 892.2 | 244.8 |
| L2 | 128 | 91.5 | 30.1 |
| L2 | 1024 | 938.6 | 252.7 |
| Cosine | 128 | 89.7 | 35.4 |
| Cosine | 1024 | 899.3 | 258.6 |
