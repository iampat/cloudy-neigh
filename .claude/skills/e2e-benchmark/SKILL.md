---
name: e2e-benchmark
description: Repeat or extend the 1M-vector end-to-end retrieval benchmark on the Mac and on GCP Emerald Rapids and Granite Rapids VMs. Use it to measure latency, server time, cold start, memory, kernel cost and recall after a scan or kernel change.
allowed-tools: Bash, Read
---

# E2E benchmark

The scripts in `scripts/` run the full benchmark, one command per phase. Each
phase skips the work it finished, so a rerun resumes. Never replace a phase
with ad hoc commands. Change the script, and commit the change.

## What it measures

- Load: `demoload` time and flush time for 1M documents in 1000 segments.
- Server: cold start to the first `sync pass`, and the resident set size (RSS).
- Kernels: `BenchmarkDistance`, `BenchmarkDecode16`, `BenchmarkScanKernel`,
  `BenchmarkSearch*_Table` and `BenchmarkScan1M_Table` on one pinned core.
- Latency: `querybench` per variant, `top_k` 10 and 100, no filter and
  `lang=en`. The report splits each query into server time and client-server
  time. Client-server time is the total minus the `server-time-us` trailer.
- Clients: a local client on each machine, and the Mac as a remote client of
  each VM.
- Correctness: recall@k, top-1 match, maximum score error, wrong-language hits
  and short results against a brute-force ground truth.
- Profiles: a CPU profile of `Scan1M` per machine and of each query server.

Each server runs with `GOMAXPROCS=1` on one vCPU. The SMT sibling of that vCPU
stays idle, and the client runs on the other cores. The Mac cannot pin a core.

## Prerequisites

- The dataset on the Mac in `datasets/cohere-wikipedia`:

  ```sh
  hf download CohereLabs/wikipedia-2023-11-embed-multilingual-v3 \
    en/0000.parquet en/0001.parquet en/0002.parquet en/0003.parquet \
    en/0004.parquet en/0005.parquet en/0006.parquet de/0000.parquet \
    es/0000.parquet fr/0000.parquet --repo-type dataset \
    --local-dir datasets/cohere-wikipedia
  ```

- `gcloud` logged in as `amiri1982@gmail.com`. The scripts pass the account
  and the project on every call. They never change the gcloud config.
- The bucket `gs://kentrolabs-ai-cloudy-neigh-bench`, tenant `cloudy`. The
  store is `gs://$BUCKET/$TENANT`. Each experiment loads into its own
  namespace, for example `f32` or `fp16`. Set `NAMESPACE` before any phase.
- 14 GB of free RAM on the Mac for the query server.
- A clean work tree. The VMs build from `git archive HEAD`, so uncommitted
  changes do not reach them.

## Config

`scripts/config.sh` holds every setting. An environment variable overrides a
setting. The important ones:

| Variable | Default | Purpose |
| --- | --- | --- |
| `NAMESPACE` | none, required | The experiment, for example `f32` or `fp16`. Loads and queries use only this namespace. A name starts with a letter and holds letters, digits, `-` and `_`. |
| `RUN_NAME` | `<date>-e2e` | Run directory `bench/$RUN_NAME`. Export it to resume on another day. |
| `BUCKET` | `kentrolabs-ai-cloudy-neigh-bench` | Bucket for the GCS store. |
| `TENANT` | `cloudy` | Tenant in the tenants file and in every request. |
| `GCS_STORE` | `gs://$BUCKET/$TENANT` | The tenant root. A namespace that holds all segments skips the load. |
| `VARIANTS_<machine>` | see file | Distance variants per machine. |
| `SHAPES_<machine>` | see file | Shape and zone pairs, in order of preference. |
| `QUERIES`, `WARMUP` | 300, 20 | Queries per scenario. `QUERIES` stays at 1000 or less. |
| `LOADER` | `gnr` | The VM that loads the GCS store. |
| `KEEP_VMS` | 0 | Set 1 to stop `run-all.sh` from deleting the VMs on exit. |

The machines are `mac`, `emr` and `gnr`. To add a VM, add its name to `VMS` and
set `VM_`, `PLATFORM_`, `MODEL_`, `SHAPES_`, `VARIANTS_` and `PROFILE_` for it.

## Phases

Run the commands from the repository root. Name the experiment first:

```sh
S=.claude/skills/e2e-benchmark/scripts
export NAMESPACE=f32
```

| Phase | Command | Time |
| --- | --- | --- |
| Build | `bash $S/build.sh` | 1 to 5 min |
| Create a VM | `bash $S/vm.sh create emr` | 1 min, hours for GNR when capacity is short |
| Set up a VM | `bash $S/vm.sh setup emr` | 40 min, most of it the C++ gRPC build |
| Microbenchmarks | `bash $S/microbench.sh mac\|emr\|gnr` | 15 min |
| Load | `bash $S/load.sh mac\|gnr` | 10 min on the Mac |
| End to end | `bash $S/e2e.sh mac\|emr\|gnr [variant...]` | 20 to 45 min per variant |
| Report | `bash $S/report.sh` | 1 min, plus the ground truth on the first run |
| Cleanup | `bash $S/cleanup.sh` | 1 min |

`bash $S/run-all.sh` runs all phases with this parallelism:

```
build
├── per VM, in the background: create ─▶ setup ─▶ microbench
└── Mac: microbench ─▶ load ─▶ e2e
load on LOADER
e2e on every VM, in parallel
report ─▶ cleanup
```

A Mac-client run never shares a server with another client. The Mac e2e phase
ends before the VM e2e phases start, so the Mac server does not compete with
the Mac client.

A full run takes about four hours and about eight VM-hours. Check the price of
the shapes before a run.

The Python tools are Bazel targets in `scripts/e2e`: `groundtruth`,
`analyze` and `report`. The scripts never call `bazel run`. The `built` helper
in `lib.sh` runs `bazel build`, finds the executable with `bazel cquery
--output=files`, and the script runs that file. `report.sh` does this for you.

## Run directory

```
bench/<RUN_NAME>/
├── build.txt, src.tar.gz       # commit, branch, source for the VMs
├── linux/, mac/bin/            # cloudy, vector_test, query_test
├── bucket-location.txt
├── <machine>/                  # lscpu.txt, vm.txt, bench-<machine>-*.txt,
│                               # cpu-scan1m-*.pprof, load.txt
├── e2e/<machine>/<variant>/    # server.txt, server.log, cpu-server.pprof,
│   └── <client>/top<k>-<all|en>.jsonl
├── logs/                       # run-all.sh logs
├── summary.json
└── report.md
```

The client is `vm` for the VM client and `mac` for the Mac client. The
ground truth sits in `bench/groundtruth-1000x100.jsonl` and serves every run.

## Read the report

- Machines: check the CPU model, the zone and the bucket location first.
- Server: cold start took 62 to 112 s with the VM and the bucket in one
  region. It took 210 to 250 s across regions.
- Kernels, per-row split: the cached kernel against the streaming kernel gives
  the DRAM cost. `Scan1M` minus the streaming kernel gives the scan overhead.
  The server p50 minus `Scan1M` gives the cost that only the server pays.
- End-to-end latency: server time does not depend on `top_k`. A change in
  client-server time comes from the network or from the response size.
- Correctness: an exact variant has recall 1, except on float32 ties. A 16-bit
  variant shows its real recall loss here.

## Troubleshooting

- Race detection is on in `.bazelrc`. Every benchmark binary needs
  `-c opt --@rules_go//go/config:race=false`. `build.sh` does this.
- `build.sh` refuses a Mach-O binary for the VMs. `bazel-bin` can point at the
  Mac build, so the script reads the path from `bazel cquery --output=files`.
- `vm.sh create` goes through `SHAPES_<machine>` for `CREATE_ROUNDS` rounds. It
  deletes a VM with the wrong CPU model (207 for EMR, 173 for GNR). Granite
  Rapids exists only in `us-central1-a` and `us-central1-f`.
- A VM deletes itself after `MAX_RUN`, 12 hours by default.
- Start the query server only after the load flushed all segments. A server
  during a live load cloned the table on each sync and reached 46 GB. The
  float32 table needs 13.5 GB of RSS, a 16-bit table about 10 GB.
- A phase on a VM runs detached. Its log is `~/run/jobs/<job>.log` on the VM.
  The phase prints the tail of the log when the job fails.
- The query client is the executable that `bazel build //scripts:querybench`
  writes. No Bazel server holds its pipes.
  No Bazel server holds a pipe of the profile capture, and `curl` has a time
  limit.
- The Mac client needs port 50052 open. `firewall.sh` opens it to the public IP
  of the Mac only, and `e2e.sh` closes it on exit.

## Cleanup

`cleanup.sh` deletes the firewall rules and every VM with the label
`cloudy-e2e=$RUN_NAME`. `run-all.sh` calls it on exit unless `KEEP_VMS=1`. The
namespaces in the GCS store and in the Mac store stay, so the next experiment
can reuse a loaded namespace. Delete one by hand when it is final:

```sh
gcloud storage rm -r gs://$BUCKET/$TENANT/ns/$NAMESPACE --account=amiri1982@gmail.com --project=kentrolabs-ai
rm -rf ~/cloudy-bench-data/$TENANT/ns/$NAMESPACE
```

The tenant catalog `ns.json` keeps the entry of a deleted namespace.
