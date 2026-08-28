# agy code review: one stream per Log

conversation_id: fc7f83cd-f88d-403f-96c9-173d97591b98

## prompt

```
Code review the branch `ali/logstream-one-stream` against `main`, here in
/Users/ali/workspace/iampat/cloudy-neigh.

Hold it to the rules of this repository.

Findings only, ranked by severity, each with a file:line and the fix.
```

## response

### High: [objectstore/local.go:98-130](file:///Users/ali/workspace/iampat/cloudy-neigh/objectstore/local.go#L98-L130)

`writeOptions` runs `d.live` unconditionally before checking `cond`. This causes two problems for `cond.Absent`:

1. On `memblob` (`d.nativeAbsent == true`), an existing key falls through to `waitPastGeneration(prevLive)` and sleeps up to 2 ms before failing.
2. On `fileblob`, `d.live` and `d.b.Exists` both stat the file under the lock.

`cond.Absent` never overwrites an existing key. It never needs `waitPastGeneration`. Fetch `prevLive` and wait only for overwrites.

```go
func (d *local) writeOptions(ctx context.Context, key string, cond *Condition) (*blob.WriterOptions, func() (string, error), error) {
	if cond != nil && cond.Absent {
		if d.nativeAbsent {
			return &blob.WriterOptions{IfNotExist: true}, func() (string, error) {
				return d.live(ctx, key)
			}, nil
		}
		exists, err := d.b.Exists(ctx, key)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			return nil, nil, errPrecondition(key)
		}
		return nil, func() (string, error) {
			return d.live(ctx, key)
		}, nil
	}
	prevLive, err := d.live(ctx, key)
	if err != nil && !errors.Is(err, ErrPreconditionFailed) {
		return nil, nil, err
	}
	if cond != nil {
		if !validLocalGeneration(cond.GenerationMatch) {
			return nil, nil, fmt.Errorf("objectstore: key %q: malformed generation %q", key, cond.GenerationMatch)
		}
		if prevLive != cond.GenerationMatch {
			return nil, nil, errPrecondition(key)
		}
	}
	waitPastGeneration(prevLive)
	return nil, func() (string, error) {
		return d.live(ctx, key)
	}, nil
}
```

---

### Medium: [logstream/log.go:117-121](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L117-L121)

The loop guard checks context cancellation right before `l.store.Put(ctx, ...)`. Every iteration of this loop calls `Put` or `jump`, and both take `ctx`. This violates the rule in `docs/guidelines/go.md` against checking cancellation before a call that takes context. Delete the check.

```go
<<<<
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}
====
>>>>
```

---

### Low: [logstream/log.go:43-44](file:///Users/ali/workspace/iampat/cloudy-neigh/logstream/log.go#L43-L44)

The comment restates the commit rationale and explains channel serialization. This violates the comment doctrine in `.claude/CLAUDE.md`. Delete it.

```go
<<<<
// One Log owns one stream. The channel serializes the appends of this process,
// so lastKnown needs no other lock.
====
>>>>
```

---

### Low: [objectstore/local.go:132-133](file:///Users/ali/workspace/iampat/cloudy-neigh/objectstore/local.go#L132-L133)

The comment restates design rationale and narrates PR changes. Delete it.

```go
<<<<
// A local generation is the modification time with the size, so two writes of
// one key must not land on the same stored time. A new key waits for nothing.
====
>>>>
```

---

### Low: [objectstore/local.go:37-39](file:///Users/ali/workspace/iampat/cloudy-neigh/objectstore/local.go#L37-L39)

The comment describes an upstream bug without citing the upstream issue. Add the issue reference or delete the comment.

```go
<<<<
// fileblob gives every writer a fresh mutex and then renames over the target,
// so its IfNotExist admits two winners. memblob checks and inserts under the
// mutex of the bucket, so only memblob sets nativeAbsent.
====
>>>>
```
