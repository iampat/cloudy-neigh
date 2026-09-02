# Deterministic Simulation Testing

## Problem

Distributed storage bugs occur in rare permutations of message reordering, clock skew, and crash-recovery cycles.
Running tests against real cloud object stores is slow and non-deterministic.
A 3-node fault injection cycle against real cloud APIs takes tens of seconds.
This approach explores only hundreds of permutations per pull request.

## Goals

- Achieve 100% reproducible test executions via seeded pseudo-random generation.
- Run thousands of simulated multi-writer race conditions per second in-memory.
- Continuously assert storage safety and contiguity invariants under simulated faults.
- Verify crash recovery and replay deduplication without external infrastructure.

## Non-goals

- End-to-end formal verification of the full Go runtime.
- Stress testing real cloud network bandwidth or hardware disk throughput.

## Model

The testing framework replaces real time and cloud transport with a deterministic simulation harness.

```
Deterministic Simulation Stack
┌────────────────────────────────────────────────────────┐
│ Randomized Workload Generator (Seeded PRNG)            │
└──────────────────────────┬─────────────────────────────┘
                           │
             ┌─────────────┴─────────────┐
             ▼                           ▼
┌────────────────────────┐  ┌────────────────────────────┐
│ cloudy-neigh Engine    │  │ In-Memory Shadow Oracle    │
│ (kvfs + logstream)     │  │ (Single-threaded std map)  │
└────────────┬───────────┘  └────────────┬───────────────┘
             │                           │
             ▼                           │
┌────────────────────────┐               │
│ FaultStore Proxy       │               │
│ (412s, Drops, Corrupt) │               │
└────────────┬───────────┘               │
             │                           │
             ▼                           ▼
┌────────────────────────┐  ┌────────────────────────────┐
│ memDriver (RAM)        │  │ Continuous Assertions      │
│                        │◄─┤ (State == Oracle, 1..N WAL)│
└────────────────────────┘  └────────────────────────────┘
```

## Architecture

### 1. Deterministic FaultStore Proxy

The simulation introduces a decorator over the `objectstore.Store` interface.
It wraps `memDriver` and accepts a seeded random generator.

The proxy injects the following faults deterministically:
- **Precondition Collisions**: Fails conditional `Put` calls with `ErrPreconditionFailed`.
- **Phantom Writes**: Commits object data to memory, but returns network timeouts to the caller.
- **Data Corruption**: Injects bit flips into payload readers to test checksum validation.
- **Latency Jitter**: Delays simulated responses based on virtual clock steps.

### 2. Virtual Clock and Randomness

Production code must not call uncontrolled sources of non-determinism directly.
- Replace `time.Now` and `time.NewTicker` with an injectable `Clock` interface.
- Replace global `rand` calls with an explicit `*rand.Rand` source.
- Allow simulation loops to advance time without sleeping OS threads.

### 3. Invariant Oracles

Fault injection alone does not prove correctness.
The test harness runs a single-threaded reference map in lockstep with the system.

The harness continuously checks these invariants:
- **WAL Contiguity**: LogStream sequence numbers form a contiguous `1..N` sequence with no gaps.
- **Linearizability**: Reads observe committed writes in sequential order.
- **Branch Isolation**: Mutations on a branch never alter or corrupt parent branch manifests.
- **Idempotent Recovery**: Replaying WAL records reproduces identical state.

## Implementation Plan

1. Define `Clock` interface for simulation testing.
2. Implement `FaultStore` decorator in `objectstore`.
3. Add randomized multi-writer differential tests for `logstream` and `kvfs`.
