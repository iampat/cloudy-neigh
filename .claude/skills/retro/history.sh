#!/usr/bin/env bash
# Print the messages the user typed in this repo's sessions, inside the window.
# Usage: history.sh [weeks]   (default 4)
set -euo pipefail

weeks="${1:-4}"
since=$(date -u -v-"${weeks}"w +%Y-%m-%d 2>/dev/null \
  || date -u -d "${weeks} weeks ago" +%Y-%m-%d)
dir="$HOME/.claude/projects/$(pwd | tr '/' '-')"

[ -d "$dir" ] || { echo "no transcripts at $dir" >&2; exit 1; }

for f in "$dir"/*.jsonl; do
  jq -rc --arg f "$(basename "$f")" --arg since "$since" '
    select(.type == "user")
    | select(.timestamp >= $since)
    | select(.message.content | type == "string"
             or (type == "array" and (map(select(.type == "tool_result")) | length) == 0))
    | {ts: .timestamp,
       f: $f,
       t: (if (.message.content | type) == "string"
           then .message.content
           else (.message.content | map(select(.type == "text") | .text) | join(" "))
           end)}
    | select(.t | length > 0)
    | select(.t | test("<system-reminder>|<command-name>|<task-notification>|<local-command-stdout>|Caveat: The messages below|Request interrupted|Base directory for this skill:|Review target:") | not)
    | "\(.ts[0:10])\t\(.f[0:8])\t\(.t | gsub("\n"; " | "))"
  ' "$f" 2>/dev/null
done | sort | awk -F'\t' '
  length($3) > 8000 { printf "dropped %d chars, %s: %.60s\n", length($3), $1, $3 > "/dev/stderr"; next }
  { print }
'
