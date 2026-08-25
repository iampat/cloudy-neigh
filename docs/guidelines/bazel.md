# Bazel guidelines

These rules bind `BUILD.bazel`, `MODULE.bazel`, and `.bazelrc`. Read them
before the first Bazel edit in a task. Review Bazel changes against them. The
comment rules in `.claude/CLAUDE.md` also apply.

## Generated build files

`BUILD.bazel` files are gazelle output. Regenerate them with
`bazel run //:gazelle`. Do not hand-edit. To resolve a conflict, delete the
file and regenerate: `rm <pkg>/BUILD.bazel && bazel run //:gazelle`.

## Dependency changes

A `go.mod` change must arrive with the regenerated `MODULE.bazel`,
`MODULE.bazel.lock`, and BUILD files. `bazel mod tidy` produces the first two.
A stale `use_repo` call breaks the build.

## Formatting

Buildifier formats build files: `bazel run //:buildifier`. It runs with its
default configuration. Formatting is not a review topic.
