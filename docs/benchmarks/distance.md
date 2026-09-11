# Distance Benchmarks

## Machine Spec

- CPU: Apple M3 Max (16 cores)
- OS: macOS (darwin/arm64)
- Compiler: Go 1.27.0 (`GOEXPERIMENT=simd`)

## Results

| Operation | Size | Implementation | Time/op | Ops/sec |
|:---|---:|:---|---:|---:|
| L2Squared | 128 | pure | 91.51 ns | 10,927,767 |
| L2Squared | 128 | simd-portable | 30.10 ns | 33,222,591 |
| L2Squared | 128 | simd-arch | 27.19 ns | 36,778,227 |
| L2Squared | 768 | pure | 698.00 ns | 1,432,665 |
| L2Squared | 768 | simd-portable | 189.60 ns | 5,274,262 |
| L2Squared | 768 | simd-arch | 187.40 ns | 5,336,179 |
| L2Squared | 1024 | pure | 938.60 ns | 1,065,417 |
| L2Squared | 1024 | simd-portable | 252.70 ns | 3,957,261 |
| L2Squared | 1024 | simd-arch | 251.20 ns | 3,980,892 |
| DotProduct | 128 | pure | 85.69 ns | 11,669,973 |
| DotProduct | 128 | simd-portable | 28.99 ns | 34,494,653 |
| DotProduct | 128 | simd-arch | 26.15 ns | 38,240,918 |
| DotProduct | 768 | pure | 662.60 ns | 1,509,206 |
| DotProduct | 768 | simd-portable | 182.80 ns | 5,470,459 |
| DotProduct | 768 | simd-arch | 180.20 ns | 5,549,389 |
| DotProduct | 1024 | pure | 892.20 ns | 1,120,825 |
| DotProduct | 1024 | simd-portable | 244.80 ns | 4,084,967 |
| DotProduct | 1024 | simd-arch | 241.20 ns | 4,145,937 |
| Cosine | 128 | pure | 89.65 ns | 11,154,489 |
| Cosine | 128 | simd-portable | 35.41 ns | 28,240,609 |
| Cosine | 128 | simd-arch | 31.84 ns | 31,407,035 |
| Cosine | 768 | pure | 672.60 ns | 1,486,768 |
| Cosine | 768 | simd-portable | 193.70 ns | 5,162,623 |
| Cosine | 768 | simd-arch | 191.60 ns | 5,219,207 |
| Cosine | 1024 | pure | 899.30 ns | 1,111,976 |
| Cosine | 1024 | simd-portable | 258.60 ns | 3,866,976 |
| Cosine | 1024 | simd-arch | 255.20 ns | 3,918,495 |
| NormalizeInPlace | 128 | pure | 152.40 ns | 6,561,679 |
| NormalizeInPlace | 128 | simd-portable | 61.43 ns | 16,278,691 |
| NormalizeInPlace | 128 | simd-arch | 49.51 ns | 20,197,939 |
| NormalizeInPlace | 768 | pure | 941.00 ns | 1,062,699 |
| NormalizeInPlace | 768 | simd-portable | 330.00 ns | 3,030,303 |
| NormalizeInPlace | 768 | simd-arch | 298.20 ns | 3,353,454 |
| NormalizeInPlace | 1024 | pure | 1247.00 ns | 801,925 |
| NormalizeInPlace | 1024 | simd-portable | 436.30 ns | 2,292,001 |
| NormalizeInPlace | 1024 | simd-arch | 400.80 ns | 2,495,010 |
