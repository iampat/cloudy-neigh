---
name: code-review
description: Review Go code, a diff, or a pull request against this repo's conventions. Use when asked to review code or a PR.
allowed-tools: Read, Grep, Glob, Bash
---

# Code review

Cite `file:line` and give the concrete replacement text, not a description of one.
When the shorter fix is to restructure the code rather than patch it, suggest the
restructure instead.

Skip anything the toolchain already enforces — gofumpt, buildifier, nogo, and
golangci-lint all run in CI. Formatting and unused-variable comments are noise.

Generated code is out of scope. Protoc output lives in `bazel-bin` and never
reaches the repository. Do not review it, do not lint it, and do not apply the
comment rules to it. A source-level tool that cannot load a generated package
fails on every package that imports it. That is a fact about the tool, not a
finding.

Report only what you can point at. An unverified suspicion stated as a finding
costs more than it saves.

Output findings only, ranked by severity. No summary of the diff, no praise, no
preamble.

## 1. Delete code that does not earn its place — highest priority

- Cut anything adding no value to the implementation or the test.
- Do not assert things you can assume already work; test the thing under test.
- Drop a nil check on a value that was just assigned.
- Do not export what nothing outside the package uses.
- Inline a single-use constant or struct field assignment.
- No wrapper type, generic, or helper introduced for one caller.

## 2. Dependencies

- A new third-party dependency needs agreement first. Flag any `go.mod` addition
  that was not discussed, including test-only ones.
- Prefer the standard library. In tests, `testify` is permitted when it
  makes an assertion more readable than the stdlib form.
- Never take a dependency to avoid writing five lines.

## 3. Errors

- Every error is handled. Never `_ = f()` when `f` failing invalidates what follows.
- Wrap only when the wrap adds information: `fmt.Errorf("read manifest: %w", err)`.
- No panic in library code — return an error and let the caller decide.
- Validate at the boundary, not deep in the call stack.
- Use `errors.As`/`errors.Is`; never a single-value type assertion on an error.

## 4. Concurrency and data ownership

Where a storage engine actually breaks. Look hardest here.

- Default to `sync.Mutex`. Reach for `RWMutex` only when reads vastly outnumber
  writes or readers hold the lock a long time.
- No I/O while holding a lock.
- Clone before releasing a lock if the caller may mutate the value. Do not return
  a pointer aliasing lock-protected state.
- Prefer immutable values over shared mutable state.
- Every `<-ch` needs a `select` with `ctx.Done()`, or it can hang forever.
- Anything claiming atomicity or durability must have an identifiable
  linearization point. Ask where it is.
- A function that does I/O, or calls one that does, takes `context.Context`
  first and passes it down. Flag a call that drops one, and flag a `_ context.Context`
  parameter. Check `ctx.Err()` where there is work worth abandoning: once per
  item in a batch, and once more before a commit. Flag a context parameter that
  no implementation reads, which claims a cancellation that does not happen.

## 5. Tests

- Never `time.Sleep` to order events — use channels, `sync.WaitGroup`, or polling.
- Flag `for i := 0; i < b.N; i++` in a benchmark. The loop is
  `for b.Loop()`, and it makes `ResetTimer` around setup unnecessary.
- Never `time.Now` for uniqueness or ordering. Use `t.Name()` for a key
  prefix and a counter for a nonce. The clock is permitted only to measure a
  reported rate.
- Never assert from inside a goroutine. An assertion that fires after the test
  returns panics the whole binary. Send the error to a buffered channel and assert
  on the test goroutine.
- No writes to package-level variables; tests share one process.
- A goroutine holding a precondition in place must loop until `ctx.Done()` rather
  than run once — a single attempt that fails silently leaves the waiter hanging.
- Cover failure modes, not just the happy path: crash mid-write, partial write,
  concurrent writers, cancelled context.
- Table-driven with named cases.
- An external test package tests a package: `package index_test` for
  `package index`. Flag an internal test that reaches nothing unexported, and
  flag an identifier exported only to let a test see it.

## 6. Comments

The default is no comment. Two kinds earn a place: a workaround with the upstream
issue that forces it, and an invariant a reader would otherwise violate. Flag
every other comment for deletion. A doc comment on an exported name is not
exempt. Go convention alone does not justify one.

A comment that earns its place is at most two sentences. Flag a longer one:
it holds rationale, and rationale lives in the design note. Flag any comment
that restates a design note. The note is the one source, and the copy in the
code goes stale.

Flag a comment that:

- paraphrases the identifier it documents. `// Digest names a blob by the
  SHA-256 of its bytes` above `type Digest [sha256.Size]byte` says the type name
  again.
- restates the signature. `// Root reports false when nothing is stored` above
  `Root() (Digest, bool, error)` says what the `bool` already says.
- explains a pattern the reader knows, or derives what follows from it. A Go
  engineer knows that a content-addressed store returns the same digest for the
  same bytes.
- gives the reason for a choice no reader would question. Hex in JSON needs no
  defence.
- names the caller, or describes how another package uses the code. That text is
  wrong after the first refactor.
- labels a section or narrates the change to the reviewer.

If a better name removes the need for the comment, give the name. No metaphor
and no counterfactual. Plain sentences. Several short ones beat one built from
stacked clauses.

## 7. Zero values

- Make the zero value useful (Go proverb). Flag a type whose zero value
  errors or panics where a sensible default exists.
- Flag an API that needs a nil pointer to express the default when the zero
  value of the type can carry it.

## 8. Naming

- No `Get` prefix on getters, no `Impl` suffix.
- No stutter: `Status` in package `index`, not `IndexStatus`.
- `TestFoo`, not `Test_Foo`.

## 9. Build files

- `BUILD.bazel` files are gazelle output. Regenerate them. Do not hand-edit.
- A `go.mod` change must arrive with the regenerated `MODULE.bazel`,
  `MODULE.bazel.lock`, and BUILD files. `bazel mod tidy` produces the first two.
