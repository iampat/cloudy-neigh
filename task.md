# Tasks

## Namespace & Branch Operations

- [ ] Add RPC to create a new empty namespace.
  - Writes the initial manifest directly to object storage.
  - Does not require WAL linearizability.
- [ ] Add Fork RPC to allow clients to fork an existing branch to create a new branch.
  - Fails with `codes.NotFound` if the parent branch does not exist in object storage at call time.
  - Document this simplicity decision justified by low fork event rate.
  - Sequenced via WAL for linearizability across mutations.
- [ ] Support default namespace: if `namespace` is not set in `Upsert`, `Fork`, or `Delete`, default to `"default"`.

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
