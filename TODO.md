# TODO

- [ ] Refactor the storage layer.
  - [ ] Replace the cloud SDK with a shim around GCS. Use the atomic-file
        package from Tailscale.
  - [ ] Revisit the ObjectStore API. Add a `Head` call, because the LogStream
        tail search probes with `List` today and pays list pricing. Drop the
        generation token from a conditional create, which no caller reads.
- [ ] Remove the `golang.org/x/tools` override in `MODULE.bazel`. rules_go
      0.62.0 pins v0.34.0, which reads export data version 2 at most. Drop the
      override when rules_go pins v0.44.0 or later.

## Done

- [X] `canary (go1.27)` fails. The nogo binary in rules_go 0.62.0 reads export
      data version 2 at most. Go 1.27rc2 writes version 4. Try rules_go 0.63.0.
