# Bazel guidelines

These rules bind `BUILD.bazel`, `MODULE.bazel`, and `.bazelrc`. Read them
before the first Bazel edit in a task. Review Bazel changes against them. The
comment rules in `.claude/CLAUDE.md` also apply.

## Generated build files

Use Gazelle for authoring or editing `BUILD.bazel` files. Never author or edit
them by hand.

## Dependency changes

A `go.mod` change must arrive with the regenerated `MODULE.bazel`,
`MODULE.bazel.lock`, and BUILD files. `bazel mod tidy` produces the first two.
A stale `use_repo` call breaks the build.

## Formatting

Buildifier formats build files: `bazel run //:buildifier`. It runs with its
default configuration. Formatting is not a review topic.

## Invalidation and performance

Always pass `--config=race` on `bazel build`, `bazel test`, and `bazel coverage`.
`.bazelrc` enables this as default for `build`. Toggling the race
flag discards the analysis cache and forces expensive rebuilds.

