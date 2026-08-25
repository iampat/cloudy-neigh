---
name: code-review
description: Review Go code, a diff, or a pull request against this repo's conventions. Use when asked to review code or a PR.
allowed-tools: Read, Grep, Glob, Bash
---

# Code review

## Rules

The language rules live in `docs/guidelines/`. Find the languages the diff
touches, and read the guideline file for each before you review:

- Go (`*.go`, `go.mod`): `docs/guidelines/go.md`
- Bazel (`BUILD.bazel`, `MODULE.bazel`, `.bazelrc`): `docs/guidelines/bazel.md`

Review against every rule in those files. The comment rules in
`.claude/CLAUDE.md` apply to every file in the diff, with or without a
guideline file.

Skip what the toolchain already enforces. The Tooling section of
`docs/guidelines/go.md` names the tools.

Generated code is out of scope. Protoc output lives in `bazel-bin` and never
reaches the repository. Do not review it, do not lint it, and do not apply the
comment rules to it. A source-level tool that cannot load a generated package
fails on every package that imports it. That is a fact about the tool, not a
finding.

## Output

Cite `file:line` and give the concrete replacement text, not a description of
one. When the shorter fix is to restructure the code rather than patch it,
suggest the restructure instead.

Report only what you can point at. An unverified suspicion stated as a finding
costs more than it saves.

Output findings only, ranked by severity. No summary of the diff, no praise, no
preamble.
