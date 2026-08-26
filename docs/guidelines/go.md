# Go guidelines

These rules bind all Go code in this repository. Read them before the first
Go edit in a task. Review Go changes against them. The comment rules in
`.claude/CLAUDE.md` also apply.

## Less code

The highest-value review finding is a deletion.

- Cut anything that adds no value to the implementation or the test.
- Drop a nil check on a value that was just assigned.
- Do not export what nothing outside the package uses.
- Inline a single-use constant or struct field assignment.
- Inline a function with one caller.
- No wrapper type, generic, or helper introduced for one caller.
- Collapse two switches on the same value into one.
- Fold two files that differ in one field into one file.
- Do not assert what you can assume already works. Test the thing under test.

## Simplicity

Explicit beats clever. Prefer the obvious solution a reader can follow without
a reconstruction of your reasoning, even when a terser one exists.

No hidden magic. Never add a silent default, a silent fallback, or a quiet
directory creation. A missing input is the caller's error, and the error says
what is missing.

One implementation, never one per backend. Before you write the second
backend, find the library's unified interface. Push the backend-specific code
into the smallest hook the library offers, such as the `As` escape hatch in
`gocloud.dev/blob`. A constructor per backend is the smell this rule prevents.

## Dependencies

- No new third-party dependency unless we agree first. Flag a `go.mod`
  addition that had no agreement, test-only ones included.
- Prefer the standard library. `github.com/stretchr/testify` is pre-agreed for
  tests. Use it when it makes an assertion more readable than the stdlib form.
- Never take a dependency to avoid five lines of code.

## Errors

- Handle every error. Never `_ = f()` when a failure of `f` invalidates what
  follows.
- Wrap only when the wrap adds information: `fmt.Errorf("read manifest: %w", err)`.
- No panic in library code. Return an error and let the caller decide.
- Validate at the boundary, not deep in the call stack.
- Use `errors.Is`/`errors.As`. Never a single-value type assertion on an error.
- Prefer a returned error over a fatal-level log.

## Context

- A function that does I/O, or calls one that does, takes `context.Context` as
  its first parameter and passes it down. Flag a call that drops one, and flag
  a `_ context.Context` parameter.
- Check `ctx.Err()` where there is work worth abandoning: once per item in a
  batch, and once more before a commit.
- Never add a context that no implementation reads. It claims a cancellation
  that does not happen. `cas.Store` takes none, because `os.Rename` and
  `File.Sync` do not observe one.

## Concurrency and data ownership

Concurrency is where a storage engine breaks. Spend the most care here, in
writing and in review.

- Default to `sync.Mutex`. Use `RWMutex` only when reads vastly outnumber
  writes or readers hold the lock a long time.
- No I/O under a lock.
- Clone before you release a lock if the caller may mutate the value. Do not
  return a pointer that aliases lock-protected state.
- Prefer immutable values over shared mutable state.
- Every `<-ch` needs a `select` with `ctx.Done()`, or it can hang forever.
- A claim of atomicity or durability must have an identifiable linearization
  point. In review, ask where it is.

## Zero values

- Make the zero value useful (Go proverb). The zero value of a type is the
  documented default, not an error. Reject only a state the contract has no
  meaning for.
- Flag an API that needs a nil pointer to express the default when the zero
  value of the type can carry it.

## Logging

Use stdlib `log/slog` for structured logging.

## Naming

- No `Get` prefix on a getter, no `Impl` suffix.
- No stutter: `Status` in package `index`, not `IndexStatus`.
- `TestFoo`, not `Test_Foo`.

## Tests

- An external test package tests a package: `package index_test` for
  `package index`. The test then proves the exported API is enough. Use an
  internal test only to reach something unexported, and say why in the commit.
  Flag an identifier exported only to let a test see it.
- Never `time.Sleep` to synchronize a test. Poll, or use channels or
  `sync.WaitGroup`.
- A benchmark loop is `for b.Loop()`, never `for i := 0; i < b.N; i++`.
  Setup goes before the loop and cleanup after it, with no `ResetTimer`.
- Never `time.Now` for uniqueness or ordering. Use `t.Name()` for a key prefix
  and a counter for a nonce, and delete what an earlier run left. The clock is
  permitted only to measure a reported rate.
- Never assert from inside a goroutine. An assertion that fires after the test
  returns panics the whole binary. Send the error to a buffered channel and
  assert on the test goroutine.
- No writes to package-level variables. Tests share one process.
- A goroutine that holds a precondition in place must loop until `ctx.Done()`.
  A single attempt that fails silently leaves the waiter to hang.
- Cover failure modes, not only the happy path: crash mid-write, partial
  write, concurrent writers, cancelled context.
- Table-driven with named cases.

## Comments

The comment rules in `.claude/CLAUDE.md` state the doctrine. The default is no
comment. Flag a comment that:

- paraphrases the identifier it documents. `// Digest names a blob by the
  SHA-256 of its bytes` above `type Digest [sha256.Size]byte` says the type
  name again.
- restates the signature. `// Root reports false when nothing is stored` above
  `Root() (Digest, bool, error)` says what the `bool` already says.
- explains a pattern the reader knows, or derives what follows from it. A Go
  engineer knows that a content-addressed store returns the same digest for
  the same bytes.
- gives the reason for a choice no reader would question. Hex in JSON needs no
  defence.
- names the caller, or describes how another package uses the code. That text
  is wrong after the first refactor.
- labels a section or narrates the change to the reviewer.

If a better name removes the need for the comment, give the name. No metaphor
and no counterfactual. Plain sentences. Several short ones beat one built from
stacked clauses.

## Tooling

gofumpt runs through `bazel run //:format`. nogo runs inside `bazel build`.
golangci-lint runs in CI as an advisory check. Do not report in review what
these already enforce. Formatting and unused-variable findings are noise.
