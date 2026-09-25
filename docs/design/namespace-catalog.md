# Multi-Tenant Namespace Catalog

## Problem

Cloudy-neigh needs clear multi-tenant isolation and namespace lifecycle management.
Creating a namespace does not depend on write-ahead logs (WAL).
Ingest nodes must validate namespaces with zero storage round trips.
Deleting and recreating a namespace must avoid storage path collisions.

## Storage Layout

Storage separates the control-plane catalog from the data-plane storage tree.
In a dedicated tenant store, the root URL isolates the tenant.
In a shared store, paths nest under `<tenant>/`.

```text
<tenant-storage-root>/
├── ns.json
└── ns/<namespace>/
    ├── branches.json
    ├── refs/heads/
    │   ├── <branch-a>.json
    │   └── <branch-b>.json
    ├── segments/
    │   ├── 00000000000000000001.recordio
    │   └── 00000000000000000002.recordio
    └── wal/
        ├── 00000000000000000001.recordio
        └── 00000000000000000002.recordio
```

### Hierarchy

1. **Tenant** (`<tenant-storage-root>/` or `<tenant>/`):
   - Strongest isolation boundary.
   - Represents an organization or billing account.
   - Cross-tenant access is strictly prohibited.
2. **Catalog** (`ns.json` or `<tenant>/ns.json`):
   - Single control-plane metadata object per tenant.
   - Maps namespace names to metadata.
3. **Namespace** (`ns/<namespace>/`):
   - Autonomous dataset with dedicated WAL and segment store.
   - No key holds an epoch. `NamespaceMetadata.epoch` is always 0.
4. **Branch** (`refs/heads/<branch>.json`):
   - The branch manifest, a `BranchManifest` in protojson.
   - Branches inside the same namespace share immutable segments.
   - `branches.json` lists the branch names of the namespace.

## Catalog Schema (`ns.json`)

The catalog is a `TenantCatalog` message in `proto/namespace/v1/catalog.proto`,
stored as protojson. A nonzero `deleted_at` marks a namespace deleted.

```json
{
  "namespaces": {
    "catalog": {
      "created_at": "1774000000"
    },
    "analytics": {
      "created_at": "1773000000",
      "deleted_at": "1773500000"
    }
  }
}
```

The catalog has no version and no status field. The storage generation
guards each write, and `deleted_at` holds the lifecycle state.

## Branch Catalog Schema (`branches.json`)

Each namespace has one `branches.json`. It is a `BranchCatalog` message in
`proto/namespace/v1/catalog.proto`, stored as protojson.

```json
{
  "branches": {
    "main": {},
    "feature-1": {}
  }
}
```

The map key is the branch name, not a storage key. Package `namespace` owns
the key layout. A caller asks `namespace.Scope.ManifestKey(branch)` for
the manifest key. A missing or empty catalog lists `main` alone.

## Lifecycle Protocols

### Namespace Creation

The ingester creates a namespace on its first write to that namespace. It
ignores `AlreadyExists`.

1. Read `<tenant>/ns.json` with storage generation.
2. If the namespace exists in the catalog (active or deleted), return `AlreadyExists`.
3. Set `created_at`.
4. Issue conditional write (CAS) on `<tenant>/ns.json`.
5. If precondition fails, retry read-modify-write loop.

### Namespace Deletion

1. `namespace.DeleteNamespace` sets `deleted_at` in `<tenant>/ns.json` with CAS.
   No RPC calls it yet.
2. The flusher and the query engine skip a deleted namespace.
3. Recreating a deleted namespace name returns `AlreadyExists`.

Future work:

- Ingest nodes reject writes to a deleted namespace.
- An asynchronous garbage collector purges `ns/<namespace>/`.

### Ingest Validation

Future work. `namespace.CatalogCache` implements the steps, but no server uses
it yet. Ingest replicas avoid storage lookups during writes:
1. Replicas maintain an in-memory `CatalogCache`.
2. Every write validates the target namespace against the cache.
3. On cache miss, read `<tenant>/ns.json` once to refresh.
4. Background worker syncs the cache every 60 seconds.

## Sample Multi-Tenant Hierarchy

```text
acme-corp/
├── ns.json
└── ns/
    ├── catalog/
    │   ├── branches.json
    │   ├── refs/heads/
    │   │   ├── main.json
    │   │   └── experiment.json
    │   ├── segments/
    │   │   ├── 00000000000000000001.recordio
    │   │   └── 00000000000000000002.recordio
    │   └── wal/
    │       ├── 00000000000000000001.recordio
    │       └── 00000000000000000002.recordio
    └── analytics/
        ├── branches.json
        ├── refs/heads/
        │   └── main.json
        ├── segments/
        │   └── 00000000000000000101.recordio
        └── wal/
            ├── 00000000000000000001.recordio
            └── 00000000000000000101.recordio

krusty-krab/
├── ns.json
└── ns/
    └── menu/
        ├── branches.json
        ├── refs/heads/
        │   └── main.json
        ├── segments/
        │   └── 00000000000000000201.recordio
        └── wal/
            ├── 00000000000000000001.recordio
            └── 00000000000000000201.recordio
```
