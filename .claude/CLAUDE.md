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
- Explicit beats clever. Prefer the obvious solution a reader can follow without
  reconstructing your reasoning, even when a terser one exists.
- Comment only what the code cannot say: a non-obvious *why*, a workaround and
  the upstream issue forcing it, an invariant a reader would otherwise violate.
  Do not restate the code, label sections, or narrate a change to the reader —
  if deleting the comment loses nothing, delete it. Applies to config and build
  files (`.bazelrc`, `MODULE.bazel`, `BUILD.bazel`, workflows) as much as to Go.
- Flag open design questions with `CONSIDER(ali):` comments.
- Write prose in Simplified Technical English: active voice, present tense, one
  idea per sentence, 15–20 words, one term per concept. Spell out an acronym at
  first use. Write "for example", not "e.g.".
- Design notes: Markdown in `docs/design/`; update them alongside the code they
  describe.
- Branch names: `ali/<topic>` for features; `chore/`, `docs/`, `fix/` otherwise.
- Before declaring done: `bazel build //...` and `bazel run //:format.check` pass.

For planning a new feature or design, use the `plan-feature` skill. For writing
any document under `docs/`, use the `design-note` skill.
