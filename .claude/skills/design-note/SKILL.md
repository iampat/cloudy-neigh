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

## Writing rules

These rules come from ASD-STE100 Simplified Technical English and the Proxmox
technical writing style guide.

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

## Justify against prior art

This project takes ideas from other systems. A design note must say what the other
system does and why we agree or differ. A decision without a reason is not a
decision, and the next reader will reverse it by accident.

Give every decision three parts:

1. What we do.
2. What the reference system does.
3. Why we differ, or why we agree.

Name the cost of a divergence. A choice with no cost is usually a choice that is
not yet understood.

## Structure

Use this order. Skip a section that has no content.

| Section  | Content                                            |
| -------- | -------------------------------------------------- |
| Status   | Draft, accepted, or superseded. The date.          |
| Scope    | What the note covers. What it does not cover.      |
| Model    | The core objects and how they relate.              |
| Decisions| One entry per decision, with the justification.    |
| Open     | Unresolved questions, marked `CONSIDER(ali):`.     |

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

- The title says what the note decides.
- Every decision names the alternative and the reason.
- Every divergence from prior art names its cost.
- Every diagram fits in 80 columns.
- Every open question carries a `CONSIDER(ali):` marker.
- No sentence is longer than 25 words.
