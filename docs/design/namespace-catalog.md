# Multi-Tenant Namespace Catalog

## Problem

Cloudy-neigh needs clear multi-tenant isolation and namespace lifecycle management.
Creating a namespace should not depend on write-ahead logs (WAL).
Ingest nodes must validate namespaces with zero storage round trips.
Deleting and recreating a namespace must avoid storage path collisions.

## Storage Layout

Storage separates the control-plane catalog from the data-plane storage tree.

```text
<tenant>/
├── ns.json
└── ns/<namespace>/<epoch>/
    ├── refs/heads/
    │   ├── <branch-a>
    │   └── <branch-b>
    ├── segments/
    │   ├── <segment-1>.seg
    │   └── <segment-2>.seg
    └── wal/
        ├── 000001.log
        └── 000002.log
```

### Hierarchy

1. **Tenant** (`<tenant>/`):
   - Strongest isolation boundary.
   - Represents an organization or billing account.
   - Cross-tenant access is strictly prohibited.
2. **Catalog** (`<tenant>/ns.json`):
   - Single control-plane metadata object per tenant.
   - Maps namespace names to metadata and active epochs.
3. **Namespace Epoch** (`ns/<namespace>/<epoch>/`):
   - Autonomous dataset with dedicated WAL and segment store.
   - `<epoch>` increments upon recreation to isolate data lifecycles.
4. **Branch** (`refs/heads/<branch>`):
   - Lightweight reference pointer to a branch manifest.
   - Branches inside the same namespace share immutable segments.

## Catalog Schema (`ns.json`)

The catalog stores tenant namespaces in Protobuf or JSON.

```json
{
  "tenant": "acme-corp",
  "version": 4,
  "namespaces": {
    "catalog": {
      "status": "ACTIVE",
      "epoch": 0,
      "created_at": 1774000000
    },
    "analytics": {
      "status": "DELETED",
      "epoch": 1,
      "created_at": 1773000000,
      "deleted_at": 1773500000
    }
  }
}
```

## Lifecycle Protocols

### Namespace Creation

1. Ingest node reads `<tenant>/ns.json` with storage generation.
2. If the namespace exists in the catalog (active or deleted), return `AlreadyExists`.
3. Set `epoch = 0` and status to `ACTIVE`.
4. Issue conditional write (CAS) on `<tenant>/ns.json`.
5. If precondition fails, retry read-modify-write loop.

### Namespace Deletion

1. Set namespace status to `DELETED` in `<tenant>/ns.json` via CAS.
2. Ingest nodes reject new writes immediately.
3. Recreating a deleted namespace name is disallowed in the current milestone.
4. Asynchronous garbage collector purges `ns/<namespace>/0/`.

### Ingest Validation

Ingest replicas avoid storage lookups during writes:
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
    │   └── 0/
    │       ├── refs/heads/
    │       │   ├── main
    │       │   └── experiment
    │       ├── segments/
    │       │   ├── seg-0001.seg
    │       │   └── seg-0002.seg
    │       └── wal/
    │           └── 000001.log
    └── analytics/
        └── 1/
            ├── refs/heads/
            │   └── main
            ├── segments/
            │   └── seg-0101.seg
            └── wal/
                └── 000001.log

krusty-krab/
├── ns.json
└── ns/
    └── menu/
        └── 0/
            ├── refs/heads/
            │   └── main
            ├── segments/
            │   └── seg-0201.seg
            └── wal/
                └── 000001.log
```
