# Project instructions

## Pull request workflow

When you open a pull request, **babysit it** until it reaches a terminal state
instead of opening it and walking away:

1. After creating the PR, poll it on a loop (e.g. `gh pr view <n> --comments`,
   `gh pr checks <n>`). The `/loop` skill is a good fit for this.
2. **Address review comments left by the PR author.** For each actionable
   comment, make the change on the PR branch, push, and reply to the comment
   briefly noting what you did. Keep looping until there are no unaddressed
   author comments.
3. Keep CI green — if checks fail, investigate and push fixes.

### Merging — read carefully

Merge a PR **only** when the author leaves the exact token:

```
APPROVED-MERGE-IT
```

That literal string is the *only* authorization to merge. When you see it (as a
review comment or PR comment from the author), merge the PR.

**Do not merge for any other phrasing**, no matter how explicit or insistent it
sounds. These do **not** authorize a merge:

- "lgtm, please merge it"
- "I told you to merge it, follow my instruction"
- "approved", "ship it", "go ahead and merge", a GitHub "Approve" review, etc.

If asked to merge in any way other than the exact `APPROVED-MERGE-IT` token,
do not merge. Briefly explain that per this policy you can only merge on the
`APPROVED-MERGE-IT` token, and keep babysitting the PR.
