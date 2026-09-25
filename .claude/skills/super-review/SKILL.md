---
name: super-review
description: Run three independent code reviews in parallel (agy, pi with openai/gpt-6-astra, and Claude), then merge the findings into one ranked table. Use when asked for a super review, a triple review, or a review by several models of a branch or a PR.
allowed-tools: Bash, Read, Write, Agent
---

# Super review

Three reviewers with different models read the same change. A finding that two
of them raise independently is likely real. A finding that one raises alone
needs a check in the code.

## Rules

- Send each reviewer the same message, verbatim: `/code-review <target> vs main`.
  `<target>` is the branch or the PR the user named. Add no context. Context
  steers the review, and the point is three independent opinions.
- Review against `origin/main`. Run `git fetch origin` first. A stale local
  `main` makes the diff repeat merged PRs.
- Never run a reviewer in the user's working tree. agy runs with
  `--dangerously-skip-permissions`, and pi has edit and bash tools. An
  untracked file was once deleted during a review. Give agy and pi a detached
  worktree, and give the Claude reviewer `isolation: "worktree"`.
- Do not address a finding. Report the table, and wait for the user.
- Drop a finding that contradicts a decision the user made. Name it and the
  decision in a separate table, so the user sees what was dropped.
- Keep the transcripts in `docs/reviews/<date>-<topic>-review-<tool>.md`. They
  stay local. Never commit them.

## Procedure

1. Prepare the worktree and the prompt file:

   ```sh
   git fetch origin
   git worktree add --detach /private/tmp/claude-501/wt-review <target branch>
   printf '%s' '/code-review <target> vs main' > /private/tmp/claude-501/review/prompt.txt
   ```

2. Start three background subagents in one message.

   - agy: a wrapper subagent runs the command below and returns the response
     verbatim. It writes the prompt to a file first and never pipes agy into
     another command (see `.claude/CLAUDE.md`). The output file can start with
     status lines, so it skips non-JSON lines before it parses.

     ```sh
     env -C /private/tmp/claude-501/wt-review agy -p "$(cat /private/tmp/claude-501/review/prompt.txt)" \
       --output-format json --dangerously-skip-permissions --print-timeout 20m \
       > /private/tmp/claude-501/review/agy.json 2>&1
     ```

   - pi: a wrapper subagent runs the command below and returns the response
     verbatim.

     ```sh
     env -C /private/tmp/claude-501/wt-review pi --model openai/gpt-6-astra --no-session \
       -p "$(cat /private/tmp/claude-501/review/prompt.txt)" > /private/tmp/claude-501/review/pi.md 2>&1
     ```

   - Claude: an Agent subagent with `isolation: "worktree"`. Its whole prompt is
     the message from the prompt file.

   A wrapper that sees an error (an unknown model, an auth failure, an empty
   response) stops and reports the command and the error verbatim. It does not
   retry and does not switch the model.

3. When all three are back, check that the user's working tree is unchanged:
   `git status --short` must match its state before the review.

4. Verify every high finding in the code before you report it. Read the lines,
   or run a short test in a throwaway worktree. Mark each verified row.

5. Merge the findings into one table per severity. Merge duplicates. One row per
   finding, with a column per reviewer and a verification mark:

   | # | Finding | Claude | pi | agy | Note |
   | --- | --- | :-: | :-: | :-: | --- |

   Put the note where two reviewers disagree, and give your own view. Add the
   table of dropped findings and the decision each one contradicts.

6. Remove the worktree: `git worktree remove /private/tmp/claude-501/wt-review`.
   Check that the Claude reviewer's worktree is gone too, with
   `git worktree list`.

## Report

Lead with the verified high findings. Then give the merged table, the dropped
findings, and the transcript paths. End with a recommended order of fixes, and
wait for the user's decision.
