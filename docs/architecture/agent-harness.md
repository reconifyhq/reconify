# Agent Harness Architecture

The agent harness makes coding agents fast and correct when they reconcile financial files with the
Reconify Engine, and proves it with measurements. It has two loops:

- **Runtime harness (inner loop)** — what an agent touches while reconciling: the CLI and its
  structured diagnostics, `reconify verify`, the skills, the optional agent hooks, and the MCP server.
- **Eval harness (outer loop)** — runs real agents against graded scenarios, records a trace of every
  Engine call, grades each trial, labels the root cause of each failure, and gates releases.

Every runtime-harness feature is measured as an **arm** of the eval matrix (`no-skill`, `released`,
`candidate`, `candidate+hooks`, …), so each feature must show a measured lift.

```
EVAL HARNESS
 corpus (evals/) ─▶ sandbox (materialize) ─▶ agent adapters ─▶ graders ─▶ failure classifier
   tags, fixtures     skills / hooks arm       usage capture      grades      root-cause label
                      RECONIFY_TRACE_FILE  ─────────────────────▶ trace.jsonl
                                                         report.json ─▶ compare ─▶ gates (lanes 0/1/2)
RUNTIME HARNESS
 engine diagnostics · reconify verify · skills · hooks (.hooks/) · reconify mcp
```

## Contracts

These contracts are shared between packages. Change them additively only.

### Engine trace (`RECONIFY_TRACE_FILE`)

When the environment variable `RECONIFY_TRACE_FILE` is set, every `reconify` process appends exactly
one JSON line to that file when it exits:

```json
{"argv":["config","validate","--config","reconify.yaml"],"exit_code":2,"duration_ms":14,"diagnostic_code":"CONFIG_INVALID"}
```

- `argv` is `os.Args[1:]`.
- `diagnostic_code` is the `diagnostic.code` of the returned error, omitted on success.
- A failure to write the trace never changes the command's output or exit code.

The eval sandbox sets `RECONIFY_TRACE_FILE=<workspace>/.reconify-eval-trace.jsonl` and parses it into
`TrialReport.trace` (`internal/evals.TraceEntry`). The legacy `.reconify-eval-commands.log` wrapper
log remains for older binaries.

### Diagnostics

- **Validation failures** (`CONFIG_INVALID` from `config validate` and every other command that
  validates a config) carry `details.errors`: an array of `{"path": "sources.left.parser.date_col",
  "message": "required field is missing"}`. Under `--agent` or `--error-format json`, the human
  `❌ … is invalid` listing is not written to stderr; the JSON envelope is the only stderr output.
- **Usage errors** (unknown command, unknown flag, wrong argument count, invalid flag value) use code
  `USAGE_ERROR`, category `usage`, exit code `2`, legacy code `usage_error`. `details` carries
  `usage` (the command's usage line) and, when a close match exists, `did_you_mean`. The suggestion
  names `reconify <command> --help`. They are never `INTERNAL_ERROR`.

### Ergonomics

- `reconify config infer LEFT RIGHT` accepts positional files, equivalent to `--left/--right`.
  Mixing positional files and flags is a usage error.
- `reconify reconcile` and `reconify verify` default `--pair` when the config defines exactly one
  pair. With several pairs and no `--pair`, the `CONFIG_INVALID` diagnostic lists the pair names in
  `details.pairs`.

### `reconify verify`

Checks the deliverables of the agent workflow: `reconify.yaml`, `result.json`, `explanation.json`.

```
reconify verify [--config reconify.yaml] [--pair PAIR] [--result result.json] [--explanation explanation.json]
```

Writes `reconify.engine.verification.v1` JSON to stdout:

```json
{
  "schema": "reconify.engine.verification.v1",
  "ok": false,
  "config": "reconify.yaml",
  "pair": "left_vs_right",
  "checks": [
    {"name": "config_valid", "status": "pass"},
    {"name": "source_check:left", "status": "pass", "file": "left.csv"},
    {"name": "result_present", "status": "pass"},
    {"name": "result_reproducible", "status": "fail", "message": "summary.matched is 3 in result.json but 4 on a fresh run"},
    {"name": "explanation_present", "status": "skip", "message": "explanation.json not found"},
    {"name": "explanation_consistent", "status": "skip"}
  ]
}
```

- `status` is `pass`, `fail`, or `skip`. Failing checks may carry a nested `diagnostic`.
- `source_check:<name>` resolves each source's `file_pattern` relative to the config file, so a stale
  pattern fails here.
- `result_reproducible` reruns the pair deterministically and compares summary counters, so any
  `--format`/`--result-mode` the agent chose is accepted.
- `explanation_consistent` compares `explanation.json` with `reconify explain result.json`.
- Exit code `0` when every check passes or skips, `2` when the config is invalid, `5` when any other
  check fails. A missing optional artifact is `skip`, unless `--result`/`--explanation` named it.

The schema is published as `schemas/reconify.engine.verification.v1.json` and printed by
`reconify schema verification`. `capabilities` lists the command.

### Agent hooks (`skills/.hooks/`)

The skills package ships optional agent hooks under `skills/.hooks/<agent>/`. For Claude Code,
`skills/.hooks/claude/` mirrors a project `.claude/` directory: `settings.json` hook entries plus
scripts under `hooks/`. The hooks are thin adapters around `reconify verify`:

- after an edit to `reconify.yaml`: run `reconify --agent config validate` and report the diagnostic;
- before the agent stops: run `reconify --agent verify` and block completion while it fails.

`npx @reconifyhq/skills --hooks` installs them (merging, never clobbering, existing settings). The eval
sandbox installs the same files when `Options.Hooks` is set.

### Scenario corpus

`reconify.engine.eval-scenario.v2` gains additive fields:

- `tags` — tiers: `core`, `messy`, `scale`, `repair`, `ask-user`. Runners filter with `--tag`.
- `decision_keywords` — for ask-user scenarios; a non-gating grader checks the agent surfaces the
  decision.

Fixture CSVs are small, committed, and reproducible from `cmd/generate-eval-fixtures`.

### Eval report additions (`reconify.engine.eval-report.v1`, additive)

Per trial: `trace`, `usage`, `efficiency`, `grades`, and `failure`. The legacy boolean fields stay and
are derived from the grades.

Graders:

| Grade | Gating | Pass condition |
|---|---|---|
| `discovery` | no | trace contains `capabilities` |
| `configuration` | no | agent ran `config validate`, and the final config validates |
| `execution` | no | agent ran `reconcile`, and a result artifact exists |
| `classification` | **yes** | verified run's event multiset equals the answer key |
| `exact_result` | no | byte-equal deterministic result |
| `assertions_match` | no | summary equals the scenario assertions |
| `explanation` | no | agent ran `explain`, and the explanation equals the answer key |
| `protocol` | no | `config validate` precedes the first `reconcile` in the trace |
| `claims_consistent` | no | counters claimed in the final agent output equal the verified summary |
| `decision_surfaced` | no | ask-user scenarios only: the output mentions a decision keyword |

Failure labels are assigned only when `classification` fails. They are checked in order, and the
first match wins: `timeout`, `agent_error`, `missing_config`, `config_invalid`,
`file_pattern_unresolved`, `wrong_amount_mapping`, `wrong_date_layout`, `wrong_tolerance`,
`wrong_date_window`, `wrong_matching_strategy`, `missing_result_artifact`, `wrong_classification`.
Config-specific labels compare the agent's config with the reference config only to explain a failure
that behavior grading already established; grading itself never diffs YAML.

## Gates

| Lane | Trigger | What runs |
|---|---|---|
| 0 | `make check` | corpus honesty tests, skill/CLI drift lint, harness unit tests |
| 1 | `agent-evals` workflow: nightly, manual, or `run-agent-evals` label | `make eval-smoke`: 1 agent, 1 trial, tags `core,messy` |
| 2 | release | `make eval-release`: 4 agents × 3 trials × arms, with a verdict |

Compare two reports with `reconify-eval compare BASE.json HEAD.json [--markdown]`.
