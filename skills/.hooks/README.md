# Reconify agent hooks

Optional hooks that keep a coding agent honest while it reconciles files with Reconify. They are thin
wrappers around the `reconify` CLI; the skills describe the workflow, the hooks enforce its gates.

Install them with the skills:

```bash
npx @reconifyhq/skills --hooks
```

Without `--hooks` the installer does not touch hooks or `.claude/settings.json`.

## Claude Code

`--hooks` copies `claude/hooks/*` to `.claude/hooks/` and merges `claude/settings.json` into your
project's `.claude/settings.json`. The merge only adds entries: existing keys and hooks are kept, and
running the installer again does not duplicate anything.

| Hook | Event | What it does |
|---|---|---|
| `reconify-post-edit.sh` | `PostToolUse` on `Write`, `Edit`, `MultiEdit` | When the edited file is named `reconify.yaml`, runs `reconify --agent config validate --config <file>`. On failure it exits 2 and prints the diagnostic JSON to stderr, which Claude Code feeds back to the agent. |
| `reconify-stop.sh` | `Stop` | When the project directory contains `reconify.yaml`, runs `reconify --agent verify`. On failure it exits 2 with the failing checks, so the agent keeps working until `reconify verify` passes. |

Both hooks exit 0 silently, and never block a session, when:

- `reconify` is not on `PATH`;
- the edited file is not `reconify.yaml` (post-edit), or the project has no `reconify.yaml` (stop);
- the Stop hook is already continuing because of an earlier block (`stop_hook_active`), which prevents
  loops;
- the installed `reconify` has no `verify` command.

The hooks read the hook JSON from stdin with `jq` when it is installed and fall back to `sed`/`grep`
otherwise.

## Opt out

- For one shell or session, set `RECONIFY_HOOKS=off`.
- To remove them from a project, delete the two `reconify-*` entries from `.claude/settings.json` and
  delete `.claude/hooks/reconify-post-edit.sh` and `.claude/hooks/reconify-stop.sh`.
- To never install them, run `npx @reconifyhq/skills` without `--hooks`.
