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

- [Storage and Filesystem Engine](docs/design/storage.md): write-ahead log streams and branching KVFS on object storage.
- [LogStream](docs/design/wal.md): append-only log on object storage, with no coordination service.
- [gRPC API Contract](docs/design/grpc-api.md): service definition for writes, vector similarity, text search, and hybrid queries.
- [RecordIO Framing](docs/design/recordio.md): append-only record framing format with CRC32C integrity checks.

## Go versions

The first `go_sdk.download` in `MODULE.bazel` is the default. Select the rest
as `bazel test --config=go1.27 //...`.

To bump a version:

1. `MODULE.bazel`: set the version on `go_sdk.download`.
2. `.bazelrc`: point the matching `build:go1.NN` config at it.
3. `.github/workflows/ci.yaml`: only when adding or dropping a minor version.
