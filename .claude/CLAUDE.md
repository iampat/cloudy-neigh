# cloudy-neigh

A cloud-native search engine in Go. Distributed-systems and storage-engine work:
durability, consistency, and failure modes matter more than feature velocity.

## Lessons

Read `.claude/lessons.md` before your first action in a task. It holds the
corrections the user repeated that no other rule file covers, and each entry
names the action to take. The `retro` skill maintains that file. The user
triggers the skill and approves every edit it makes.

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

## agy

`agy` is a separate agent CLI with its own models. It gives the project a
second opinion, and a second pair of hands. A persona sits in
`.agents/skills/<name>/SKILL.md`, and the skill that drives it sits in
`.claude/skills/consult-<name>/SKILL.md`.

These facts come from `agy help`. When a call fails, run `agy help` again. Do
not trust memory over the help output.

- `agy -p "<prompt>"` runs one prompt and prints the response. The prompt is an
  argument, not stdin.
- `/<persona> <prompt>` as the first token loads that persona. The persona pins
  its own model, so never pass `--model`.
- `--output-format json` adds a `conversation_id`.
  `agy --conversation <id> -p "<reply>"` continues that conversation.
- Headless mode cannot prompt for a tool permission. Pass
  `--dangerously-skip-permissions` on every call, so agy reads and writes files
  itself.
- The print timeout defaults to five minutes. Pass `--print-timeout 20m` for a
  turn that writes code. A long turn outlives the Bash tool timeout and moves
  to the background, which is normal. Read the output file it names.
- Write the prompt to a file first, then call `agy`. Two steps pass the
  permission classifier, and one compound command does not.
- Redirect the output to a file with `> out.json 2>&1`. Never pipe agy into
  `tail`, `head` or any filter. A turn that runs Bazel leaves a server daemon,
  the daemon inherits the pipe, and the pipe never reaches end of file. The
  filter then waits forever and the response dies in its buffer.
- `agy --continue -p "<reply>"` resumes the most recent conversation. It
  recovers a thread whose `conversation_id` was lost.
- A hung call needs a diagnosis, not a wait. `pgrep -f "agy -p"` also matches
  the wrapper shell, so it reports a process that is already gone. Look for the
  real binary with `ps -ax -o pid,ppid,command | grep "[a]gy"`, and find what
  holds the pipe with `lsof`.
- Run agy in another directory with `env -C <dir> agy ...`. A `cd` does not
  persist between tool calls, and a worktree needs the working directory.
- An empty `response` with an error on stderr is a failure. Stop and report the
  command and the error to the user, verbatim. Do not retry, and do not work
  around it.
- A transcript of the conversation goes to `docs/reviews/<date>-<topic>.md`.

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
  to Go. Delete them before you show the code, not after review asks.
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
- Before declaring done: `bazel build //...` and `bazel run //:format.check` pass.
  A change that touches only Markdown skips both. Run
  `.claude/skills/design-note/lint.sh <file>` on each changed document instead.
- Before opening a PR: run `bazel mod tidy`. A `go.mod` change leaves
  `MODULE.bazel` stale, and a stale `use_repo` call breaks the build.
- After a PR merges: go to `main`, rebase on `origin`, then delete the merged
  branch local and remote. One turn, no questions.
- Do not solve a problem we do not have. Leave out a limit, a constraint, or a
  mitigation until the problem appears. Delete a `CONSIDER` that guards a case
  nobody met.
- Verify a claim before you write it. Read the help output, the source, or the
  API before you state how a tool behaves. "I do not know" beats a confident
  error.
- A correction changes the rules, not only the file. When the user corrects a
  class of mistake, fix the instance and update the guideline or the skill in
  the same turn.
- An agy review transcript lives in `docs/reviews/<date>-<topic>.md` for the
  pull request only. `git rm` it before the merge.

For planning a new feature or design, use the `plan-feature` skill. For any
documentation or technical prose — `docs/`, READMEs, PR descriptions — use the
`design-note` skill. To review code, a diff, or a PR, use the `code-review`
skill. Before you present a design as done, use the `consult-jeff-dean` skill to get an
external review from `agy`. For a hard algorithmic or data-structure problem,
use the `consult-petr` skill. Petr designs and writes that code, and you
supervise.
