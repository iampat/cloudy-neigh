<p align="center">
  <img src="docs/logo/logo.png" alt="cloudy-neigh logo" width="300">
</p>

# cloudy-neigh
Cloudy with a Chance of Neighbors

A cloud-native search engine built on object storage.

## Overview

cloudy-neigh decouples compute from storage. The engine treats cloud object storage (AWS S3, Google Cloud Storage, or local disk) as the single source of truth. Stateless query and ingestion nodes use local NVMe SSD and RAM caches to serve low-latency search requests.

## Roadmap

See [ROADMAP.md](ROADMAP.md) for the phased milestones and feature roadmap.

## Documentation and Design Notes

- [Architecture Overview](docs/architecture.md): system topology, component layering, and end-to-end execution flows.
- [Storage Subsystem](docs/design/storage.md): write-ahead log streams, manifests, content-addressed segments, and branching KVFS on object storage.
- [Ingestion Subsystem](docs/design/ingestion.md): gRPC write endpoints, WAL sequencing, RecordIO framing, and flusher commit loops.
- [Query Subsystem](docs/design/query.md): manifest polling, flat memory table layout, SIMD kernel dispatch, and vector distance kernels.

## Go versions

The first `go_sdk.download` in `MODULE.bazel` is the default. Select the rest
as `bazel test --config=go1.27 //...`.

To bump a version:

1. `MODULE.bazel`: set the version on `go_sdk.download`.
2. `.bazelrc`: point the matching `build:go1.NN` config at it.
3. `.github/workflows/ci.yaml`: only when adding or dropping a minor version.
