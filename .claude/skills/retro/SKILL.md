---
name: retro
description: Review the recent session history for corrections Ali repeated, then update .claude/lessons.md. Ali triggers this skill. Never start it on your own.
allowed-tools: Read, Write, Edit, Bash, Grep, Glob
---

# Retro

Find what Ali had to say more than once. Write it down, so he does not say it
again.

Ali triggers this skill. Never fire it on your own, and never fold it into
another task.

## 1. Read the history

```sh
.claude/skills/retro/history.sh 4 > <scratchpad>/turns.txt
```

The argument is the window in weeks. Four is the default. The script prints one
line per message Ali typed: the date, the session, the text. It drops tool
output, system reminders and injected skill prompts. It reports a dropped long
message on stderr, so you can check what it removed.

The script gets the text. It does not find the meaning.

## 2. Read every message

A model reads the extract. A pattern does not.

Ali types fast and the text carries typos: "wirte", "currect", "undrestand",
"have yu update the the PR". A grep for the correct spelling misses those
lines, and a missed line hides a repeat.

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

> Read every message. Return each case where Ali gives an instruction about how
> the assistant must work, or corrects what the assistant did. For each case
> return: the instruction as an imperative, the date, the session, and the
> message quoted verbatim. Group nothing. Judge nothing. Typos are normal, so
> read for meaning.

Then merge the parts yourself. A subagent sees one slice. Only you see that a
case in week one and a case in week four are the same finding.

## 3. Group into findings

Group the cases by the action they ask for. Two cases join when the action is
the same, whatever the words.

Evidence makes a finding. Two shapes count:

- Ali gave the same instruction in two or more sessions.
- Ali corrected the same behaviour twice, in any words.

One case is not a finding. Note it and move on.

For each finding, record the action, the number of cases, the first date, the
last date, and the one quote that shows it best.

Count by the cases you collected in step 2. Do not count with grep. A typo
breaks the count the same way it breaks the search.

## 4. Classify each finding

Check the candidate against the rules that already exist:
`.claude/CLAUDE.md`, `docs/guidelines/*.md`, `.claude/skills/*/SKILL.md` and
`~/.claude/CLAUDE.md`.

| Case | Action |
| --- | --- |
| No rule covers it | Add it to `.claude/lessons.md` |
| A rule covers it, and I broke it again | Add it, marked as a repeat offence |
| A rule covers it, and every case falls outside the window | Drop it |

A repeat offence stays in lessons.md. The rule is not wrong. The rule does not
reach the moment of the mistake, and the short entry does.

## 5. Maintain `.claude/lessons.md`

The file is read at the start of every task, so its length is a tax. Prune
before you add.

- **Merge.** Two entries that ask for the same action become one entry.
- **Sharpen.** New evidence rewrites the entry it belongs to. Never append a
  second entry for the same lesson.
- **Promote.** An entry that holds for three passes belongs in `.claude/CLAUDE.md`
  or in a guideline file. Move it there, and delete it here. Report the move.
- **Retire.** Delete an entry with no evidence in the last two windows.
- **Cap.** Twenty entries. Over the cap, delete the entry with the oldest last
  date.

Never duplicate a rule that already lives in CLAUDE.md or a guideline file. A
duplicate rule drifts from its copy, and then two rules disagree.

### Entry format

```markdown
### <the rule, as an imperative>
**Do:** <the action, in one sentence>
<N asks, <first date> to <last date>><, repeat offence: <where the rule lives>>
> "<Ali's words>"
```

Five lines. No paragraph, no rationale beyond the quote. The quote is the
rationale.

## 6. Report

Give Ali the pass result:

- What you added, with the evidence count.
- What you merged, promoted or retired, and why.
- The entry count before and after.

Then stop. Do not act on the findings in the same turn.
