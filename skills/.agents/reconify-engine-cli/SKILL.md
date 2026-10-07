---
name: reconify-engine-cli
description: Resolve Reconify CLI behavior from the installed binary. Use when choosing commands, flags, formats, schemas, or exit-code handling.
---

# Reconify Engine CLI

## Resolve the installed contract

```bash
reconify capabilities
reconify COMMAND --help
reconify schema NAME
reconify config schema
```

Start with `capabilities`, then load only the help or schema for the branch being implemented. The
CLI decision is complete when the chosen command, flags, format, schema identifier, and exit policy
all appear in the installed contract.

## Keep streams separate

Result data goes to stdout or `--out`. Diagnostics, warnings, validation messages, and progress go
to stderr. Use `--error-format json` when a caller needs the
`reconify.engine.diagnostic.v1` contract, and branch on documented exit codes rather than message
text.

## Recover from structured diagnostics

Under `--agent` (or `--error-format json`) stderr carries one `reconify.engine.diagnostic.v1`
envelope and nothing else. Branch on `diagnostic.code`, then read `details`:

| Code | Exit | Recover with |
|---|---|---|
| `CONFIG_INVALID` | 2 | `details.errors` is a list of `{path, message}`; fix each `path` in `reconify.yaml`, then rerun `config validate`. `details.pairs` lists the pair names when several exist and `--pair` was omitted. |
| `USAGE_ERROR` | 2 | The command line is wrong, not the config. Apply `details.did_you_mean` when present, otherwise run the command named in `suggestions` (`reconify COMMAND --help`); `details.usage` is the usage line. |
| `VERIFICATION_FAILED` | 5 | Read the failing checks in the `verify` output on stdout. |

A usage error is never an Engine fault: do not retry it unchanged or report it as a bug.

## Shortcuts

`reconify config infer LEFT RIGHT` takes the two files positionally; `--left`/`--right` remain
valid, and mixing the two forms is a `USAGE_ERROR`. `reconcile` and `verify` default `--pair` when
the config defines exactly one pair; pass it explicitly when there are several.

## Verify the deliverables

```bash
reconify --agent verify
```

`verify` checks `reconify.yaml`, `result.json`, and `explanation.json` together and prints a
`reconify.engine.verification.v1` checklist (`reconify schema verification`): the config validates,
each source's `file_pattern` resolves and matches its mapping, a fresh run reproduces the summary
counters in `result.json`, and `explanation.json` equals `reconify explain result.json`. Each check
is `pass`, `fail`, or `skip`; a missing artifact is skipped unless `--result` or `--explanation`
names it. Exit `0` means every check passed or was skipped, `2` an invalid config, `5` any other
failing check. Fix the first failing check and rerun; do not hand-edit an artifact to satisfy it.

## Choose the retained artifact deliberately

| Need | Choose |
|---|---|
| Complete, stable document for review or diffing | `json`, `--result-mode all`, `--deterministic` |
| Bounded-memory event stream | `ndjson` or `csv` |
| Lower encoding pressure while retaining one JSON document | `json-stream` |
| Interactive inspection of a small result | `table` |

`json` and `table` buffer the result. `ndjson` and `csv` remain bounded with result size;
`json-stream` releases encoded structs early but still accumulates one JSON document. Confirm the
chosen behavior against `capabilities` and command help for the installed build.

Financial-enabled runs add `financial_effect_match`, `financial_effect_diff`, `financial_unchecked`,
`settlement_match`, and `settlement_diff` events plus financial counters in `summary`. Use
`exceptions_only` to retain financial and settlement differences while suppressing clean financial
matches and unchecked findings. `--fail-if-exceptions` also fails for those differences.

## Understand the agent profile

`--agent` selects machine-readable defaults and exception-focused result emission. It fits callers
that consume differences as events. A retained complete result uses explicit `--format` and
`--result-mode all`; explicit flags override the profile defaults.

CLI work is complete when a representative invocation produces data in the declared format,
diagnostics remain on stderr, and the caller handles every exit code it opted into.
