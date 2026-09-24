# Tenant Storage Isolation

**Status:** Draft, 2026-09-23

## Scope

This note defines physical storage isolation for tenants. Each tenant gets a dedicated storage root URL.

## Non-goals

- Dynamic tenant discovery at runtime. Changing tenants requires a service restart.
- Cross-tenant data sharing or federated queries.
- Cloud IAM credential exchange. Cloudy uses ambient credentials for all storage URLs.

## Future work

- Move tenant resolution from protobuf message fields to gRPC metadata headers and authentication tokens.
- Workload Identity and cloud IAM role impersonation.
- Customer-managed encryption keys per tenant.
- Namespace storage quotas in a tenant.

## Model

The service loads tenant storage roots at startup. Each tenant controls an isolated storage tree.

```text
┌────────────────────────────────────────────────────────┐
│                   Static Configuration                 │
│                (--tenants-file=tenants.json)           │
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

### Static Tenant Catalog Schema

Cloudy loads tenant mappings at boot from a file passed by a flag.
The JSON maps tenant identifiers to storage root URLs.
The map key identifies the tenant.
An inner tenant identifier field is redundant and omitted.
We omit a version field because static configuration needs no optimistic concurrency control.

```json
{
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

### Service Configuration Flag

The CLI takes a `--tenants-file=<path>` flag on `ingest` and `query` subcommands.
Cloudy reads and unmarshals this JSON file during server initialization.
If the file path is invalid or unmarshaling fails, the server stops immediately.

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

Cloudy nodes open and cache `objectstore.Store` instances at startup.
The registry stores instances in a read-only map.
The query path never parses storage URLs during request execution.

### Request Routing

Clients pass the tenant identifier via `x-tenant-id` in gRPC metadata.
A server unary interceptor extracts and validates the tenant into the context.
Protobuf request messages contain no tenant field.
Ingest and query handlers resolve the tenant store from the registry using the context.
Missing or invalid tenants fail fast with an error.
