# cloudy-neigh

A cloud-native search engine in Go. Distributed-systems and storage-engine work:
durability, consistency, and failure modes matter more than feature velocity.

## Build — Bazel only

Never invoke `go build`/`go test` or run scripts directly. Go through Bazel so
the toolchain and the dependency graph stay authoritative.

| Plain Go | Here |
| --- | --- |
| `go build ./...` | `bazel build //...` |
| `go test ./...` | `bazel test //...` (race: `--config=race`) |
| `go test -run TestX ./pkg` | `bazel test //pkg:pkg_test --test_filter=TestX` |
| `go test -v` | `bazel test //... --test_output=all` |
| `go test -bench=.` | `bazel test //... --test_arg=-test.bench=. --test_output=all` |
| add a dependency | add the import, then `bazel run @io_bazel_rules_go//go -- mod tidy` |
| `go mod tidy` | `bazel run @io_bazel_rules_go//go -- mod tidy` (also runs `bazel mod tidy`) |
| `go fmt ./...` | `bazel run //:format` (check-only: `//:format.check`) |
| `go vet ./...` | nogo, which runs inside `bazel build` |
| `go run ./cmd/x -- a b` | `bazel run //cmd/x -- a b` |
| `go run <tool>@<version>` | `bazel run @io_bazel_rules_go//go -- run <tool>@<version>` |
| anything else | `bazel run @io_bazel_rules_go//go -- <args>` |

### Useful commands

```sh
bazel run //:gazelle                           # regenerate BUILD files
bazel run //:buildifier                        # format BUILD files
rm <pkg>/BUILD.bazel && bazel run //:gazelle   # regenerate rather than merge
bazel query "somepath(//a, //b)"               # why does A depend on B
bazel build //... --verbose_failures --keep_going
bazel aquery //proto/cloudyneigh:cloudyneigh_go_proto  # generated file paths
find -L bazel-bin/proto -name '*.pb.go' -exec cp {} proto/cloudyneigh/ \;  # stage
bazel mod show_extension @bazel_gazelle//:extensions.bzl%go_deps  # resolved Go repos
```

The last one stages the generated protobuf code in the source tree. A tool that
reads source cannot load that package otherwise, and fails on every package that
imports it. Delete the copies when the tool finishes.

## Language guidelines

Before the first edit in a language — new code or a refactor — read its
guideline file. Code review reads the same files, so you write to the rules
you are reviewed against.

- Go (`*.go`, `go.mod`): `docs/guidelines/go.md`
- Bazel (`BUILD.bazel`, `MODULE.bazel`, `.bazelrc`): `docs/guidelines/bazel.md`

Python, JavaScript, and Protobuf get a file here when their first source file
lands.

## Google Cloud

- Always use the `kentrolabs-ai` project with the `amiri1982@gmail.com`
  account. Pass `--account=amiri1982@gmail.com` and
  `--project=kentrolabs-ai` on every gcloud call. Never use another account
  or project, and never change the global gcloud config or the application
  default credentials.
- For a Go client, mint a token with
  `gcloud auth print-access-token --account=amiri1982@gmail.com` and pass it
  as an `oauth2.StaticTokenSource`.

## Gotchas & conventions

- Generated code is not ours. Protoc output lives in `bazel-bin` and never
  reaches the repository. It stays outside review, outside lint, and outside the
  comment rules.
- Default to no comment. Two kinds earn a place: a workaround with the upstream
  issue that forces it, and an invariant a reader would otherwise violate. Write
  nothing else, doc comments on exported names included. A comment that earns
  its place is at most two sentences. Do not restate a design note in code.
  The note holds the rationale, and the copy goes stale. Go convention alone
  does not justify a doc comment. Delete a comment that paraphrases the
  identifier, restates the signature, or explains a pattern the reader knows.
  Delete one that gives the reason for an obvious choice, or names the caller. Applies to config and
  build files (`.bazelrc`, `MODULE.bazel`, `BUILD.bazel`, workflows) as much as
  to Go.
- Flag open design questions with `CONSIDER(ali):` comments.
- Write prose in ASD-STE100 Simplified Technical English: active voice, simple
  tenses, one idea per sentence, one term per concept. Maximum 20 words in an
  instruction, 25 in a description. No semicolons, no phrasal verbs. Spell out
  an acronym at first use. "e.g." is permitted.
- Write for a working software engineer. Do not explain a language feature, a
  protocol, or a pattern that audience already knows. Explain only what is
  specific to this project.
- Replies to the user follow the same rules. Lead with the result. Add detail
  only when it changes what the reader does next. To explain structure or
  flow, prefer a small sketch — a file tree, a call tree, pseudocode, or a
  diff — over prose.
- Design notes: Markdown in `docs/design/`. Update them alongside the code they
  describe.
- Every change reaches `main` through a pull request. Never commit on `main`,
  and never push to `origin/main`. Start a branch first, even for a one-line
  Markdown edit.
- A branch name always starts with `ali/`. The type of the change goes in the PR
  title instead, as `feat:`, `fix:`, `chore:`, or `docs:`.
- Report the URL when you open a PR, then stop. Do not wait for the checks and
  do not poll them. I read them myself.
- Never put a Claude session link in a commit message or a PR body. Drop the
  `Claude-Session:` trailer, and drop the `https://claude.ai/code/session_...`
  URL under the generated-with line. The link opens for one person, and the
  repository keeps it forever. Keep the `Co-Authored-By:` trailer and the
  generated-with line, which name the tool without a private URL.
- Before declaring done: `bazel build //...` and `bazel run //:format.check` pass.
  A change that touches only Markdown skips both. Run
  `.claude/skills/design-note/lint.sh <file>` on each changed document instead.
- Before opening a PR: run `bazel mod tidy`. A `go.mod` change leaves
  `MODULE.bazel` stale, and a stale `use_repo` call breaks the build.

For planning a new feature or design, use the `plan-feature` skill. For any
documentation or technical prose — `docs/`, READMEs, PR descriptions — use the
`design-note` skill. To review code, a diff, or a PR, use the `code-review`
skill. Before you present a design as done, use the `consult-jeff-dean` skill to get an
external review from `agy`.
