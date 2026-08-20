<p align="center">
  <img src="docs/logo/logo.png" alt="cloudy-neigh logo" width="300">
</p>

# cloudy-neigh
Cloudy with a Chance of Neighbors

A cloud-native search engine.

## Packages

Every package sits at the repository root, so another module can import it.

- `github.com/iampat/cloudy-neigh/cas` — content-addressed blob storage. The
  content of a blob names it.
- `github.com/iampat/cloudy-neigh/index` — namespaces, and lookup of a document
  by identifier.
- `github.com/iampat/cloudy-neigh/server` — the gRPC service over `index`.

## Demo

Documents live in a content-addressed store, either in memory or on disk.
`ingest` walks a directory and writes one document per text file, keyed by the
path relative to that directory.

```sh
# Serve, and keep the data on disk.
bazel run //cmd/cloudy-neigh -- serve --store disk --dir /tmp/cn
```

In another terminal:

```sh
bazel run //cmd/cloudy-neigh -- ingest --namespace repo ./cmd
# ingested 7 documents, skipped 0
# verified cloudy-neigh/BUILD.bazel
# verified cloudy-neigh/client.go
# verified cloudy-neigh/ingest.go

# The identifier is the path relative to the directory that was ingested.
bazel run //cmd/cloudy-neigh -- query --namespace repo --id cloudy-neigh/serve.go
```

`ingest` skips a file that is not valid UTF-8, and a file over `--max-size`.

### Look at what it stored

Every document is a blob named by the SHA-256 of its content. One `root` file
names the manifest that maps an identifier to a digest.

```sh
cat /tmp/cn/root                                 # a digest
cat /tmp/cn/blobs/$(cat /tmp/cn/root)            # the manifest, as JSON
shasum -a 256 /tmp/cn/blobs/$(cat /tmp/cn/root)  # matches the file name
```

Stop the server, start it again on the same `--dir`, and the query still
answers. `docs/design/storage.md` states what survives a crash.

Pass `--store memory` to keep nothing.

## grpcurl

TODO

## Go versions

The first `go_sdk.download` in `MODULE.bazel` is the default. Select any other
with `bazel test --config=go1.27 //...`.

To bump a version:

1. `MODULE.bazel` — set the version on `go_sdk.download`.
2. `.bazelrc` — point the matching `build:go1.NN` config at it.
3. `.github/workflows/ci.yaml` — only when adding or dropping a minor version.
