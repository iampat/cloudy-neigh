<p align="center">
  <img src="docs/logo/logo.png" alt="cloudy-neigh logo" width="300">
</p>

# cloudy-neigh
Cloudy with a Chance of Neighbors

A cloud-native search engine.

## Go versions

The first `go_sdk.download` in `MODULE.bazel` is the default; the rest are
selectable as `bazel test --config=go1.27 //...`.

To bump a version:

1. `MODULE.bazel` — set the version on `go_sdk.download`.
2. `.bazelrc` — point the matching `build:go1.NN` config at it.
3. `.github/workflows/ci.yaml` — only when adding or dropping a minor version.
