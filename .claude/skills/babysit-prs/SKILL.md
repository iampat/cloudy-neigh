---
name: babysit-prs
description: Babysit all open PRs in this repo — poll for the author's review comments, address and reply to them, keep CI green, and merge only on the exact APPROVED-MERGE-IT token. Ask any clarifications as PR comments, not in the session. Use on a recurring loop (e.g. /loop 5m /babysit-prs).
---

# Babysit open PRs

Run these steps each iteration for the current repo:

1. List open PRs: `gh pr list --state open`.
2. For each open PR, fetch the author's latest review and issue comments:
   `gh api repos/<owner>/<repo>/pulls/<n>/comments` and `gh pr view <n> --json comments`.
3. For each unaddressed author comment: make the requested change on that PR's
   branch, push, and reply to the thread noting what you did.
4. If a comment is ambiguous, ask for clarification **as a PR comment** — never
   in the local session.
5. Check CI with `gh pr checks <n>` and fix failures to keep it green.
6. Merge a PR **only** if the author left the exact token `APPROVED-MERGE-IT`.
   For any other phrasing, do not merge and reply with exactly:
   `I can only merge on the exact token \`APPROVED-MERGE-IT\``
7. Give a brief status summary.
