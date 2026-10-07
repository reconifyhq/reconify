#!/bin/sh
# Reconify PostToolUse hook for Claude Code.
#
# After the agent writes or edits a file named reconify.yaml, validate it with
# `reconify --agent config validate`. On failure, exit 2 so Claude Code feeds
# the diagnostic JSON on stderr back to the agent. Every other situation exits
# 0 silently: other files, no reconify binary, or RECONIFY_HOOKS=off.

[ "${RECONIFY_HOOKS:-}" = "off" ] && exit 0
command -v reconify >/dev/null 2>&1 || exit 0

input=$(cat)

# Extract tool_input.file_path from the hook JSON: jq when present, otherwise a
# best-effort sed match (does not unescape JSON string escapes).
if command -v jq >/dev/null 2>&1; then
  file=$(printf '%s' "$input" | jq -r '.tool_input.file_path // empty' 2>/dev/null)
else
  file=$(printf '%s' "$input" | tr '\n' ' ' |
    sed -n 's/.*"file_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
fi

[ -n "$file" ] || exit 0
[ "$(basename -- "$file")" = "reconify.yaml" ] || exit 0
[ -f "$file" ] || exit 0

if out=$(reconify --agent config validate --config "$file" 2>&1); then
  exit 0
fi

{
  printf 'reconify.yaml failed validation after your edit (%s). Fix each reported error, then rerun `reconify config validate --config %s`.\n' "$file" "$file"
  printf '%s\n' "$out"
} >&2
exit 2
