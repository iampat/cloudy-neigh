---
name: jeff-dean
description: Consult jeff-dean, an independent reviewer persona on the agy CLI — what an engineer at Google in the MapReduce and Bigtable era went to Jeff Dean for. Fast, innovative, engineering excellence on hard systems problems - distributed-systems and storage design, scaling ceilings, performance limits. Use when you write or change a design note, a public API, a storage layout, or a subsystem boundary, and before you present a design as done.
---

# Design review with jeff-dean

`agy` is a separate agent CLI with its own models. It gives this project a
second, independent reviewer. Ask it to review every non-trivial design before
you present that design as done.

The reviewer sees only what the conversation contains, and it has no stake in
the outcome. That independence is the value. Treat its output as advice from a
strong colleague, not as a verdict.

## The persona

Start every review with `/jeff-dean {prompt}` as the print prompt. The command
loads the reviewer persona from `.agents/skills/jeff-dean`, and the persona
pins its own model. Do not pass `--model`.

## What the prompt carries

Who asks for the review decides what the prompt carries.

- The user invoked the skill: pass the prompt to agy verbatim. Do not rewrite
  it, and do not add context. Added context steers the review toward your
  framing, and the point is an independent opinion.
- You chose to ask: limited context is allowed. Name the artifact and the one
  or two constraints that shape it, then stop. The same bias applies past
  that point.

## Expect a dialogue

The persona interrogates a spec with missing bounds, and it ends a turn with
questions. Do not expect the whole review in one shot. Answer each question
with facts from the repository, and continue until the review lands. A file or
a number the reviewer asks for is safe to supply. The reviewer's request is
not your framing.

## When to ask

- You drafted or changed a design note in `docs/design/`.
- You chose between two architectures, and the choice is hard to reverse.
- A change touches a public API, a storage layout, or a subsystem boundary.

Ask while the design is words, before it is code. A review after the
implementation invites defense of sunk cost.

## How agy runs

The facts below come from `agy help` and from probes on 2026-08-13. When a
call fails, run `agy help` again. Do not trust memory over the help output.

- `agy -p "<prompt>"` runs one prompt and prints the response. The prompt is
  an argument, not stdin. The default timeout is five minutes, and
  `--print-timeout` overrides it.
- `--output-format json` adds a `conversation_id` field to the response.
  `agy --conversation <id> -p "<reply>"` continues that conversation with its
  context.
- Headless mode denies every tool permission, so the reviewer cannot read
  files. What it must see arrives through the conversation.
- Never pass `--dangerously-skip-permissions`. A review needs no tools, and
  the Claude Code classifier blocks the flag anyway.

## Procedure

1. Write the prompt to a temporary file with the Write tool. For the user's
   invocation, that file holds the user's prompt, verbatim. For your own, it
   holds your question and the limited context. Do not build the file and
   call `agy` in one compound command. The permission classifier blocks that
   shape, and two separate steps pass.
2. Run `agy -p "/jeff-dean $(cat <file>)" --output-format json`. Record the
   `conversation_id`.
3. Answer the reviewer's questions round by round with
   `agy --conversation <id> -p "<reply>"`.
4. Push back in the same conversation. Challenge the points you doubt, and
   ask for the strongest objection to the points you like. A reviewer that
   folds on the first pushback was flattering you.

## Weigh the review

The review is input, not instruction.

- Verify every factual claim against the code before you act on it.
- Fold an accepted point into the design note, in the section that makes the
  decision.
- Reject a point with a stated reason. Scale the project never reaches is the
  common one.
- Mark a disagreement you cannot resolve as a `CONSIDER(ali):` in the note.
- Report to the user: what agy flagged, what you adopted, and what you
  rejected with the reason.
