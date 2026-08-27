# TODO

- [ ] Refactor the storage layer.
  - [ ] Replace the cloud SDK with a shim around GCS. Use the atomic-file
        package from Tailscale.
  - [ ] Revisit the ObjectStore API. Add a `Head` call, because the LogStream
        tail search probes with `List` today and pays list pricing. Drop the
        generation token from a conditional create, which no caller reads.
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
- [ ] Remove the `golang.org/x/tools` override in `MODULE.bazel`. rules_go
      0.62.0 pins v0.34.0, which reads export data version 2 at most. Drop the
      override when rules_go pins v0.44.0 or later.

## Done

- [X] `canary (go1.27)` fails. The nogo binary in rules_go 0.62.0 reads export
      data version 2 at most. Go 1.27rc2 writes version 4. Try rules_go 0.63.0.
