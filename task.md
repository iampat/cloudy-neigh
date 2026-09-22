# Tasks

## Namespace & Branch Operations

- [X] Automatic namespace creation on ingest write.
  - Namespaces are created empty on initial document ingestion when WAL records are flushed to storage.
  - No explicit `CreateNamespace` RPC is needed.
- [X] Add Fork RPC to allow clients to fork an existing branch to create a new branch.
  - Scoped by namespace: `namespace`, `source_branch`, and `target_branch`.
  - Fails with `codes.NotFound` if the parent branch does not exist in object storage at call time.
  - Sequenced via WAL for linearizability across mutations.
- [X] Support default namespace and branch scoping:
  - If `namespace` is not set in `Upsert`, `Fork`, or `Delete`, default to `"default"`.
  - If `branch` is not set in `Upsert`, `Delete`, `Query`, or `Fork.source_branch`, default to `"main"`.

## Implementation Plan

### Phase 1: API Contract & Protobuf Schemas
- Add `branch` field to `UpsertRequest`, `DeleteRequest`, and `QueryRequest` in `proto/cloudyneigh/v1/index.proto`.
- Define `ForkRequest` (`namespace`, `source_branch`, `target_branch`) and `ForkResponse` in `index.proto`.
- Add `Fork` RPC to `IngestService`.
- Expose `DefaultNamespace = "default"` and `DefaultBranch = "main"` constants and `BranchKey` helper in `namespace/namespace.go`.

### Phase 2: Storage & Ingestion Pipeline
- `ingest.Ingester`:
  - `Fork`: Verify parent exists in storage. Check target is absent. Write target manifest via `kvfs.CreateBranch`.
  - Append `BranchLifecycleEvent{Type: FORK}` to WAL stream for deterministic ordering. Roll back branch creation if append fails.
- `ingest.Flusher`:
  - Consume `BranchLifecycleEvent_FORK` from WAL stream.
  - Flush any active parent memtable mutations to segments.
  - Record target branch checkpoint sequence.
  - Automatically create branch manifests on initial flush when resolving absent refs.
- `grpcapi.IngestServer`:
  - Update `Ingester` interface to include `Fork`.
  - Initialize variables with fallback defaults first, then override with provided values per `go.md`.
  - Default empty namespace to `"default"` in `Upsert`, `Delete`, and `Fork`.
  - Map errors to canonical gRPC status codes (`codes.NotFound`, `codes.AlreadyExists`, `codes.InvalidArgument`).

### Phase 3: Verification & Concurrency Checks
- Unit test `BatchIngester.Fork` in `ingest/ingester_test.go`.
- Unit test `IngestServer` default namespace fallback, branch validation, and error mappings in `grpcapi/ingest_test.go`.
- Unit test `QueryServer` branch routing in `grpcapi/query_test.go`.
- Verify full test suite passes with race detection (`bazel test --config=race //...`).

### Phase 4: Systems Analysis & Bottlenecks
- Ingest service instances remain stateless. Parent branch existence is validated against storage.
- Zero-copy branching preserves storage efficiency by sharing immutable segment references.
- Atomic conditional creates (`Absent: true`) eliminate distributed locks across replicas.

## Design Decisions & Thought Process

### Implicit Namespace Creation
- Namespaces are collections that appear on first write.
- When ingesting into a new namespace, flusher creates the branch manifest in object storage upon flush.
- Explicit creation RPCs add unnecessary external state coordination.

### Branch Scoping within Namespaces
- Namespaces are always created as empty.
- Forking creates a new branch divergence point from an existing parent branch.
- Storage keys map cleanly via `namespace.BranchKey(ns, branch)`.
- Default namespace `"default"` maps directly to the root branch identifier (`"main"`). Non-default namespaces are prefixed (`<ns>_<branch>`).

### Missing Parent Branch Handling on Fork
- If the parent branch does not exist in object storage at the moment of the RPC call, fail immediately with `codes.NotFound`.
- Direct storage checks keep ingest service instances completely stateless.
- This design decision favors simplicity, justified by the low rate of fork operations.
