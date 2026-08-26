# Lessons

Corrections Ali repeated. Read this before the first action in a task.

The `retro` skill maintains this file. Ali triggers that skill. Do not edit this
file by hand, and do not add an entry from one case.

Last pass: 2026-08-26. Window: 4 weeks, 286 messages, 21 sessions.

## Workflow

### Update the PR in the same turn as the change
**Do:** After you change a file on a PR branch, commit, push and update the PR body. Do not wait to be told.
21 asks, 2026-08-12 to 2026-08-26. Three were chases after I said nothing.
> "have yu update the the PR"

### Rebase and prune after a merge
**Do:** When Ali says a PR merged, go to main, rebase on origin, then delete the merged branch local and remote. One turn, no questions.
26 asks, 2026-08-11 to 2026-08-26.
> "#51 is merged, rebase to main with origin"

### Write the correction into the rules, not only into the file
**Do:** When Ali corrects a class of mistake, fix the instance and update the guideline or the skill in the same turn.
14 asks, 2026-08-11 to 2026-08-26.
> "updat the guidelines for coding and code review to prevent such verbos comments. and then clean all the comments there"

### Split independent work across subagents
**Do:** When a task holds two or more parts that do not depend on each other, offer subagents before Ali asks.
5 asks, 2026-08-12 to 2026-08-22.
> "in a subagent move .agents and .claude changes into a new PR | in a different subagent read ..."

## Scope

### Do what was asked, then stop
**Do:** Read the named file and nothing more. Do not start the next step, the review or the redesign until Ali says so.
7 stops, 2026-08-13 to 2026-08-21.
> "I did not ask you to start the review. have out applied my comments?"

### Give a recommendation
**Do:** End an analysis with the option you pick and the reason. A survey of choices is not an answer.
6 asks, 2026-08-12 to 2026-08-26.
> "I like to hear your opinion"

### Show the text before you apply it
**Do:** For a doc edit, a design change or a prompt you will send, print the proposed text in the reply first. Apply it after Ali agrees.
5 asks, 2026-08-12 to 2026-08-22.
> "what you want to add for 5 and 7?? write them here before adding them to the doc"

### Delete means delete
**Do:** Delete the named file. Do not restore it, do not ask again, and do not treat its absence as an accident.
4 asks on 2026-08-25, three of them the same words.
> "how-it-was-implemented.md --> delete it"

## Code

### One implementation with a driver, never one per backend
**Do:** Before you write a second backend, find the library's unified interface. Push the driver-specific code into the smallest hook the library offers.
8 rounds, 2026-08-13 to 2026-08-25, and one full rejection of the objectstore package.
> "I asked you many times! https://gocloud.dev/howto/blob/ ... you don't neeed opener for each driver"

### Cut the redundant code
**Do:** Inline a helper with one caller. Collapse two switches on the same value into one. Fold two files that differ in one field. Delete a test that asserts nothing.
7 cases on 2026-08-25. Repeat offence: `docs/guidelines/go.md`, "Less code".
> "look carefully to these lines two switch! ... it can be cleaned up be be simpliied, we don't need two swithc statments"

### Delete the comment that carries no information
**Do:** Write no comment except a workaround with its upstream issue, or an invariant a reader would break. Delete the rest before you show the code.
6 asks, 2026-08-13 to 2026-08-25. Repeat offence: `.claude/CLAUDE.md`, `docs/guidelines/go.md`.
> "there are still comments in objectstore/local.go clean it up"

### Do not solve a problem we do not have
**Do:** Leave out a limit, a constraint or a mitigation until the problem appears. Delete a `CONSIDER` that guards a future case.
5 asks, 2026-08-12 to 2026-08-26.
> "both of them are premature optimization, we are solving a problem that does not exist"

### No hidden magic
**Do:** Never add a silent default, a silent fallback or a quiet directory creation. A missing input is the caller's error.
3 asks, 2026-08-11 to 2026-08-25. Repeat offence: `docs/guidelines/go.md`, "Explicit beats clever".
> "if dir does not exist it's the user's responcibility to create or set auto create on. do not hiden magic"

## Writing

### Write to me the way you write a design note
**Do:** Apply the STE rules to the terminal reply, the commit message and the PR body, not only to `docs/`.
5 asks, 2026-08-12 to 2026-08-25. Repeat offence: `.claude/CLAUDE.md`, `~/.claude/CLAUDE.md`.
> "but I saw you used complex sentences, complex grammer or complex words. that should be fixed."

### Do not explain what the reader knows
**Do:** Cut the sentence that explains a language feature, a protocol or a pattern. Cut the reason for an obvious choice.
7 asks, 2026-08-12 to 2026-08-13. Repeat offence: `.claude/CLAUDE.md`.
> "you don't need to explain why a message is empty, reader are experienced. remove it here and in the rest of the doc."

### Verify the claim before you write it
**Do:** Read the help output, the source or the API before you state how a tool behaves. Cite the reference. "I do not know" beats a confident error.
4 asks, 2026-08-13 to 2026-08-25.
> "before start writing the skill call `agy help` to learn how it work. do not trust your knowledge about it use the help"

### Delete the review transcript before the merge
**Do:** Write the agy transcript to `docs/reviews/`, let Ali read it in the PR diff, then `git rm` it before the merge.
2 asks, 2026-08-21 to 2026-08-26.
> "remove files in ./docs/review/..."
