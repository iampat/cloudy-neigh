---
name: ideate
description: Search algorithm patterns or record session verdicts in docs/random-thoughts.md via subagents.
---

# ideate

Catalog and retrieve architectural ideas, external algorithms, and brainstorm verdicts in `docs/random-thoughts.md`.

## Rule: Subagent Only for Retrieval

`docs/random-thoughts.md` contains large volumes of ideas. Reading it directly in the main conversation pollutes context.

- The main agent must never call `view_file` or `grep_search` on `docs/random-thoughts.md`.
- Always launch a `research` subagent to search `docs/random-thoughts.md`.
- The subagent reads the file, identifies matching items, and returns only the concise summary and relevant section.

## Rule: User Approval Required for Edits

Never edit `docs/random-thoughts.md` without explicit user approval.

1. Draft the new entry with Date, Topic, Options Considered, Verdict, References, and Status.
2. Draft the 1-to-2 sentence summary for the top Index.
3. Print the proposed text in the terminal reply for user review.
4. Apply the edit to `docs/random-thoughts.md` only after the user approves.
