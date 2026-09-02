# Codebase Simplification Plan

Todo tracker for simplifying cloudy-neigh, eliminating dead code, removing premature optimizations, and fixing concurrency hazards across three PRs.

## PR 1: Storage & Recordio Simplification (`ali/storage-simplification`)

- [x] Delete `objectstore/driver.go` and unexported `driver` interface.
- [x] Remove `client` wrapper struct in `objectstore/objectstore.go`.
- [x] Make `memStore`, `localStore`, and `gcsStore` directly implement `objectstore.Store`.
- [x] Replace `sync.RWMutex` with `sync.Mutex` in `memStore`.
- [x] Clone data slice in `memStore.Get` to prevent memory aliasing.
- [x] Delete `recordio/reader.go` (`recordio.Reader` duplicate of `recordio.Scanner`).
- [x] Delete `recordio.Writer.WriteRecordFrom` and remove 64 KB `copyBuf` pre-allocation.
- [x] Update build files and test suites.
- [x] Run `bazel test --config=race //...` and formatting tools.
- [x] Open/Update PR 1 and run `/petr` code review.
- [x] Address all review comments.

## PR 2: Logstream & xtime Cleanup (`ali/logstream-simplification`)

- [x] Delete `paceWinner`, `winStreak`, and artificial 20ms pacing sleeps in `logstream/log.go`.
- [x] Simplify `logstream.Read` to direct slice cloning. Delete `TestReadSharesBackingArray`.
- [x] Inline single-caller `probe` helper in `logstream/log.go`.
- [x] Delete package `internal/xtime`.
- [x] Inline context timer sleep in `cmd/walbench/main.go`.
- [x] Clean up single-caller math helpers and loop syntax in `cmd/walbench/main.go`.
- [x] Update build files and test suites.
- [x] Run `bazel test --config=race //...` and formatting tools.
- [x] Open PR 2 and run `/petr` code review.
- [x] Address all review comments.

## PR 3: KVFS Concurrency & Context Correctness (`ali/kvfs-simplification`)

- [ ] Delete `singleflight.Group` (`flushGroup`, `readGroup`) from `kvfs/store.go`.
- [ ] Pass caller `context.Context` through all I/O in `Flush` and `loadManifest`.
- [ ] Remove lease TTL cache and fix `Manifest` pointer aliasing race on reads.
- [ ] Delete unused `existsBlob` and `existsCAS` in `kvfs/cas.go`.
- [ ] Delete `kvfs/export_test.go` and test primitives via internal package tests or public `Store` API.
- [ ] Update build files and test suites.
- [ ] Run `bazel test --config=race //...` and formatting tools.
- [ ] Open PR 3 and run `/petr` code review.
- [ ] Address all review comments.
