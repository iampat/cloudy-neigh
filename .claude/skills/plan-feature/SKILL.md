---
name: plan-feature
description: Plan a new feature, subsystem, or design for cloudy-neigh. Use when starting non-trivial work — before writing code — to surface unknowns, failure modes, and trade-offs while they are still cheap to fix.
---

# Planning a feature

The cheapest place to find an unknown is before any code is written. Work through
this before implementing; share the result concisely with the user (or as a design
note in `docs/design/` if the scope warrants one).

## 1. Surface unknowns first

- Do a blindspot pass: what parts of this task touch areas neither you nor the
  request has pinned down? Name them explicitly rather than silently picking.
- Translate vague terms in the request into precise ones ("fast" → target
  latency/throughput; "durable" → what survives what kind of crash).
- Schema decisions, serialization formats, and type interfaces are the most likely
  tweaking points — flag them and keep them cheap to change.
- When two designs are genuinely competitive, sketch both briefly and compare
  instead of committing to the first.

## 2. Plan

1. Break the work into small, independently verifiable tasks.
2. Outline the code: package layout, key types, function names and purposes.
3. List the specific test cases you will write — happy path **and** failure modes
   (crash mid-write, partial/torn writes, concurrent writers, retries, context
   cancellation).
4. State the error-handling strategy per failure scenario.
5. State trade-offs: performance, scalability, complexity, security.

## 3. Interrogate failure modes

For a storage/search engine this is the part most likely to be wrong — answer
explicitly:

- What happens on a crash at the worst possible moment? What state is recovered,
  and by whom?
- What happens under 10x load? What backs up, and where is backpressure applied?
- What happens with two concurrent writers? What is the linearization point?

## 4. While implementing

- Keep a running note of where reality diverged from the plan; fold it back into
  the design note so the next task starts smarter.
- When an unknown forces a conservative choice mid-implementation, record *why*
  (a `CONSIDER(ali):` comment or a line in the design note).
