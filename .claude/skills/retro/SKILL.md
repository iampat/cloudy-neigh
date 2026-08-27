---
name: retro
description: Review the recent session history for corrections the user repeated, then fold each one into the rule file that owns it. The user triggers this skill and approves every edit. Never start it on your own.
allowed-tools: Read, Write, Edit, Bash, Grep, Glob
---

# Retro

Find what the user had to say more than once. Put the rule where it belongs, so
they do not say it again.

The user triggers this skill. Never fire it on your own, and never fold it into
another task.

The pass changes the rule files, so it needs approval. Steps 1 to 5 read and
plan. Step 6 asks. Step 7 edits.

## 1. Read the history

```sh
.claude/skills/retro/history.sh 4 > <scratchpad>/turns.txt
```

The argument is the window in weeks. Four is the default. The script prints one
line per message the user typed: the date, the session, the text. It drops tool
output, system reminders and injected skill prompts. It reports a dropped long
message on stderr, so you can check what it removed.

The script gets the text. It does not find the meaning.

## 2. Read every message

A model reads the extract. A pattern does not.

A person types fast. The text carries a misspelling, a doubled word and a
dropped letter. A grep for the correct spelling misses those lines, and a
missed line hides a repeat.

Rules for this step:

- Read every line of the extract. Do not sample.
- Never use grep, `test()` or a word list to find a candidate. A search finds
  what you already suspect, and the point of the pass is what you do not.
- Match on intent, not on words. "update the PR", "have yu update the the PR"
  and "did you push it" are one finding.
- Read the reply the message answers when the message alone is unclear. A bare
  "no, again" carries its meaning in the turn before it.

When the extract is too large for one read, slice it by date into parts of
about 200 messages. Give one subagent each part and this instruction:

> Read every message. Return each case where the user gives an instruction
> about how the assistant must work, or corrects what the assistant did. For
> each case return: the instruction as an imperative, the date, the session,
> and the message quoted verbatim. Group nothing. Judge nothing. Typos are
> normal, so read for meaning.

Then merge the parts yourself. A subagent sees one slice. Only you see that a
case in week one and a case in week four are the same finding.

## 3. Group into findings

Group the cases by the action they ask for. Two cases join when the action is
the same, whatever the words.

Evidence makes a finding. Two shapes count:

- The user gave the same instruction in two or more sessions.
- The user corrected the same behaviour twice, in any words.

One case is not a finding. Note it and move on.

For each finding, record the action, the number of cases, the first date, the
last date, and the one quote that shows it best.

Count by the cases you collected in step 2. Do not count with grep. A typo
breaks the count the same way it breaks the search.

## 4. Give each finding a home

A rule lives in one file. The file decides when the rule reaches you.

Read the rules that exist first: `.claude/CLAUDE.md`, `docs/guidelines/*.md`,
`.claude/skills/*/SKILL.md` and the user file `~/.claude/CLAUDE.md`.

| The finding is about | Home |
| --- | --- |
| A rule for one language | `docs/guidelines/<language>.md` |
| How one named skill runs | that skill's `SKILL.md` |
| The repo workflow, or a rule two skills need | `.claude/CLAUDE.md` |
| How the user and the assistant work, with no home above | `.claude/lessons.md` |

`lessons.md` is the last choice, not the first. It is the holding area for what
no rule file covers. A finding with a home goes home.

Then check what the finding does to the rule that exists:

| Case | Action |
| --- | --- |
| No rule covers it | Write the rule in its home |
| A rule covers it, and the rule is vague | Sharpen that rule where it lives |
| A rule covers it, and it is right | Add a `lessons.md` entry, and change nothing else |
| A rule covers it, and every case falls outside the window | Drop the finding |

The third row is the repeat offence. The rule is not wrong. The rule does not
reach the moment of the mistake, and a short entry read at task start does.

## 5. Keep the rules DRY

The pass adds text to files that load at the start of every task. Left alone,
it grows into two rules that disagree.

- **One rule, one file.** Never state a rule twice. When you write it in its
  home, delete every copy elsewhere, `lessons.md` included.
- **Factor up.** A rule that two or more skills need moves to `.claude/CLAUDE.md`.
  Each skill drops its copy and depends on the one in CLAUDE.md.
- **Supersede.** Before you add a rule, find the rule it replaces. Delete that
  one in the same edit. Do not leave the weaker version behind.
- **Merge.** Two entries that ask for the same action become one entry.
- **Retire.** Delete a `lessons.md` entry with no evidence in the last two
  windows.
- **Cap.** Twenty entries in `lessons.md`. Over the cap, delete the entry with
  the oldest last date.

The skill may edit itself. A finding about how this pass runs belongs in this
file.

### `lessons.md` entry format

```markdown
### <the rule, as an imperative>
**Do:** <the action, in one sentence>
<N asks, <first date> to <last date>><, repeat offence: <where the rule lives>>
> "<the user's words>"
```

Five lines. No paragraph, no rationale beyond the quote. The quote is the
rationale.

## 6. Ask before you edit

Print the plan and wait. The user approves it before any file changes.

For each finding give one block:

```
<the rule, as an imperative>      <N asks, first date to last date>
  home:  <file>
  edit:  add | sharpen | delete | move from <file>
  text:  <the exact lines you will write>
```

Then the prune list: every entry you will merge, retire or drop, with the
reason in four words.

More than five findings makes that plan too long to read. Then ask in two
stages. Stage one is a table: the rule, the count, the home and the edit.
Stage two is the exact text, for the approved rows.

The user may change a home, reject a finding or rewrite the text. Apply that
answer, not your plan. Never edit a file before they answer.

The skill edits only these files: `.claude/CLAUDE.md`, `.claude/lessons.md`,
`docs/guidelines/*.md` and `.claude/skills/*/SKILL.md`. It never touches source
code, a design note or a file outside the repository.

## 7. Apply and report

After approval, make the edits, then report:

- Each rule you wrote, and the file it went to.
- Each rule you deleted, moved or superseded, and the file it left.
- The `lessons.md` entry count, before and after.

Run `.claude/skills/technical-writing/lint.sh` on every file you changed.

Then stop. Do not act on the findings in the same turn.
