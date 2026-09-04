# First customer demo

**Status:** Draft, 2026-09-04, v0

## What this is

The plan for the first end-to-end demo. The customer knows the system is
early. Slow is fine. Fake is not: the demo runs the product dataflow on
the product datastore. Every part is a bare-minimum implementation. We
replace each part later, one at a time, when it must change.

## Dataflow

```
client ──gRPC Write──▶ ingest process ──append──▶ WAL (logstream)
                            │ tails WAL
                            ▼
                  per-namespace memtable
                            │ flush on threshold
                            ▼
          segment on objectstore + manifest CAS (refs/heads/<ns>)
                            │
client ──gRPC Query──▶ query process
                       polls manifest, loads segments,
                       exact cosine k-NN + Eq filter
```

A namespace maps to the `branch` field of `WalRecord`. Two processes,
storage in the middle. The query engine never reads the WAL.

## Components

| Part | What it does |
| --- | --- |
| `proto/cloudyneigh/v1/index.proto` | `Write`, `Query`, minimal messages |
| `segment/` | write and read one segment file |
| `ingest/` | WAL consumer, memtable, flush, manifest commit |
| `query/` | manifest poll, segment load, k-NN, filter |
| `grpcapi/` | the two service implementations |
| `restgw/` | JSON-to-gRPC translation, demo search, web page |
| `cmd/cloudyd` | one binary, `ingest` and `query` subcommands |
| `scripts/demoload.py` | stream the corpus into `Write` calls |
| `examples/search.py` | Python client example on the REST API |

Existing code stays as is: `objectstore`, `recordio`, `logstream`,
`kvfs/branch.go`, `proto/storage/v1`.

## Corpus

Cohere Wikipedia, 1M documents, 1024-dim vectors, cosine. It is the
milestone-1 dataset of `docs/benchmark/turbopuffer.md`, it fits in RAM,
and the multilingual text gives a natural `lang=en` filter demo.

## Simplifications

All honest, all replaced later behind a stable boundary.

- A segment is one recordio file of `DocumentMutation` protos. No
  columnar split, no footer, no bloom filter, no block index.
- No compaction. Deletes ride in segments. The query engine applies
  latest-wins by WAL sequence.
- Async ingestion only. A document becomes visible after flush, in
  seconds. No read-after-write.
- The query engine downloads whole segments into memory. Exact scan,
  no index.
- One `Retrieve` node per query: vector rank, one `Eq` filter, `top_k`.
  No fusion, no pagination, no schema evolution.

## Milestones

### M1: wire format and segments

- [ ] Write `proto/cloudyneigh/v1/index.proto`: `Write`, `Query`,
      document with id, attributes, one vector. `WriteRequest` carries
      repeated documents.
- [ ] Add `uint64 seq` to `DocumentMutation`. The consumer stamps it
      at flush time as `wal_seq << 32 | record_index`.
- [ ] `segment/`: writer that dumps a memtable to one recordio file of
      `DocumentMutation` protos, and a reader.
- [ ] Tests: roundtrip, latest-wins order, torn tail.

### M2: ingestion engine

- [ ] `ingest/`: consumer that tails the WAL from `checkpoint_seq`,
      routes by namespace into memtables.
- [ ] Flush on threshold: upload segment, CAS the manifest with
      `kvfs.UpdateBranch`, advance `checkpoint_seq`.
- [ ] `grpcapi/`: `Write` encodes `WalRecord` and appends to logstream.
- [ ] `cloudyd ingest` subcommand.
- [ ] On a 412 from the manifest CAS, reload the ref and retry.
- [ ] Tests: flush, restart resume, crash between upload and CAS.

### M3: query engine

- [ ] `query/`: poll the manifest, download segments, build the
      in-memory view with latest-wins.
- [ ] Vectors in one contiguous `[]float32`, parallel slices for doc
      ids and attributes. No per-document heap pointers.
- [ ] Exact cosine k-NN with goroutine fan-out, `Eq` filter.
- [ ] `grpcapi/`: `Query`.
- [ ] `cloudyd query` subcommand.
- [ ] Tests: k-NN correctness on a tiny corpus, delete visibility,
      manifest refresh picks up a new segment.

### M4: REST gateway and demo tooling

- [ ] `restgw/`: JSON translation for write and query.
- [ ] `POST /demo/search`: embed the query text with the Cohere API,
      key from the environment, then call `Query`.
- [ ] Static web page: query box, `lang` filter, results with title
      and snippet.
- [ ] `scripts/demoload.py`: read Cohere Wikipedia from local disk cache
      into `Write` calls, 1,000 documents per call. Reader is done;
      `send_batch` stub awaits `grpcapi/`.
- [X] Check whether the Hugging Face CLI covers the local dataset
      cache. Verified and automated via `just download-dataset`.
- [ ] `examples/search.py`: plain `requests`, no gRPC stubs.

### M5: demo run

- [ ] Load 1M documents into a GCS bucket. Record the load time.
- [ ] Dry-run the full script below. Record query latency.
- [ ] Rehearse the kill-and-restart step for both processes.

### M6: benchmark (optional, only if time remains)

- [ ] The six HTTP endpoints from `docs/benchmark/turbopuffer.md`.
- [ ] Run `vector-knn-1m-hot.toml` against us.

## Demo script

1. Show the empty GCS bucket. Start both processes.
2. Run `demoload`. Show WAL segments and then data segments appear in
   the bucket.
3. Open the web page. Type a question. Show results. Add `lang=en`.
4. Run `examples/search.py` for the API view.
5. `kill -9` both processes. Restart. Query again. All state came back
   from object storage alone.
6. Optional: show benchmark numbers from M6.

## Open questions

- CONSIDER(ali): flush threshold for the demo. 10k documents or 10 s,
  whichever comes first, is the starting guess.
