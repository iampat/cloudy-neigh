# Tasks

## Namespace & Branch Operations

- [X] Add RPC to create a new empty namespace.
  - Writes the initial manifest directly to object storage.
  - Does not require WAL linearizability.
- [X] Add Fork RPC to allow clients to fork an existing branch to create a new branch.
  - Fails with `codes.NotFound` if the parent branch does not exist in object storage at call time.
  - Document this simplicity decision justified by low fork event rate.
  - Sequenced via WAL for linearizability across mutations.
- [X] Support default namespace: if `namespace` is not set in `Upsert`, `Fork`, or `Delete`, default to `"default"`.

## Implementation Plan

### Phase 1: API Contract & Protobuf Schemas
- Define `CreateNamespaceRequest` and `CreateNamespaceResponse` in `proto/cloudyneigh/v1/index.proto`.
- Define `ForkRequest` (`source_namespace`, `target_namespace`) and `ForkResponse` in `index.proto`.
- Add `CreateNamespace` and `Fork` RPCs to `IngestService`.
- Expose `DefaultNamespace = "default"` constant in `namespace/namespace.go`.

### Phase 2: Storage & Ingestion Pipeline
- `kvfs.CreateEmptyBranch`:
  - Atomically write empty `BranchManifest` with `Condition{Absent: true}`.
- `ingest.BatchIngester`:
  - Require `objectstore.Store` in constructor.
  - `CreateNamespace`: Execute `kvfs.CreateEmptyBranch`.
  - `Fork`: Verify parent exists in storage. Check target is absent. Write target manifest via `kvfs.CreateBranch`.
  - Append `BranchLifecycleEvent{Type: FORK}` to WAL stream for deterministic ordering.
- `ingest.Flusher`:
  - Consume `BranchLifecycleEvent_FORK` from WAL stream.
  - Flush any active parent memtable mutations to segments.
  - Record target branch checkpoint sequence.
- `grpcapi.IngestServer`:
  - Update `Ingester` interface to include `CreateNamespace` and `Fork`.
  - Default empty namespace to `"default"` in `Upsert`, `Delete`, `CreateNamespace`, and `Fork.source_namespace`.
  - Validate names via `namespace.ValidateNamespace`.
  - Map errors to canonical gRPC status codes (`codes.NotFound`, `codes.AlreadyExists`, `codes.InvalidArgument`).

### Phase 3: Verification & Concurrency Checks
- Unit test `kvfs.CreateEmptyBranch` in `kvfs/branch_test.go`.
- Unit test `BatchIngester.CreateNamespace` and `BatchIngester.Fork` in `ingest/ingester_test.go`.
- Unit test `IngestServer` default namespace fallback, validation, and error mappings in `grpcapi/ingest_test.go`.
- Verify full test suite passes with race detection (`bazel test --config=race //...`).

### Phase 4: Systems Analysis & Bottlenecks
- Ingest service instances remain stateless. Parent branch existence is validated against storage.
- Zero-copy branching preserves storage efficiency by sharing immutable segment references.
- Atomic conditional creates (`Absent: true`) eliminate distributed locks across replicas.

## Design Decisions & Thought Process

### Linearizability & WAL Sequencing
- `Upsert`, `Fork`, and `Delete` require linearizability across data mutations.
- `CreateNamespace` creates an empty manifest directly in object storage. It does not require WAL linearizability.

### Missing Parent Branch Handling on Fork
- If the parent branch does not exist in object storage at the moment of the RPC call, fail immediately with `codes.NotFound`.
- An in-memory uncommitted branch registry was considered and rejected. It requires state synchronization across multiple running service replicas.
- Direct storage checks keep ingest service instances completely stateless.
- This design decision favors simplicity. It is justified by the low rate of fork operations.
- Clients must ensure parent manifests are committed before forking.

### Default Namespace
- `Upsert`, `Fork`, and `Delete` fall back to `"default"` when the namespace field is omitted.
