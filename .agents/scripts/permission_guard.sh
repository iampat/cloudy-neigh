#!/usr/bin/env bash
set -euo pipefail

# Locate settings.json relative to the script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETTINGS_FILE="${SCRIPT_DIR}/../settings.json"

# Read the full JSON payload from stdin
PAYLOAD=$(cat)

# Extract tool details
TOOL_NAME=$(echo "$PAYLOAD" | jq -r '.toolCall.name // "unknown"')
COMMAND_LINE=$(echo "$PAYLOAD" | jq -r '.toolCall.args.CommandLine // ""')
FILE_PATH=$(echo "$PAYLOAD" | jq -r '.toolCall.args.AbsolutePath // .toolCall.args.path // ""')
CWD=$(echo "$PAYLOAD" | jq -r '.toolCall.args.Cwd // ""')

# If settings.json does not exist, fail-safe deny
if [[ ! -f "$SETTINGS_FILE" ]]; then
  echo -e "\n🚨🚨🚨 [HOOK ERROR] Missing settings.json at ${SETTINGS_FILE}! 🚨🚨🚨\n" >&2
  jq -n '{decision: "deny", reason: "Missing .agents/settings.json configuration file."}'
  exit 0
fi

# Load allowed rules array from settings.json
ALLOWED_RULES=$(jq -r '.permissions.allow[]?' "$SETTINGS_FILE")

IS_ALLOWED=false
MATCHED_RULE=""

# Check file/general tools against <tool_name>(*) rules
if [[ "$TOOL_NAME" != "run_command" ]]; then
  TARGET_TOOL_RULE="${TOOL_NAME}(*)"
  while IFS= read -r rule; do
    if [[ "$rule" == "$TARGET_TOOL_RULE" || "$rule" == "$TOOL_NAME" || "$rule" == "*" ]]; then
      IS_ALLOWED=true
      MATCHED_RULE="$rule"
      break
    fi
  done <<< "$ALLOWED_RULES"

# Check run_command against command(...) rules
else
  while IFS= read -r rule; do
    if [[ "$rule" == command\(*\) ]]; then
      pattern="${rule#command(}"
      pattern="${pattern%)}"
      
      # Match exact or glob pattern
      # Also treat 'prefix *' as matching 'prefix'
      trimmed_pattern="${pattern% \*}"
      
      if [[ "$COMMAND_LINE" == $pattern || "$COMMAND_LINE" == "$trimmed_pattern" || "$COMMAND_LINE" == $pattern* ]]; then
        IS_ALLOWED=true
        MATCHED_RULE="$rule"
        break
      fi
    elif [[ "$rule" == "*" ]]; then
      IS_ALLOWED=true
      MATCHED_RULE="*"
      break
    fi
  done <<< "$ALLOWED_RULES"
fi

# ==========================================================
# Decision & Loud Reporting
# ==========================================================
if [ "$IS_ALLOWED" = true ]; then
  if [[ "$TOOL_NAME" == "run_command" ]]; then
    echo "⚡ [HOOK:ALLOWED] Command: '${COMMAND_LINE}' (Rule: '${MATCHED_RULE}')" >&2
  else
    echo "🔍 [HOOK:ALLOWED] Tool: ${TOOL_NAME} | Target: ${FILE_PATH:-$TOOL_NAME}" >&2
  fi
  jq -n --arg reason "Allowed by rule '${MATCHED_RULE}' in .agents/settings.json" \
    '{decision: "allow", reason: $reason}'
  exit 0
else
  # LOUD SHOUT ON DENIAL TO STDERR
  echo -e "\n================================================================================" >&2
  echo -e "🚨🚨🚨 [PERMISSION DENIED BY HOOK] 🚨🚨🚨" >&2
  if [[ "$TOOL_NAME" == "run_command" ]]; then
    echo -e "❌ BLOCKED COMMAND : ${COMMAND_LINE}" >&2
    if [[ -n "$CWD" ]]; then
      echo -e "📁 WORKING DIR     : ${CWD}" >&2
    fi
    echo -e "📋 REASON          : Command does not match any 'command(...)' rule in .agents/settings.json" >&2
  else
    echo -e "❌ BLOCKED TOOL    : ${TOOL_NAME}" >&2
    echo -e "📁 TARGET          : ${FILE_PATH}" >&2
    echo -e "📋 REASON          : Tool '${TOOL_NAME}(*)' is not permitted in .agents/settings.json" >&2
  fi
  echo -e "================================================================================\n" >&2

  jq -n --arg name "$TOOL_NAME" --arg cmd "$COMMAND_LINE" \
    '{decision: "deny", reason: ("Permission denied: " + (if $cmd != "" then $cmd else $name end) + " is not in .agents/settings.json")}'
  exit 0
fi
