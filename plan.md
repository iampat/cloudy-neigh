# Query RPC with exact k-NN: implementation plan

**Status:** Approved, 2026-09-11. Design review: jeff-dean. Implementation
plan: petr. Split into four pull requests, stacked in order.

## Decisions

- `QueryRequest` gains one field: `string vector_column = 5`. Unset means
  `"default"`. No metric field. The metric belongs to the namespace schema,
  which arrives later. The server uses cosine until then.
- A background goroutine syncs the tables every `-sync-interval` seconds. A
  query never does remote I/O and never blocks on a sync.
- The sync is incremental. A generation check skips an unchanged manifest.
  Only unseen segment IDs load, in manifest order. One sync per branch at a
  time.
- The server discovers branches with `kvfs.ListBranches`. Only a branch that
  exists in storage gets state. An unknown namespace returns an empty
  response and leaves nothing behind.
- The loader applies one segment under one table lock. Decoding happens
  outside the lock.
- Top-k selection uses a bounded heap of size `min(k, N)`. Cosine and L2
  squared ascend, dot product descends. Ties break by document ID ascending.

## PR 1: exact k-NN search in the columnar table

- Files: `query/columnar.go`, `query/columnar_test.go`.
- API:

```go
type Metric int

const (
    MetricCosine Metric = iota
    MetricL2Squared
    MetricDotProduct
)

func (t *Table) Search(col string, query []float32, topK int,
    metric Metric, filter *cloudyneighpb.EqualityFilter,
) ([]*cloudyneighpb.ScoredRecord, error)
```

- Scan the packed column under `RLock` with the `query/distance` kernels.
- Skip a tombstoned row, a row without the vector, and a zero stored vector
  under cosine. A missing column returns an empty slice, no error.
- Errors: `ErrDimensionMismatch` on a bad query width,
  `distance.ErrZeroVector` on an all-zero cosine query.
- Tests: each metric, k < N, k = N, k > N, ties, filter hit and miss and
  missing key, tombstones, absent vectors, dimension mismatch, zero vector.
- Depends on: nothing.

## PR 2: batch apply and incremental loader sync

- Files: `query/columnar.go`, `query/columnar_test.go`, `query/loader.go`,
  `query/loader_test.go`.
- API: `func (t *Table) ApplyMutations(...) error`, one write-lock
  acquisition per segment. The loader tracks the last manifest generation
  and a mutex serializes `Sync`.
- Reads and protobuf decoding stay outside the table lock.
- An unchanged generation exits with zero segment reads. Unseen segments
  apply in manifest order.
- Tests: batch atomicity under concurrent readers, generation skip, replay
  order, a later delete masks an earlier put.
- Depends on: nothing. Stacked on PR 1 to avoid conflicts in
  `query/columnar.go`.

## PR 3: proto field and gRPC QueryServer with background sync

- Files: `proto/cloudyneigh/v1/index.proto`, `grpcapi/query.go`,
  `grpcapi/query_test.go`.
- Proto: add `string vector_column = 5` to `QueryRequest`.
- API:

```go
func NewQueryServer(store objectstore.Store, syncInterval time.Duration) (*QueryServer, error)
func (s *QueryServer) Run(ctx context.Context) error
func (s *QueryServer) Query(ctx context.Context, req *cloudyneighpb.QueryRequest) (*cloudyneighpb.QueryResponse, error)
```

- `Run` discovers branches every interval and syncs each one. A sync failure
  logs and serves the last good state.
- `Query` validates the request, reads the branch map under a read lock, and
  searches. Cosine is hardcoded until schema support lands.
- Errors: `InvalidArgument` for empty vector, `top_k` = 0, invalid
  namespace, zero query vector. Unknown namespace returns empty hits.
- Tests: validation cases, unknown namespace, end-to-end ingest then flush
  then sync then query, concurrent queries during sync under the race
  detector.
- Depends on: PR 1 and PR 2.

## PR 4: CLI wiring

- Files: `cmd/cloudy/main.go`.
- Flags on `cloudy query`: `-url` (default
  `file:///tmp/cloudy-demo?create_dir=true`) and `-sync-interval` (default
  `2s`).
- Run the gRPC server and `QueryServer.Run` in an errgroup. On shutdown:
  graceful gRPC stop, cancel the sync loop, close the store.
- Tests: flag validation.
- Depends on: PR 3.
