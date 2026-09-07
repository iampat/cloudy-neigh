# Petr review: segment recordio

conversation_id: 70fe978b-a83a-46c9-977b-502c11c00a5e

## prompt

```
MODE: design

## The user's request, verbatim

Review PR #99

## Context added by the assistant

Review the branch `ali/segment-recordio` against `origin/main`. It is PR #99 in this repository (/Users/ali/workspace/iampat/cloudy-neigh).
Decide what to focus on and how to review. Hold the code to the rules of this repository.
```

## response

### High

[`kvfs/branch.go:23-44`](file:///Users/ali/workspace/iampat/cloudy-neigh/kvfs/branch.go#L23-L44), [`kvfs/branch_test.go:115-117`](file:///Users/ali/workspace/iampat/cloudy-neigh/kvfs/branch_test.go#L115-L117): Revert changes to `kvfs/branch.go` and `kvfs/branch_test.go`.

`origin/main` permitted `/` in branch names to support hierarchical paths like `features/search`. This PR removed `/` support from `ValidateBranch` and marked valid paths as invalid in tests. `docs/design/demo.md` specifies that `kvfs/branch.go` stays as is.

```diff
diff --git a/kvfs/branch.go b/kvfs/branch.go
index ce3519c..45ed0bc 100644
--- a/kvfs/branch.go
+++ b/kvfs/branch.go
@@ -6,6 +6,7 @@ import (
 	"errors"
 	"fmt"
 	"io"
+	"strings"
 
 	"github.com/iampat/cloudy-neigh/objectstore"
 	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
@@ -20,22 +21,26 @@ var (
 
 const refPrefix = "refs/heads/"
 
-func ValidateBranch(branch string) error {
+func validateBranch(branch string) error {
 	if branch == "" {
 		return fmt.Errorf("%w: empty branch name", ErrInvalidBranchName)
 	}
 
 	first := branch[0]
 	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')) {
-		return fmt.Errorf("%w: must start with letter: %q", ErrInvalidBranchName, branch)
+		return fmt.Errorf("%w: must start with a letter: %q", ErrInvalidBranchName, branch)
 	}
 
-	for i := 1; i < len(branch); i++ {
+	if strings.HasSuffix(branch, "/") || strings.Contains(branch, "//") {
+		return fmt.Errorf("%w: invalid slash placement: %q", ErrInvalidBranchName, branch)
+	}
+
+	for i := 0; i < len(branch); i++ {
 		c := branch[i]
 		switch {
-		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
+		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
 		case c >= '0' && c <= '9':
-		case c == '-' || c == '_':
+		case c == '-' || c == '_' || c == '/':
 		default:
 			return fmt.Errorf("%w: invalid character %q in %q", ErrInvalidBranchName, c, branch)
 		}
@@ -48,7 +53,7 @@ func branchKey(branch string) string {
 }
 
 func ResolveBranch(ctx context.Context, store objectstore.Store, branch string) (*storagepb.BranchManifest, string, error) {
-	if err := ValidateBranch(branch); err != nil {
+	if err := validateBranch(branch); err != nil {
 		return nil, "", err
 	}
 
@@ -71,7 +76,7 @@ func ResolveBranch(ctx context.Context, store objectstore.Store, branch string)
 }
 
 func UpdateBranch(ctx context.Context, store objectstore.Store, branch string, m *storagepb.BranchManifest, expectedGen string) (string, error) {
-	if err := ValidateBranch(branch); err != nil {
+	if err := validateBranch(branch); err != nil {
 		return "", err
 	}
 	if m == nil {
@@ -98,10 +103,10 @@ func UpdateBranch(ctx context.Context, store objectstore.Store, branch string, m
 }
 
 func CreateBranch(ctx context.Context, store objectstore.Store, newBranch, parentBranch string) (*storagepb.BranchManifest, string, error) {
-	if err := ValidateBranch(newBranch); err != nil {
+	if err := validateBranch(newBranch); err != nil {
 		return nil, "", err
 	}
-	if err := ValidateBranch(parentBranch); err != nil {
+	if err := validateBranch(parentBranch); err != nil {
 		return nil, "", err
 	}
 
@@ -126,7 +131,7 @@ func CreateBranch(ctx context.Context, store objectstore.Store, newBranch, paren
 }
 
 func DeleteBranch(ctx context.Context, store objectstore.Store, branch string) error {
-	if err := ValidateBranch(branch); err != nil {
+	if err := validateBranch(branch); err != nil {
 		return err
 	}
 	return store.Delete(ctx, branchKey(branch))
diff --git a/kvfs/branch_test.go b/kvfs/branch_test.go
index f8931fe..fb0b21d 100644
--- a/kvfs/branch_test.go
+++ b/kvfs/branch_test.go
@@ -112,9 +112,7 @@ func TestBranchNameValidation(t *testing.T) {
 			"",
 			"/main",
 			"main/",
-			"a/b",
 			"a//b",
-			"features/search",
 			"123branch",
 			"-branch",
 			"_branch",
```

---

[`segment/writer.go:14-34`](file:///Users/ali/workspace/iampat/cloudy-neigh/segment/writer.go#L14-L34), [`segment/BUILD.bazel:12`](file:///Users/ali/workspace/iampat/cloudy-neigh/segment/BUILD.bazel#L12): Remove `kvfs` dependency and branch validation from `segment.Writer`.

`segment` is a file format serializer. It must not depend on `kvfs`. Branch names are validated at the API boundary, not during segment serialization.

```diff
diff --git a/segment/BUILD.bazel b/segment/BUILD.bazel
index 9cd8fdc..b310f8d 100644
--- a/segment/BUILD.bazel
+++ b/segment/BUILD.bazel
@@ -9,7 +9,6 @@ go_library(
     importpath = "github.com/iampat/cloudy-neigh/segment",
     visibility = ["//visibility:public"],
     deps = [
-        "//kvfs",
         "//proto/storage/v1:storage",
         "//recordio",
         "@org_golang_google_protobuf//proto",
diff --git a/segment/writer.go b/segment/writer.go
index 14eede8..47f8702 100644
--- a/segment/writer.go
+++ b/segment/writer.go
@@ -5,16 +5,12 @@ import (
 	"fmt"
 	"io"
 
-	"github.com/iampat/cloudy-neigh/kvfs"
 	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
 	"github.com/iampat/cloudy-neigh/recordio"
 	"google.golang.org/protobuf/proto"
 )
 
-var (
-	ErrInvalidBranchName = kvfs.ErrInvalidBranchName
-	ErrNilMutation       = errors.New("segment: nil mutation")
-)
+var ErrNilMutation = errors.New("segment: nil mutation")
 
 type Writer struct {
 	w *recordio.Writer
@@ -28,9 +24,6 @@ func (w *Writer) Write(m *storagepb.DocumentMutation) error {
 	if m == nil {
 		return ErrNilMutation
 	}
-	if err := kvfs.ValidateBranch(m.Branch); err != nil {
-		return err
-	}
 	data, err := proto.Marshal(m)
 	if err != nil {
 		return fmt.Errorf("segment write: marshal: %w", err)
```

---

### Medium

[`segment/writer.go:20-42`](file:///Users/ali/workspace/iampat/cloudy-neigh/segment/writer.go#L20-L42): Reuse byte buffer with `MarshalAppend` in `segment.Writer`.

`proto.Marshal` allocates a new slice on every call. Storing a reusable buffer on `Writer` avoids heap allocations during memtable flush.

```diff
diff --git a/segment/writer.go b/segment/writer.go
index 47f8702..cd34842 100644
--- a/segment/writer.go
+++ b/segment/writer.go
@@ -13,6 +13,7 @@ var ErrNilMutation = errors.New("segment: nil mutation")
 
 type Writer struct {
 	w *recordio.Writer
+	buf []byte
 }
 
 func NewWriter(w io.Writer) *Writer {
@@ -24,11 +25,12 @@ func (w *Writer) Write(m *storagepb.DocumentMutation) error {
 	if m == nil {
 		return ErrNilMutation
 	}
-	data, err := proto.Marshal(m)
+	var err error
+	w.buf, err = proto.MarshalOptions{}.MarshalAppend(w.buf[:0], m)
 	if err != nil {
 		return fmt.Errorf("segment write: marshal: %w", err)
 	}
-	if _, _, err := w.w.WriteRecord(data); err != nil {
+	if _, _, err := w.w.WriteRecord(w.buf); err != nil {
 		return fmt.Errorf("segment write: %w", err)
 	}
 	return nil
```

---

[`segment/segment_test.go:242-250`](file:///Users/ali/workspace/iampat/cloudy-neigh/segment/segment_test.go#L242-L250): Move `segment.NewWriter` outside `b.Loop()`.

Instantiating `NewWriter` inside the loop allocates a 64KB buffer on every iteration. Setup must sit outside the loop.

```diff
diff --git a/segment/segment_test.go b/segment/segment_test.go
index 6fbf918..13876cd 100644
--- a/segment/segment_test.go
+++ b/segment/segment_test.go
@@ -240,8 +240,8 @@ func BenchmarkWriter(b *testing.B) {
 		Payload: bytes.Repeat([]byte("test-payload-bytes"), 50),
 	}
 
+	w := segment.NewWriter(io.Discard)
 	for b.Loop() {
-		w := segment.NewWriter(io.Discard)
 		if err := w.Write(m); err != nil {
 			b.Fatalf("Write failed: %v", err)
 		}
```

---

### Low

[`segment/segment_test.go:287-400`](file:///Users/ali/workspace/iampat/cloudy-neigh/segment/segment_test.go#L287-L400): Delete redundant and fabricated tests.

`TestBranchIsolation` tests standard Go map operations. `TestWriterCloseCloser` tests `recordio.Writer.Close`, which is tested in `recordio`. `TestBranchValidation` is obsolete once branch validation moves to the API boundary.

```diff
diff --git a/segment/segment_test.go b/segment/segment_test.go
index 13876cd..d527a29 100644
--- a/segment/segment_test.go
+++ b/segment/segment_test.go
@@ -285,116 +285,9 @@ func BenchmarkReader(b *testing.B) {
 	}
 }
 
-func TestBranchValidation(t *testing.T) {
-	valid := []string{"main", "dev_1", "feature-branch", "Staging_v2-test"}
-	for _, branch := range valid {
-		var buf bytes.Buffer
-		w := segment.NewWriter(&buf)
-		m := &storagepb.DocumentMutation{
-			Branch: branch,
-			DocId:  "doc-1",
-			Op:     storagepb.MutationOp_PUT,
-		}
-		if err := w.Write(m); err != nil {
-			t.Errorf("expected valid branch %q, got error: %v", branch, err)
-		}
-	}
-
-	invalid := []string{
-		"",
-		"features/search",
-		"/root",
-		"123num",
-		"-dash",
-		"_under",
-		"branch space",
-		"tag@1",
-	}
-	for _, branch := range invalid {
-		var buf bytes.Buffer
-		w := segment.NewWriter(&buf)
-		m := &storagepb.DocumentMutation{
-			Branch: branch,
-			DocId:  "doc-1",
-			Op:     storagepb.MutationOp_PUT,
-		}
-		err := w.Write(m)
-		if !errors.Is(err, segment.ErrInvalidBranchName) {
-			t.Errorf("expected ErrInvalidBranchName for %q, got %v", branch, err)
-		}
-	}
-
+func TestNilMutation(t *testing.T) {
 	w := segment.NewWriter(&bytes.Buffer{})
 	if err := w.Write(nil); !errors.Is(err, segment.ErrNilMutation) {
-		t.Errorf("expected ErrNilMutation, got %v", err)
-	}
-}
-
-func TestBranchIsolation(t *testing.T) {
-	mutations := []*storagepb.DocumentMutation{
-		{Branch: "branch_a", DocId: "doc-1", Op: storagepb.MutationOp_PUT, Payload: []byte("v1")},
-		{Branch: "branch_b", DocId: "doc-1", Op: storagepb.MutationOp_PUT, Payload: []byte("v1-b")},
-		{Branch: "branch_a", DocId: "doc-1", Op: storagepb.MutationOp_DELETE},
-		{Branch: "branch_b", DocId: "doc-1", Op: storagepb.MutationOp_PUT, Payload: []byte("v2-b")},
-	}
-
-	var buf bytes.Buffer
-	w := segment.NewWriter(&buf)
-	for _, m := range mutations {
-		if err := w.Write(m); err != nil {
-			t.Fatalf("Write failed: %v", err)
-		}
-	}
-	if err := w.Close(); err != nil {
-		t.Fatalf("Close failed: %v", err)
-	}
-
-	branchState := make(map[string]map[string]*storagepb.DocumentMutation)
-	r := segment.NewReader(&buf)
-	for {
-		m, err := r.Next()
-		if errors.Is(err, io.EOF) {
-			break
-		}
-		if err != nil {
-			t.Fatalf("Next failed: %v", err)
-		}
-		if _, ok := branchState[m.Branch]; !ok {
-			branchState[m.Branch] = make(map[string]*storagepb.DocumentMutation)
-		}
-		if m.Op == storagepb.MutationOp_DELETE {
-			delete(branchState[m.Branch], m.DocId)
-		} else {
-			branchState[m.Branch][m.DocId] = m
-		}
-	}
-
-	if _, exists := branchState["branch_a"]["doc-1"]; exists {
-		t.Fatal("expected doc-1 to be deleted in branch_a")
-	}
-	docB, exists := branchState["branch_b"]["doc-1"]
-	if !exists {
-		t.Fatal("expected doc-1 to exist in branch_b")
-	}
-	if string(docB.Payload) != "v2-b" {
-		t.Fatalf("expected v2-b, got %s", string(docB.Payload))
-	}
-}
-
-func TestWriterCloseCloser(t *testing.T) {
-	cw := &closingWriter{}
-	w := segment.NewWriter(cw)
-	if err := w.Close(); err != nil {
-		t.Fatalf("Close failed: %v", err)
-	}
-	if !cw.closed {
-		t.Fatal("expected underlying closer to be closed")
+		t.Fatalf("expected ErrNilMutation, got %v", err)
 	}
 }
-
-type closingWriter struct {
-	closed bool
-}
-
-func (c *closingWriter) Write(p []byte) (int, error) { return len(p), nil }
-func (c *closingWriter) Close() error                { c.closed = true; return nil }
```

---

[`.claude/CLAUDE.md:172-174`](file:///Users/ali/workspace/iampat/cloudy-neigh/.claude/CLAUDE.md#L172-L174), [`.claude/skills/consult-petr/SKILL.md:131`](file:///Users/ali/workspace/iampat/cloudy-neigh/.claude/skills/consult-petr/SKILL.md#L131): Move `.claude` agent changes into a separate PR.

Commit `fda8080` bundles agent instructions into a storage engine PR. Keep feature branches isolated from workflow configuration edits.

```diff
diff --git a/.claude/CLAUDE.md b/.claude/CLAUDE.md
index aea4e04..4463f12 100644
--- a/.claude/CLAUDE.md
+++ b/.claude/CLAUDE.md
@@ -169,9 +169,6 @@ not trust memory over the help output.
   the same turn.
 - An agy review transcript lives in `docs/reviews/<date>-<topic>.md` for the
   pull request only. `git rm` it before the merge.
-- When asked for a review, start immediately without asking for approval.
-  Let the reviewer persona decide what to focus on and how to review, unless
-  the user gives an explicit instruction.
 - Ideas and brainstorms: `docs/random-thoughts.md` catalogs external
   algorithms, references, and brainstorming outcomes. Keep an index at the top
   with a one-to-two sentence summary per entry. The user approves every edit
diff --git a/.claude/skills/consult-petr/SKILL.md b/.claude/skills/consult-petr/SKILL.md
index d403d56..13962dc 100644
--- a/.claude/skills/consult-petr/SKILL.md
+++ b/.claude/skills/consult-petr/SKILL.md
@@ -128,7 +128,6 @@ how the design went.
 - A bug or invariant failure in a complex algorithmic scenario.
 - A hot path where the constant factor decides the latency target.
 - A correctness argument that needs a proof, not a test.
-- Reviewing code or a PR. Start immediately without asking for approval. Let Petr decide what to focus on and how to review, unless instructed otherwise.
 
 Do not ask for plumbing, glue code, configuration, or a problem the standard
 library already answers.
```

