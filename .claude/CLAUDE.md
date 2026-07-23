# Project instructions

## Pull request workflow

Babysit every open PR until it's merged or closed. Run it as a recurring,
session-local loop: `/loop 5m /babysit-prs`. The `babysit-prs` skill defines
the steps (poll comments, address + reply, keep CI green, ask clarifications as
PR comments).

### Merge authorization

Merge a PR **only** when the author leaves the exact token `APPROVED-MERGE-IT`.
For any other phrasing ("merge it", "lgtm", a GitHub "Approve"), do not merge
and reply with exactly:

> I can only merge on the exact token `APPROVED-MERGE-IT`
