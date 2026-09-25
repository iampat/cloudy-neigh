---
name: super-review
description: Run three independent code reviews in parallel (agy, pi with openai/gpt-6-astra, and Claude), then merge the findings into one ranked table. Use when asked for a super review, a triple review, or a review by several models of a branch or a PR.
allowed-tools: Bash, Read, Write, Agent
---

# Super review

Three reviewers with different models read the same change. A finding that two
of them raise independently is likely real. A finding that one raises alone
needs a check in the code.

## Arguments

```text
/super-review <target> [--context "<text>"] [--prompt "<text>"] [--dry-run]
```

- `<target>`: the branch or the PR to review.
- `--context "<text>"`: extra context from the user. Append it to the message
  after one blank line, verbatim.
- `--prompt "<text>"`: a review prompt from the user. It replaces
  `/code-review <target> vs main`, verbatim. `--context` still appends to it.
- `--dry-run`: print the exact message and the exact three commands, then
  stop. Create no worktree, start no subagent, and write no file.

## The message

All three reviewers get the same message:

```text
<--prompt text, or: /code-review <target> vs main>

<--context text, when given>
```

Add nothing that the user did not give. Your own context steers the review,
and the point is three independent opinions.

## Rules

- Review against `origin/main`. Run `git fetch origin` first. A stale local
  `main` makes the diff repeat merged PRs.
- Never run a reviewer in the user's working tree. agy runs with
  `--dangerously-skip-permissions`, and pi has edit and bash tools. An
  untracked file was once deleted during a review. Give agy and pi a detached
  worktree, and give the Claude reviewer `isolation: "worktree"`.
- Do not address a finding. Report the table, and wait for the user.
- Drop a finding that contradicts a decision the user made. Name it and the
  decision in a separate table, so the user sees what was dropped.
- Keep the transcripts in `docs/reviews/<date>-<topic>-<run id>-<tool>.md`. The
  run id is the random suffix of `$RUN`. They stay local. Never commit them.

## Procedure

1. Create a run directory with a random name, so parallel or leftover runs
   never collide. Shell state does not persist between tool calls, so record
   the printed path and write it literally into every later command. The
   steps below call it `$RUN`.

   ```sh
   git fetch origin
   mktemp -d /private/tmp/claude-501/super-review.XXXXXX
   git worktree add --detach $RUN/wt <target branch>
   ```

   Write the message from "The message" to `$RUN/prompt.txt` with the Write
   tool.

   With `--dry-run`, skip this step. Print the message, then the three
   commands of step 2 with the target filled in and `$RUN` left as is, and
   stop.

2. Start three background subagents in one message.

   - agy: a wrapper subagent runs the command below and returns the response
     verbatim. It writes the prompt to a file first and never pipes agy into
     another command (see `.claude/CLAUDE.md`). The output file can start with
     status lines, so it skips non-JSON lines before it parses.

     ```sh
     env -C $RUN/wt agy -p "$(cat $RUN/prompt.txt)" \
       --output-format json --dangerously-skip-permissions --print-timeout 20m \
       > $RUN/agy.json 2>&1
     ```

   - pi: a wrapper subagent runs the command below and returns the response
     verbatim.

     ```sh
     env -C $RUN/wt pi --model openai/gpt-6-astra --no-session \
       -p "$(cat $RUN/prompt.txt)" > $RUN/pi.md 2>&1
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

6. Remove the worktree with `git worktree remove $RUN/wt`, then the run
   directory with `rm -r $RUN`.
   Check that the Claude reviewer's worktree is gone too, with
   `git worktree list`.

## Report

Lead with the verified high findings. Then give the merged table, the dropped
findings, and the transcript paths. End with a recommended order of fixes, and
wait for the user's decision.
