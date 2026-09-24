# Tenant Storage Isolation

**Status:** Draft, 2026-09-23

## Scope

This note defines physical storage isolation for tenants. Each tenant gets a dedicated storage root URL.

## Non-goals

- Cross-tenant data sharing or federated queries.
- Cloud IAM credential exchange. Cloudy uses ambient credentials for all storage URLs.

## Future work

- Workload Identity and cloud IAM role impersonation.
- Customer-managed encryption keys per tenant.
- Namespace storage quotas in a tenant.

## Model

The control plane stores tenant locations in a central file. Each tenant controls an isolated storage tree.

```text
┌────────────────────────────────────────────────────────┐
│                   Global Control Plane                 │
│                      (tenants.json)                    │
│   tenant: "acme"        ▶  gs://acme-bucket/data/      │
│   tenant: "krusty-krab" ▶  file:///data/krusty/        │
└───────────────────────────┬────────────────────────────┘
                            │
              ┌─────────────┴─────────────┐
              ▼                           ▼
┌───────────────────────────┐ ┌───────────────────────────┐
│        Tenant Acme        │ │    Tenant Krusty Krab     │
│   gs://acme-bucket/data/  │ │    file:///data/krusty/   │
│   ├── ns.json             │ │    ├── ns.json            │
│   └── ns/                 │ │    └── ns/                │
└───────────────────────────┘ └───────────────────────────┘
```

## Decisions

### Tenant Catalog Schema

The control plane maintains a `tenants.json` file in master storage.
It maps tenant identifiers to storage root URLs.

```json
{
  "version": 1,
  "tenants": {
    "acme": {
      "storage_url": "gs://acme-bucket/data"
    },
    "krusty-krab": {
      "storage_url": "file:///data/krusty"
    }
  }
}
```

A map structure gives O(1) lookup by tenant ID. It prevents duplicate registrations.

### Storage Root Layout

A tenant storage root holds only data for that tenant.
The root contains `ns.json` and the `ns/` namespace tree directly.
The path drops the `<tenant>/` prefix because the storage URL isolates the root.

```text
<tenant-storage-root>/
├── ns.json
└── ns/<namespace>/<epoch>/
    ├── refs/heads/
    ├── segments/
    └── wal/
```

### In-Memory Tenant Registry

Cloudy nodes cache open `objectstore.Store` instances in memory.
A lookup uses an atomic pointer or read-write lock.
The query path never parses storage URLs during request execution.

### Request Routing

Each request specifies the tenant identifier.
Ingest and query handlers resolve the tenant in the registry before accessing storage.
Unknown tenants fail fast with a not found error.

## Open

`CONSIDER(ali):` Pass tenant ID through a protobuf field or a gRPC metadata header.
`CONSIDER(ali):` Define how flusher workers discover newly registered tenants without a process restart.
