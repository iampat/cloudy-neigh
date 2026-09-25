# Distance benchmarks

**Status:** 2026-09-24, branch `ali/querybench-server-time` [#157]

On one core, the AVX-512 kernels cut the per-call cost 3x to 20x against the
portable `simd` kernels. The 10K×1024 cosine scan is memory bound. It gains
2.2x to 2.5x with float32 rows, and 4.1x to 5.2x with AVX-512 16-bit rows. The design
sits in `docs/design/distance-variants.md`.

These runs used eight variants. Three remain, and the tables use the current
names:

- `simd` (avx512) was `avx512`. `simd` runs it on an AVX-512 CPU.
- `simd` (portable) was `simd`. `simd` runs it on a CPU without AVX-512.
- `fp16` was `avx512-fp16`.
- A row marked removed is a variant that no longer exists. bf16 rows are a
  roadmap item, see `ROADMAP.md`.

## Machines

| Name | CPU                          | GCE shape       | vCPUs | L2 per core |
| ---- | ---------------------------- | --------------- | ----- | ----------- |
| EMR  | Xeon Platinum 8581C, 2.1 GHz | `n4-standard-4` | 4     | 2 MiB       |
| GNR  | Xeon 6985P-C, 2.3 GHz        | `c4-highcpu-8`  | 8     | 2 MiB       |

EMR is Emerald Rapids and GNR is Granite Rapids. Both have AVX-512F, FMA,
VPCLMULQDQ, AVX512_BF16 and AVX512_FP16. Both run linux/amd64 under KVM.

## Method

- Cross-compile the test binaries on the Mac: `-c opt`, race off,
  `--platforms=@rules_go//go/toolchain:linux_amd64`, Go 1.27.1 with
  `GOEXPERIMENT=simd`.
- Copy them to each VM with the local `bench/run.sh`. Run the tests first.
- Pin one core: `GOMAXPROCS=1`, `-test.cpu=1`, `taskset -c <cpu>`.
- Run 7 samples per cell (`-test.count=7`). Report the median in ns/op.
- Search per row is the `BenchmarkSearch*_Table` time divided by the row count.
  The metric is COSINE and D=1024.
- `Search10K1024` streams 41 MB of float32 rows per query and runs from L3.
  `Search256x1024` stays in L2.
- recall@10 uses 2000 random unit vectors, D=64, 20 queries, against the exact
  float32 top 10.

The raw files are `bench/2026-09-24-kernels/results/00-baseline-*.txt` for the
baseline and `final2-*.txt` for the final numbers. `bench/` is not in git.

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
benchmark within 5%, except the 16-bit variants on GNR. GC and the handler
take at most 3% of the server CPU.

The final run used other VM shapes, because the first shapes had no capacity:
EMR on `n4-highmem-4` in us-east1-c, GNR on `c4-highcpu-16` in
us-central1-f. The Mac is the Apple M3 Max, not pinned. All three run
`GOMAXPROCS=1`. The VMs pin the server to one vCPU with an idle SMT sibling.

Each step adds one cost. The unit is ns per row, D=1024, 1M rows.

- Cached: `BenchmarkDistance/DotProduct` at D=1024. The data stays in L1.
- Streaming: `BenchmarkScanKernel/<variant>/1M`. The same kernel over a
  1M-row slice.
- Search: `BenchmarkScan1M_Table/<variant>` divided by 1M rows.
- Server: the server p50 of top10 with no filter, client on the same host.
- DRAM is streaming minus cached. Overhead is Search minus streaming. Rest is
  server minus Search.

| Machine | Variant                  | Cached | Streaming | Search | Server | DRAM  | Overhead | Rest  |
| ------- | ------------------------ | -----: | --------: | -----: | -----: | ----: | -------: | ----: |
| Mac     | `simd` (portable)        | 237.5  | 243.4     | 250.1  | 248.3  | 5.9   | 6.7      | -1.8  |
| EMR     | `simd` (avx512)          | 32.6   | 286.8     | 295.3  | 286.9  | 254.2 | 8.5      | -8.5  |
| EMR     | `fp16`                   | 41.5   | 200.8     | 196.4  | 198.6  | 159.3 | -4.4     | 2.2   |
| EMR     | `avx512-bf16p` (removed) | 36.8   | 211.2     | 213.4  | 217.4  | 174.4 | 2.2      | 4.0   |
| EMR     | `simd` (portable)        | 282.4  | 751.8     | 761.1  | 747.3  | 469.4 | 9.3      | -13.9 |
| GNR     | `simd` (avx512)          | 26.9   | 248.0     | 249.4  | 248.6  | 221.1 | 1.4      | -0.9  |
| GNR     | `fp16`                   | 30.0   | 115.5     | 154.2  | 184.7  | 85.5  | 38.7     | 30.5  |
| GNR     | `avx512-bf16p` (removed) | 29.1   | 177.4     | 168.2  | 205.6  | 148.3 | -9.2     | 37.3  |
| GNR     | `simd` (portable)        | 207.5  | 361.8     | 369.5  | 382.6  | 154.3 | 7.7      | 13.1  |

- On the VMs, DRAM is 89% of the `simd` (avx512) row cost. One core streams
  float32 rows at 14.3 GB/s (EMR) and 16.5 GB/s (GNR).
- The 16-bit rows cut the server time by 17% to 31% on the VMs, not by
  50%. Half the bytes do not give half the stall on one core.
- The Mac hides DRAM. Its portable kernel is compute bound at 237 ns per row.
- A slow kernel loses more to DRAM. EMR `simd` (portable) pays 469 ns per
  row for DRAM, and `simd` (avx512) pays 254.
- The five samples of each cell spread less than 2%. The negative steps and
  the GNR 16-bit rest of 31 to 37 ns are real. They follow the memory layout
  of each allocation, and the cause is not known.
- The server profiles put 97% to 100% of the CPU in `Table.Search`. The top
  entry is the vector load (`LoadFloat32x16`, `loadBF16Pair`) or
  `dotFP16Blocks`, which carries the DRAM stall.

`CONSIDER(ali):` Find why the GNR 16-bit server runs 20% to 22% slower than
`Scan1M`, while EMR shows no gap.

## End-to-end retrieval

The query server holds the 1M Cohere Wikipedia corpus: 1024-dim, COSINE, 700k
`en` docs and 100k each `de`, `es` and `fr`. Each run sends 300 queries after
20 warmup queries, one client, serial calls. The queries are the first 300
vectors of `en/0000.parquet`. The server uses one core as in the split.

Top10 with no filter, p50 in ms. Server is the `server-time-us` trailer.

| Machine | Variant                  | Server, same host | Total, same host | Server, Mac client | Total, Mac client |
| ------- | ------------------------ | ----------------: | ---------------: | -----------------: | ----------------: |
| Mac     | `simd` (portable)        | 248.3             | 256.3            | n/a                | n/a               |
| EMR     | `simd` (avx512)          | 286.9             | 299.3            | 282.1              | 374.3             |
| EMR     | `fp16`                   | 198.6             | 210.5            | 201.6              | 312.7             |
| EMR     | `avx512-bf16p` (removed) | 217.4             | 230.2            | 217.7              | 324.3             |
| EMR     | `simd` (portable)        | 747.3             | 760.1            | 697.2              | 959.8             |
| GNR     | `simd` (avx512)          | 248.6             | 257.8            | 290.2              | 354.2             |
| GNR     | `fp16`                   | 184.7             | 193.8            | 185.1              | 271.9             |
| GNR     | `avx512-bf16p` (removed) | 205.6             | 214.6            | 176.2              | 245.2             |
| GNR     | `simd` (portable)        | 382.6             | 391.5            | 388.7              | 474.4             |

- `fp16` is the fastest variant on both VMs.
- top100 adds 65 to 107 ms of client-server time. Each hit carries the full
  `text` attribute.
- `lang=en` adds 151 ms (Mac `simd`), 298 ms (GNR `simd`) and 458 ms (EMR
  `simd`). The filter calls `proto.Equal` on each row before the distance.
- The server time changes with the client location in three cells. With the
  Mac client, GNR `simd` (avx512) goes from 249 to 290 ms. GNR
  `avx512-bf16p` goes from 206 to 176 ms, and EMR `simd` (portable) from 747
  to 697 ms. The cause is not known.

Recall against the exact float32 top 100 over all 1M docs:

| Variant                      | top10, no filter | top10, `lang=en` | top100, no filter | Max score error |
| ---------------------------- | ---------------: | ---------------: | ----------------: | --------------: |
| `simd` (avx512 and portable) | 1.0000           | 1.0000           | 1.0000            | 1.5e-6          |
| `fp16`                       | 1.0000           | 1.0000           | 1.0000            | 1.4e-6          |
| `avx512-bf16p` (removed)     | 0.9987           | 0.9983           | 0.9986            | 3.2e-4          |
| `pure-bf16` (removed, Mac)   | 0.9987           | 0.9983           | 0.9986            | 3.2e-4          |

`fp16` returns the float32 results on real data. bf16 misses one
slot in 4 of 300 top-10 queries.

Load and cold start: `demoload` wrote 1M docs in 544 s to `file://` on the Mac
and in 736 s to GCS from GNR. The first sync of 1000 segments took 8 s on the
Mac. It took 62 to 112 s on GNR (same region) and 209 to 249 s on EMR (another
region).
Steady RSS is 7.0 GB for the 16-bit variants and 8 to 10 GB for float32 on the
VMs.

The raw files are in `bench/2026-09-25-final/`. `report.md` holds every
scenario, the profiles, and the anomalies.

## M3 Max, pre-change arm64 reference

Apple M3 Max, 16 cores, darwin/arm64, Go 1.27.0 with `GOEXPERIMENT=simd`.
These numbers predate PR #134, which removed the Neon kernels. The old Neon
column is gone. The 1M split holds the current Mac numbers. Unit: ns/op.

| Kernel | D    | pure  | simd, portable |
| ------ | ---- | ----- | -------------- |
| Dot    | 128  | 85.7  | 29.0           |
| Dot    | 1024 | 892.2 | 244.8          |
| L2     | 128  | 91.5  | 30.1           |
| L2     | 1024 | 938.6 | 252.7          |
| Cosine | 128  | 89.7  | 35.4           |
| Cosine | 1024 | 899.3 | 258.6          |
