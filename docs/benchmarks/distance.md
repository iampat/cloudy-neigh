# Distance Benchmarks

## Machine Spec

- CPU: Apple M3 Max (16 cores)
- OS: macOS (darwin/arm64)
- Compiler: Go 1.27.0 (`GOEXPERIMENT=simd`)

## Results

| Operation | Size | Pure Time/op | Pure Ops/sec | Portable Time/op | Portable Ops/sec | Arch Time/op | Arch Ops/sec |
|:---|---:|---:|---:|---:|---:|---:|---:|
| L2Squared | 128 | 91.51 ns | 10,927,767 | 30.10 ns (67% faster) | 33,222,591 (+204%) | 27.19 ns (70% faster) | 36,778,227 (+237%) |
| L2Squared | 768 | 698.00 ns | 1,432,665 | 189.60 ns (73% faster) | 5,274,262 (+268%) | 187.40 ns (73% faster) | 5,336,179 (+272%) |
| L2Squared | 1024 | 938.60 ns | 1,065,417 | 252.70 ns (73% faster) | 3,957,261 (+271%) | 251.20 ns (73% faster) | 3,980,892 (+274%) |
| DotProduct | 128 | 85.69 ns | 11,669,973 | 28.99 ns (66% faster) | 34,494,653 (+196%) | 26.15 ns (69% faster) | 38,240,918 (+228%) |
| DotProduct | 768 | 662.60 ns | 1,509,206 | 182.80 ns (72% faster) | 5,470,459 (+262%) | 180.20 ns (73% faster) | 5,549,389 (+268%) |
| DotProduct | 1024 | 892.20 ns | 1,120,825 | 244.80 ns (73% faster) | 4,084,967 (+264%) | 241.20 ns (73% faster) | 4,145,937 (+270%) |
| Cosine | 128 | 89.65 ns | 11,154,489 | 35.41 ns (61% faster) | 28,240,609 (+153%) | 31.84 ns (64% faster) | 31,407,035 (+182%) |
| Cosine | 768 | 672.60 ns | 1,486,768 | 193.70 ns (71% faster) | 5,162,623 (+247%) | 191.60 ns (72% faster) | 5,219,207 (+251%) |
| Cosine | 1024 | 899.30 ns | 1,111,976 | 258.60 ns (71% faster) | 3,866,976 (+248%) | 255.20 ns (72% faster) | 3,918,495 (+252%) |
| NormalizeInPlace | 128 | 152.40 ns | 6,561,679 | 61.43 ns (60% faster) | 16,278,691 (+148%) | 49.51 ns (68% faster) | 20,197,939 (+208%) |
| NormalizeInPlace | 768 | 941.00 ns | 1,062,699 | 330.00 ns (65% faster) | 3,030,303 (+185%) | 298.20 ns (68% faster) | 3,353,454 (+216%) |
| NormalizeInPlace | 1024 | 1247.00 ns | 801,925 | 436.30 ns (65% faster) | 2,292,001 (+186%) | 400.80 ns (68% faster) | 2,495,010 (+211%) |
