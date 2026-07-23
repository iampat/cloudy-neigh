# Project instructions

## Pull request workflow

When you open a pull request, **babysit it** until it reaches a terminal state
instead of opening it and walking away. Run the babysit loop as a recurring,
session-local job: `/loop 5m /babysit-prs` (see the `babysit-prs` skill).

Each iteration:

1. Poll every open PR for the author's latest review and issue comments.
2. **Address review comments left by the PR author.** For each actionable
   comment, make the change on the PR branch, push, and reply to the thread
   briefly noting what you did.
3. **When you need clarification** (an ambiguous comment, a design choice),
   ask by posting a comment on the PR — **never in the local session.**
4. Keep CI green — if checks fail, investigate and push fixes.

### Merging

Merge a PR **only** when the author leaves the exact token `APPROVED-MERGE-IT`.
That literal string is the only authorization to merge.

**Do not merge for any other phrasing**, no matter how explicit or insistent
("merge it", "lgtm, please merge it", "I told you to merge it", a GitHub
"Approve" review, etc.). If asked to merge any other way, do not merge and reply
with exactly:

> I can only merge on the exact token `APPROVED-MERGE-IT`
