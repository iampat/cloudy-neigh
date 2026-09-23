# TODO

- [X] Unify storage layout and replace `kvfs` with lightweight `manifest` package. Flattened segments under `segments/` and cataloged active branches in `branches.json`. [#142]
- [X] Simplify query engine branch discovery and synchronization. Load branches from `branches.json` and remove `branchState` wrapper. [#142]
- [X] Clean up server CLI flags. Renamed `-listen` to `-addr` and `-url` to `-storage-root`. Internalized stream name and message size. [#141]
- [X] Audit codebase for instances of premature optimization. Always compare with a vanilla baseline first. [#133, #134]
  - Example: `Table` switched from chunked copy-on-write slices to a flat contiguous `[]float32` array. PR 134 eliminated dead SIMD intrinsics, QueryExecutor, Table mutex, and Loader buffering. [#133, #134]
- [X] Eliminate server-side batch buffering in Ingester. Replaced BatchIngester actor goroutine and channels with direct synchronous WAL Append. [#140]
- [X] Add Fork RPC to IngestService with namespace scoping and WAL event sequencing. [#138]
- [ ] Support capturing unflushed parent mutations before Fork manifest creation. Sequence the fork event in the flusher, flush active parent mutations to a segment, and create the child manifest at that exact sequence boundary.
- [ ] Background compaction worker to merge flat immutable segments across branches and purge tombstoned rows.
- [ ] Move multi-tenant log stream routing into the `ingest` library. `cmd/cloudy` acts strictly as an assembly root using dependency injection, without hardcoded stream names or paths.
- [ ] Garbage collection worker to prune unreferenced flat segments and dead branch manifests.
- [ ] Expose branch deletion RPC in IngestService to remove branch pointers and update `branches.json`.
- [ ] Refactor the storage layer. [#47]
  - [ ] Replace the cloud SDK with a shim around GCS. Use the atomic-file
        package from Tailscale. [#47]
  - [X] Revisit the ObjectStore API and `Head` probing in LogStream. [#50, #74]
        Napkin math shows 1 `List` RTT (~35 ms) beats 6 sequential `Head` RTTs (~90 ms) on collisions (delta=5).
        Cost difference is negligible ($5.00 vs $2.40 per 1M collisions, and $0.000005 per cold start).
        Parked until high collision QPS or list pricing becomes an operational bottleneck.
  - [ ] Drop the generation token from a conditional create, which no caller reads. [#85]
- [X] Settle the fileblob conditional write. `gocloud.dev/blob/fileblob` was dropped and
      replaced by direct `localDriver` using POSIX `os.Link(2)` for atomic `Absent: true`
      and `flock(2)` + `rename(2)` for generation-matched updates. [#44, #71]
- [ ] Publish the benchmarks, the code coverage, and the build health on the
      front page of the repository. A number nobody sees changes no decision. [#58, #64]
  - [ ] Show the state of the build and the tests as a badge in `README.md`.
  - [ ] Measure the code coverage in CI, publish it, and show it as a badge.
        `bazel coverage //...` produces the data today.
  - [ ] Publish the benchmark results. `ci.yaml` already runs them with
        `-test.bench=.`, and the numbers reach the log and stop there. Keep a
        history, so a change that costs latency shows as a step in a chart.
  - [ ] Name the numbers that matter on the front page: the append rate of
        LogStream, and the read latency of the object store.
- [ ] Validate the protocols with a formal method. A test finds the
      interleaving it runs. A model checker finds the one nobody thought of,
      and that is where a storage protocol fails. [#58]
  - [ ] Choose the tool. TLA+ with PlusCal, or Alloy. Model one protocol in
        both before the choice binds the rest.
  - [ ] Model the LogStream append protocol from `docs/design/wal.md`. Check
        the contiguity invariant, that the live sequence numbers form `1..T`
        with no hole, against many writers and a lost acknowledgement.
  - [ ] Model the manifest and branch protocol from `docs/design/storage.md`.
        Check that a conditional write linearizes every mutation.
  - [ ] State where the model and the code can drift apart. A proof binds the
        model, not the Go.
- [ ] The `quality` workflow is slow. Most of the time goes to a build of
      golangci-lint. The step runs `go run <tool>@<version>` through
      `bazel run @rules_go//go`, so it compiles the tool from source
      on every run. `setup-bazel` caches the Bazel disk cache and the
      repository cache. Neither one holds the Go build cache that `go run`
      writes, so no Bazel action covers this work. `govulncheck` has the same
      shape. Measure the step, find where the `go` tool puts `GOCACHE`, then
      cache that directory or make the tool a Bazel target. [#60]
- [ ] Restore `govulncheck` in the `quality` workflow.
      The check was removed because `govulncheck ./...` cannot import generated
      protobuf packages from `bazel-bin`. Write a Bazel rule to fix this issue [#27, #130]
- [ ] Add fuzz tests. A table test covers the cases we thought of. A fuzzer
      finds the frame that no case names. [#61]
  - [ ] Fuzz the RecordIO reader and the scanner with arbitrary bytes. Every
        input must give a record or a named error, and never a panic.
  - [ ] Fuzz a RecordIO write and read round trip. The records must come back
        byte for byte.
  - [ ] Fuzz the LogStream key parse and the stream name check. Both read
        untrusted text, and both need an internal test to reach.
  - [ ] Run a long fuzz job in CI and cache the corpus. `bazel test` runs the
        seed corpus alone today.
  - [ ] Support running all fuzz targets under `--config=fuzz` and `--config=race`.
        `bazel test //... --config=fuzz` fails because `-test.fuzz=.` matches
        multiple fuzz functions in one package. Find a fix to run all fuzz targets
        and enable libfuzzer coverage instrumentation in Bazel. [#82]
- [ ] Test against a mock object store. `logstream.New` takes a concrete
      `*objectstore.Store`, so no test can inject a failure today. [#61]
  - [ ] Let the caller declare the interface it needs, which
        `docs/guidelines/go.md` requires. Take that interface in `New`.
  - [ ] Build a mock that injects an error, a delay, a lost acknowledgement,
        and a precondition failure.
  - [ ] Cover the drift recovery with the mock. A test then
        counts the probes instead of guessing them.
  - [ ] Keep one test per real backend for the contract. A mock proves the
        logic, and a backend proves the assumption.
- [X] Count the append attempts inside LogStream. Debug logs record collisions,
      jump probes, uploads, and elapsed time. `walbench -debuglog` writes these
      records to measure write amplification. [#61]
- [ ] Remove the `golang.org/x/tools` override in `MODULE.bazel`. rules_go
      0.63.0 still pins v0.34.0, which reads export data version 2 at most.
      Drop the override when rules_go pins v0.44.0 or later. [#127]
- [X] Replace custom cancellable sleeps across the codebase with `xtime.Sleep`.
      `walbench` now uses `xtime.Sleep`. [#72, #87]
- [ ] Hedged sequence discovery: probe candidate sequence numbers concurrently in LogStream. [#75]
- [ ] Plan and execute deterministic simulation testing from `docs/design/testing.md`. [#85]
  - [ ] Implement injectable `Clock` interface in `internal/xtime`.
  - [ ] Build deterministic `FaultStore` proxy wrapping `objectstore.memDriver`.
  - [ ] Implement shadow invariant differential test harness for `logstream` and `manifest`.
- [ ] Integrate Python into Bazel. [#101, #104]
  - [X] Configure `rules_python` in `MODULE.bazel` for hermetic Python toolchains. [#101]
  - [X] Add Bazel targets (`py_binary`, `py_library`) for Python scripts. [#101]
  - [ ] Integrate `ruff` and type-checking into `bazel test` and format targets.
- [ ] Refactor segment reader to follow the `bufio.Scanner` pattern.
      Replace `Next() (*DocumentMutation, error)` returning `io.EOF` with `Scan() bool`, `Mutation() *DocumentMutation`, and `Err() error`. [#107]
- [ ] Use nil-safe protobuf getters across the codebase. Replace a nil check
      plus field access with `GetX()`. `docs/guidelines/go.md` has the rule. [#125]
- [ ] Fix the double expansion of `--config=race`. `.bazelrc` sets it by default, so an explicit `--config=race` expands it twice. [#108]
- [ ] Drop `--test_output=streamed` from `test:fuzz`. It disables sharding and serializes the test run. [#108]
- [ ] Build fuzz targets with coverage instrumentation. Without it, fuzzing runs without coverage guidance. [#82, #108]

## Done

- [X] Enable `--config=race` by default for `build` in `.bazelrc`. Gazelle
      analysis under race mode works after upgrading to rules_go 0.63.0 and
      gazelle 0.53.0. [#98]
- [X] `canary (go1.27)` fails. The nogo binary in rules_go 0.62.0 reads export
      data version 2 at most. Go 1.27rc2 writes version 4. Try rules_go 0.63.0. [#35]
