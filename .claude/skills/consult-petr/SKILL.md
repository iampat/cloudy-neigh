---
name: consult-petr
description: Consult petr, an algorithm persona on the agy CLI, a competitive programming champion and algorithm designer. Use for a hard algorithmic or data-structure problem. The naive solution is too slow, the complexity bound decides the design, or a hot path needs the right structure. Petr designs and writes the code. You supervise.
---

# Algorithm work with petr

`agy` is a separate agent CLI with its own models. `petr` is its algorithm
persona. Petr designs the algorithm, proves it, and writes the code.

**You do not implement inside this skill.** Not the algorithm, not the tests,
not the fix for a broken build. Your job is different, and it is listed below.
An edit by you breaks the loop, because Petr then owns code he did not write.

## Your job

- Frame the problem and carry the bounds.
- Relay the user's questions and answers, verbatim in both directions.
- Verify the claimed complexity against the code Petr wrote.
- Send every finding back to Petr in the same conversation.
- Keep the transcript, and report to the user.

Petr runs the build himself: `bazel run //:gazelle`, `bazel build //...`,
`bazel test //...` and `bazel run //:format.check`. He owns the code, so he
owns the gate that proves it works. Tell him to run it, and read what he
reports.

Run the gate yourself only to check a claim he made. A green report you never
tested is a claim, and this skill exists because a claim needs a check.

## Two modes

Every prompt to Petr starts with a `MODE:` line.

- `MODE: design`: Petr analyses, proves and plans. He writes no file. Use this
  for a discussion, a data-structure choice, or a complexity question.
- `MODE: implement`: Petr writes the code and the tests.

Start in design mode. Move to implement only when the user says so. Never send
`MODE: implement` on your own initiative.

## What the prompt carries

Unlike a review, this work needs context. A missing bound costs a round trip.

Always carry the bounds you know: n, the value range, the query rate, the
memory ceiling and the latency target. Add the failure the code must survive.
Name the files Petr must read.

State the constraint, not the solution you prefer. Your framing steers the
approach, and the approach is what you came for.

When the user invoked the skill, pass the user's problem statement verbatim.
Add the bounds under it, marked as your own addition.

## Expect a dialogue

Petr interrogates a spec with missing bounds, and he ends a turn with
questions. Do not expect the whole design in one shot.

- The user invoked the skill: relay the questions to the user, and wait. Do not
  answer in the user's place. Send the answers verbatim.
- You fired the skill: answer from facts in the repository. Answer only what is
  certain. Say so when a number is an estimate. "I do not know" beats a
  confident error.

## How agy runs

`.claude/CLAUDE.md` holds the agy facts. Read them before the first call. An
implementation turn writes code, so it needs `--print-timeout 20m`.

## Procedure

1. Write the prompt to a temporary file with the Write tool. Start the file with
   the `MODE:` line.
2. Run `agy -p "/petr $(cat <file>)" --output-format json
   --dangerously-skip-permissions --print-timeout 20m`. Record the
   `conversation_id`.
3. Answer round by round with `agy --conversation <id> -p "<reply>"
   --dangerously-skip-permissions`.
4. After every turn, append the turn to the transcript file. See below.
5. Push back. Challenge a bound you doubt. Ask for the input that breaks the
   approach he likes. An algorithm that folds on the first pushback was a guess.
6. An implementation turn starts with a language choice and a scaffold: the
   package, the API, the types, the signatures. Read the scaffold when it
   appears. An objection to the API costs one round now, and a rewrite later.
7. Tell Petr to run the gate and to report the output: `bazel run //:gazelle`,
   `bazel build //...`, `bazel test //...`, `bazel run //:format.check`. He
   fixes what fails, and he runs it again until it passes.
8. Review the diff against `docs/guidelines/` and the comment rules in
   `.claude/CLAUDE.md`. Send the findings back to Petr. He fixes them.
9. Run the gate once yourself, at the end. It confirms his report, and it
   catches the failure that only appears in a clean tree.

## Verify the claim

A complexity claim is a claim about the code, so check the code.

- Re-derive the bound from the loops and the recursion. A claimed O(n log n)
  with a linear scan inside the loop is O(n² log n).
- Check the stress test compares against the baseline, not against itself.
- Check the adversarial inputs from the design became test cases.
- Check the benchmark exists when speed was the reason for the approach.

Petr picks the kinds of test the problem needs, and he names the kind he
skipped. Weigh his reason. Send it back when the reason does not hold. Do not
ask for a test that cannot fail.

A complexity claim with nothing that shows it is not done. Send that back.

## Record the transcript

Keep a verbatim transcript of the whole conversation. The user reads it to see
how the design went.

- One markdown file per conversation: `docs/reviews/<date>-<topic>.md`. It
  belongs to the pull request, and `.claude/CLAUDE.md` states its lifetime.
- Start the file with the `conversation_id`.
- After every turn, append the prompt you sent and the response you received.
  Copy both verbatim. Do not summarize, do not trim.
- Mark each half with a heading: `## prompt` and `## response`.

## When to ask

- The naive solution is too slow, and the bound decides the design.
- A data structure choice shapes a subsystem: index layout, posting list merge,
  ranking, compression, cache eviction.
- A hot path where the constant factor decides the latency target.
- A correctness argument that needs a proof, not a test.

Do not ask for plumbing, glue code, configuration, or a problem the standard
library already answers.

## Report to the user

Give the chosen approach, its complexity, the alternatives Petr rejected and
his reason, and the state of the build gate. Name what you sent back and why.
Mark an unresolved disagreement as a `CONSIDER(ali):` in the code or the design
note.
