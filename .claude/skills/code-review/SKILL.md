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
- Prefer the standard library, assertions included.
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

## 5. Tests

- Never `time.Sleep` to order events — use channels, `sync.WaitGroup`, or polling.
- Never assert from inside a goroutine. An assertion that fires after the test
  returns panics the whole binary. Send the error to a buffered channel and assert
  on the test goroutine.
- No writes to package-level variables; tests share one process.
- A goroutine holding a precondition in place must loop until `ctx.Done()` rather
  than run once — a single attempt that fails silently leaves the waiter hanging.
- Cover failure modes, not just the happy path: crash mid-write, partial write,
  concurrent writers, cancelled context.
- Table-driven with named cases.

## 6. Comments

- Remove a comment whose content is apparent from the code.
- If a better name would remove the need for the comment, suggest the name.
- No metaphor, no counterfactuals, no account of the decision that produced the
  code, no explanation of how callers use it.
- Plain sentences. Several short ones beat one built from stacked clauses.

## 7. Naming

- No `Get` prefix on getters, no `Impl` suffix.
- No stutter: `Status` in package `index`, not `IndexStatus`.
- `TestFoo`, not `Test_Foo`.

## 8. Build files

- `BUILD.bazel` files are gazelle output. Regenerate them; do not hand-edit.
- A `go.mod` change must arrive with the regenerated `MODULE.bazel.lock` and
  BUILD files.
