#!/bin/sh
# Reconify Stop hook for Claude Code.
#
# Before the agent stops, run `reconify --agent verify` in the project when it
# contains a reconify.yaml, and block completion (exit 2, stderr is fed back to
# the agent) while verification fails. Exits 0 silently when this is not a
# Reconify task, when reconify is not installed, when a previous stop was
# already blocked (stop_hook_active, prevents loops), or when RECONIFY_HOOKS=off.

[ "${RECONIFY_HOOKS:-}" = "off" ] && exit 0
command -v reconify >/dev/null 2>&1 || exit 0

input=$(cat)

# stop_hook_active is true when Claude is already continuing because of a Stop hook.
if printf '%s' "$input" | tr '\n' ' ' |
  grep -Eq '"stop_hook_active"[[:space:]]*:[[:space:]]*true'; then
  exit 0
fi

dir=${CLAUDE_PROJECT_DIR:-$PWD}
[ -f "$dir/reconify.yaml" ] || exit 0
cd "$dir" || exit 0

errfile=$(mktemp 2>/dev/null) || exit 0
trap 'rm -f "$errfile"' EXIT

out=$(reconify --agent verify 2>"$errfile")
status=$?
err=$(cat "$errfile")
[ "$status" -eq 0 ] && exit 0

# A reconify build without `verify` cannot gate completion; do not block on it.
if printf '%s' "$err" | grep -qi 'unknown command'; then
  exit 0
fi

{
  printf 'Reconify verification failed (exit %s). The deliverables are not done until `reconify verify` passes. Fix the failing checks below, rerun `reconify verify`, then finish.\n' "$status"
  failing=
  if command -v jq >/dev/null 2>&1 && [ -n "$out" ]; then
    failing=$(printf '%s' "$out" |
      jq -r '.checks[]? | select(.status == "fail") | "- \(.name): \(.message // .diagnostic.message // "failed")"' 2>/dev/null)
  fi
  if [ -n "$failing" ]; then
    printf 'Failing checks:\n%s\n' "$failing"
  else
    printf '%s\n%s\n' "$out" "$err" | head -c 4000
  fi
} >&2
exit 2
