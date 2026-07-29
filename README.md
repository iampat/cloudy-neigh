<p align="center">
  <img src="docs/logo/logo.png" alt="cloudy-neigh logo" width="300">
</p>

# cloudy-neigh
Cloudy with a Chance of Neighbors

A cloud-native search engine.

## Documentation & Design
- [Atomic Blob Store Design](docs/design/blob-cas-atomic-store.html)
- [Ingestion WAL System Design](docs/design/ingestion-wal-system.html)

## Modules
- [`ingestion`](ingestion/): Coordinator-free ingestion WAL engine supporting batching ($N$ docs, $T$ time, $B$ bytes), pluggable serialization drivers (`pb.jsonl`, `recordio`/`pb.bin`), zero-padded sequence discovery, conditional CAS writes (`WriteIfNotExist`), and structured logging (`log/slog`).

## Building & Testing
Run unit tests with Bazel:
```bash
bazel test //...
```

