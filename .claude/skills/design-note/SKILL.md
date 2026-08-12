---
name: design-note
description: Write or update a document under docs/. Use for design notes, architecture notes, and READMEs to keep the style and structure consistent.
---

# Design notes

A design note records a decision and the reason for it. The reader arrives months
later with one question. Answer that question near the top.

Write Markdown in `docs/design/`. Markdown renders on GitHub, in an editor, and in
a terminal. It also diffs one line at a time, so a reviewer can comment on a
sentence. Do not write HTML. Generate HTML from the Markdown if a published site
is needed later.

## Assume a working software engineer

The reader knows the language, the protocol, and the common patterns. Never
explain a `oneof`, a status code, a map, or a retry. Explaining a known thing
wastes the reader's attention and buries the part that is specific to us.

Write about the choice, not the mechanism. "Patch and delete join the same
`oneof`" is enough. Why a `oneof` holds one field at a time is not.

## Two kinds of prose

A design note contains normative prose and rationale prose. They follow different
rules.

Normative prose states what the system does. It covers the model, the message
shapes, the invariants, and the limits. Apply the writing rules below without
exception. Ambiguity here becomes a defect later.

Rationale prose explains why. It weighs one option against another and names a
cost. Keep it plain, but let a sentence carry a subordinate clause when the
argument needs one. A rule that forbids "because" and "which means" removes the
connective tissue of an argument, and every claim then lands with equal weight.

Rationale prose is still short. Three sentences per decision is usually enough.

## Writing rules

These rules come from ASD-STE100 Simplified Technical English and the Proxmox
technical writing style guide. Apply them strictly to normative prose and loosely
to rationale prose.

- Put the important information first in the sentence.
- Keep a sentence to 15–20 words. Split a longer sentence.
- Write one idea per sentence and one topic per paragraph.
- Use active voice. Write "the server rejects the write", not "the write is
  rejected".
- Use present tense. Describe what the system does, not what it will do.
- Use third person for a description. Use the imperative for an instruction.
- Do not write "I". Write "we" only to give a recommendation.
- Use one term for one concept. Never change a term for variety.
- Use a list when a sentence names three or more items.
- Write "for example", not "e.g.". Write "and so on", not "etc.".
- Spell out an acronym at first use, then use the acronym.
- Do not use jargon, idiom, or an informal expression.
- State a trade-off in one sentence. Do not argue both sides at length.
- Delete a sentence that repeats the sentence before it.

## Prior art is evidence, not a blueprint

This project studies other systems and takes what is useful. Present our design on
its own terms. Do not present it as a set of differences from another system.

A note that frames every decision against one reference system makes that system
the spine of our architecture. It also implies that every choice the other system
made was a live option for us, which is often false. A transport that dictates an
answer is not a decision at all.

Keep prior art in one section. Cite another system where it carries information we
do not have:

- Operational experience. A published limit or a documented failure is evidence.
- A trap they hit first. A versioned name usually marks a migration they could not
  perform.
- A genuine alternative we weighed and did not take.

Do not cite another system to justify a choice the medium already forces.

## Record the alternatives

A decision needs its rejected alternatives. Give each one a line: what it is, and
why we did not take it. Without that section the next reader re-proposes the same
option, and nobody remembers the answer.

Name the cost of every choice. A choice with no cost is usually a choice that is
not yet understood.

## Structure

Use this order. Skip a section that has no content.

| Section      | Content                                              |
| ------------ | ---------------------------------------------------- |
| Status       | Draft, accepted, or superseded. The date.            |
| Problem      | What the work must achieve. The constraints.         |
| Goals        | Goals and non-goals, as two lists.                   |
| Model        | The core objects and how they relate.                |
| Design       | One section per topic. Name the topic, not the answer.|
| Alternatives | Rejected options, one line each.                     |
| Prior art    | What other systems teach. One section.               |
| Open         | Unresolved questions, marked `CONSIDER(ali):`.       |

A heading names a topic. Write "Identifiers", not "One identifier type, ordered by
encoding". State the decision in the first sentence of the section. A reader scans
headings to find a topic, not to collect conclusions.

Put the open questions at the end, never in a footnote. An unresolved question is
the most valuable part of a draft.

## Diagrams

Draw a diagram when the shape of the data is the point. Use box-drawing characters
in a fenced code block. A fenced block renders as monospace everywhere.

Cap every diagram at 80 columns. A wider diagram scrolls sideways on GitHub and
wraps in a terminal.

```
  text attribute              vector attribute
  ┌──────────────┐            ┌──────────────┐
  │  "the cat"   │            │  [0.1, 0.9]  │
  └──┬────────┬──┘            └──┬────────┬──┘
     │        │                  │        │
     ▼        ▼                  ▼        ▼
  postings  stored           ANN index  stored
  (lossy)   (exact)          (lossy)    (exact)
```

Use these characters: `┌ ┐ └ ┘ ├ ┤ ┬ ┴ ┼ ─ │ ▶ ▼ ◀ ▲`.

Show a measurement as a bar chart. Put the number at the end of the bar.

```
  v1 ║░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  51.6 GiB
  v2 ║░░░                                5.2 GiB
```

## Code in a design note

Show a type or a message definition when the reader needs the exact shape. Keep
the snippet to the fields under discussion. Cut the imports and the boilerplate.

A design note is not a source file. Do not paste a whole file into it.

## Checklist

- The note opens with the problem, not with the answer.
- Every heading names a topic. No heading states a conclusion.
- The design reads on its own terms. Another system is not the spine.
- Prior art appears in one section, and each citation carries information.
- Every decision names its cost.
- Rejected alternatives have a home.
- Every diagram fits in 80 columns.
- Every open question carries a `CONSIDER(ali):` marker.
- No normative sentence is longer than 25 words.
