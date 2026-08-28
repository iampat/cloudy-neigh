# TODO

- [ ] Refactor the storage layer.
  - [ ] Replace the cloud SDK with a shim around GCS. Use the atomic-file
        package from Tailscale.
  - [ ] Revisit the ObjectStore API. Add a `Head` call, because the LogStream
        tail search probes with `List` today and pays list pricing. Drop the
        generation token from a conditional create, which no caller reads.
- [ ] Settle the fileblob conditional write. `objectstore` never asks fileblob
      to enforce `Absent`. A read of `gocloud.dev@v0.46.0` shows a fresh mutex
      for every writer, then a rename over the target.
  - [ ] Reproduce it, or reject it. Race two writers with `IfNotExist` straight
        at fileblob, outside our lock. Our lock hides the fault today, so
        nothing in the repository proves it.
  - [ ] Report it upstream when it reproduces, and record the issue number.
  - [ ] Improve the implementation on the outcome. Drop `nativeAbsent` and the
        lock when fileblob holds. Keep both and cite the issue when it does not.
- [ ] Publish the benchmarks, the code coverage, and the build health on the
      front page of the repository. A number nobody sees changes no decision.
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
      and that is where a storage protocol fails.
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
      `bazel run @io_bazel_rules_go//go`, so it compiles the tool from source
      on every run. `setup-bazel` caches the Bazel disk cache and the
      repository cache. Neither one holds the Go build cache that `go run`
      writes, so no Bazel action covers this work. `govulncheck` has the same
      shape. Measure the step, find where the `go` tool puts `GOCACHE`, then
      cache that directory or make the tool a Bazel target.
- [ ] Add fuzz tests. A table test covers the cases we thought of. A fuzzer
      finds the frame that no case names.
  - [ ] Fuzz the RecordIO reader and the scanner with arbitrary bytes. Every
        input must give a record or a named error, and never a panic.
  - [ ] Fuzz a RecordIO write and read round trip. The records must come back
        byte for byte.
  - [ ] Fuzz the LogStream key parse and the stream name check. Both read
        untrusted text, and both need an internal test to reach.
  - [ ] Run a long fuzz job in CI and cache the corpus. `bazel test` runs the
        seed corpus alone today.
- [ ] Test against a mock object store. `logstream.New` takes a concrete
      `*objectstore.Store`, so no test can inject a failure today.
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
      records to measure write amplification.
- [ ] Remove the `golang.org/x/tools` override in `MODULE.bazel`. rules_go
      0.62.0 pins v0.34.0, which reads export data version 2 at most. Drop the
      override when rules_go pins v0.44.0 or later.
- [ ] Replace custom cancellable sleeps across the codebase with `xtime.Sleep`.
      `walbench` and other tools use ad-hoc timer and select patterns.

## Done

- [X] `canary (go1.27)` fails. The nogo binary in rules_go 0.62.0 reads export
      data version 2 at most. Go 1.27rc2 writes version 4. Try rules_go 0.63.0.
