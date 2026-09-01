# Branching Key-Value Filesystem (KVFS)

## 1. Architecture
KVFS is Layer 2 of the storage system. It provides a Git-like, Copy-on-Write (CoW) key-value filesystem on top of LogStream and Cloud Object Storage.

```text
[Bucket Root]
├── refs/heads/<branch>            --> Pointer to current Manifest Hash
├── manifests/<m0>/<m1>/<hash>     --> Content-addressed manifest JSON
├── objects/<h0>/<h1>/<hash>       --> Content-addressed raw payload blobs
└── wal/<branch>/<seq>.recordio    --> Branch mutation log (Layer 1)
```

## 2. Content-Addressed Storage (CAS)
- **Objects (`objects/<hash>`)**: Raw file payloads stored by SHA-256 hash. Immutable and written once.
- **Manifests (`manifests/<hash>`)**: Key-to-hash mappings representing filesystem snapshots.
  ```json
  {
    "last_wal_seq": 42,
    "entries": {
      "docs/readme.md": "a3f1c890...",
      "src/main.go": "b70c34e9..."
    }
  }
  ```

## 3. Branch Operations
- **`BRANCH(new_branch, parent_branch)`**:
  1. Reads `refs/heads/<parent_branch>` to get the latest manifest hash `M_parent`.
  2. Writes `refs/heads/<new_branch>` with value `M_parent` using `if-generation-match=0`.
  3. Emits `BRANCH_CREATED` to `wal/_meta/`.
  4. Instant $O(1)$ operation without copying blobs or manifests.
- **`PUT(branch, path, data)`**:
  1. Uploads `data` to `objects/<hash>`.
  2. Appends mutation to `wal/<branch>/`.
  3. Periodically flushes new `manifests/<hash>` and conditionally updates `refs/heads/<branch>`.
