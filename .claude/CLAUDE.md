# cloudy-neigh

A cloud-native search engine in Go. Distributed-systems and storage-engine work:
durability, consistency, and failure modes matter more than feature velocity.

## Build — Bazel only

Never invoke `go build`/`go test` or run scripts directly; go through Bazel so the
toolchain and dependency graph stay authoritative.

- Test: `bazel test //...` (race: add `--config=race`)
- Regenerate BUILD files after adding/moving Go code: `bazel run //:gazelle`
- Format: `bazel run //:format` (check-only: `//:format.check`; BUILD files: `//:buildifier`)
- Go toolchain passthrough: `bazel run @io_bazel_rules_go//go -- <args>`
- Dependency change: edit `go.mod` → `bazel run @io_bazel_rules_go//go -- mod tidy`
  (also updates MODULE.bazel) → `bazel run //:gazelle`

## Gotchas & conventions

- No new third-party Go dependencies unless we agree first — prefer stdlib,
  including for test assertions. This covers "convenience" deps like testify.
- Structured logging via stdlib `log/slog`; prefer returning errors over
  fatal-level logging.
- Never `time.Sleep` to synchronize a test — poll or use channels.
- Flag open design questions with `CONSIDER(ali):` comments.
- Design notes: standalone simple HTML in `docs/design/`, no CSS/JS frameworks;
  update them alongside the code they describe.
- Branch names: `ali/<topic>` for features; `chore/`, `docs/`, `fix/` otherwise.
- Before declaring done: `bazel build //...` and `bazel run //:format.check` pass.

For planning a new feature or design, use the `plan-feature` skill.
