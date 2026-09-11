Warning: no stdin data received in 3s, proceeding without it. If piping from a slow command, redirect stdin explicitly: < /dev/null to skip, or wait longer.
Findings, ranked.

**1. Concurrent `Sync` calls apply segments out of order — `query/loader.go:69-93`**
`inFlight` exists only to support concurrent `Sync` callers. But it makes them unsound: while caller A loads seg-1, caller B skips it and applies seg-2 first. Seg-1's older PUT for a doc then lands after seg-2's newer PUT, and the merge in `Upsert` keeps stale values. Manifest order is the correctness invariant. The one real caller, `Run`, is sequential. Delete `inFlight` and serialize the whole method:

```go
func (l *Loader) Sync(ctx context.Context, branch string) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	manifest, _, err := kvfs.ResolveBranch(ctx, l.store, branch)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("sync branch %s: %w", branch, err)
	}

	loadedCount := 0
	for _, seg := range manifest.Segments {
		if l.loaded[seg.SegmentId] {
			continue
		}
		if err := l.loadSegment(ctx, branch, seg.SegmentId); err != nil {
			return loadedCount, err
		}
		l.loaded[seg.SegmentId] = true
		loadedCount++
	}
	return loadedCount, nil
}
```

This holds `l.mu` across I/O. That is the point here: the lock is the linearization mechanism for manifest order, and it guards only loader bookkeeping. `Table` reads stay unblocked.

**2. `Run` can return nil on a real failure — `query/loader.go:147-149`**
`Sync` can surface a `DeadlineExceeded` from a store-internal timeout while `ctx` is still live. `errors.Is` matches, `ctx.Err()` is nil, and `Run` returns nil. The loop stops silently with no error and no log. Test the context, not the error:

```go
		if _, err := l.Sync(ctx, branch); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.WarnContext(ctx, "manifest loader sync failed", "branch", branch, "err", err)
		}
```

**3. Unknown `MutationOp` is dropped silently — `query/loader.go:119-130`**
A new op added to the proto later would skip records during load with no signal. That is silent data loss. Add a default arm:

```go
		default:
			return fmt.Errorf("unknown mutation op %v in %s", mut.Op, segKey)
```

**4. Silent default for `pollInterval` — `query/loader.go:139-141`**
`docs/guidelines/go.md` bans a silent fallback. A caller who passes 0 gets 100ms with no signal. A missing input is the caller's error:

```go
	if pollInterval <= 0 {
		return fmt.Errorf("query: pollInterval must be positive, got %v", pollInterval)
	}
```

**5. Two init paths for `Table` — `query/columnar.go:79-101`**
`NewTable(chunkSize ...int)` and `initLocked` both initialize the table. The variadic optional argument is a silent fallback twice: `NewTable(0)` gets 65536 without a signal, and `NewTable(3, 7)` compiles and drops the 7. Pick one path. Keep the constructor, take a plain `int`, and delete `initLocked` with its three call sites:

```go
func NewTable(chunkSize int) *Table {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	return &Table{
		chunkSize:  chunkSize,
		index:      make(map[string]RowLoc),
		vectorDims: make(map[string]int),
	}
}
```

`chunkSize == 0` for the default is the documented zero value, not a hidden fallback. Callers become `query.NewTable(0)` and `query.NewTable(3)`.

**6. Exports that only tests use**
Nothing outside the package imports `query`. The guideline says: never export an identifier solely for tests.

- `Table()` (`query/loader.go:44`): the caller constructs the table and passes it to `NewLoader`. It already holds the reference. Delete.
- `LoadedCount` (`query/loader.go:54`): only `loader_test.go:97` uses it, next to an `IsLoaded` check that already proves the point. Delete.
- `Loc`, `RowLoc`, `IsTombstoned`, `TotalRows`, `ChunkCount` (`query/columnar.go:17`, `308-361`): only `columnar_test.go` uses them, to assert internal chunk layout. That tests the implementation, not the contract. Delete them, and rewrite `TestTable_ChunkBoundaries` and `TestTable_DeleteTombstones` against `Get`, `Vector`, and `Len`. Keep an unexported `rowLoc` for the index map. Export them later when the index integration needs them.

**7. Dead nil check — `query/loader.go:115-117`**
`segment.Reader.Next` never returns `(nil, nil)` (`segment/reader.go:20-33`). Delete:

```go
		if mut == nil {
			continue
		}
```

One note on scope: the command names "PR 2". This branch holds one commit, 735bd0c, and no open PR number was given to cross-check. I reviewed the full branch diff against `main`. Tell me if "PR 2" means a different diff.
