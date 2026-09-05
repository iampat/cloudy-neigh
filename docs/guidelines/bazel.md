# Bazel guidelines

These rules bind `BUILD.bazel`, `MODULE.bazel`, and `.bazelrc`. Read them
before the first Bazel edit in a task. Review Bazel changes against them. The
comment rules in `.claude/CLAUDE.md` also apply.

## Generated build files

Prefer Gazelle for authoring and editing `BUILD.bazel` files. Avoid editing them
by hand unless Gazelle cannot express the target.

## Dependency changes

A `go.mod` change must arrive with the regenerated `MODULE.bazel`,
`MODULE.bazel.lock`, and BUILD files. `bazel mod tidy` produces the first two.
A stale `use_repo` call breaks the build.

## Formatting

Buildifier formats build files: `bazel run //:buildifier`. It runs with its
default configuration. Formatting is not a review topic.
