---
name: reconify-engine-reconcile
description: Reconcile unfamiliar financial files end to end. Use when one task spans input discovery, config creation or repair, Engine execution, and result explanation.
---

# Reconify Engine Reconcile

Produce a correct, reproducible reconciliation, not a high match rate. Complete the checkpoints in
order from the directory that will contain `reconify.yaml`.

## Routing

| Situation | Skill |
|---|---|
| Unfamiliar files, or one task spanning discovery through explanation | stay here |
| No `reconify.yaml` yet, and the outcome is a first validated config plus a first result | `reconify-engine-bootstrap` |
| A `reconify.yaml` exists and needs creation detail, correction, or validation | `reconify-engine-config` |
| A run succeeded but the counters contradict the business scenario | `reconify-engine-debug` |
| Memory, wall time, or artifact size is the binding constraint | `reconify-engine-performance` |
| The workflow must run unattended with an exit-code policy | `reconify-engine-ci` |

This skill owns the end-to-end sequence. Delegate only for config detail at checkpoint 3, a large
input at checkpoint 4, and diagnosis at checkpoint 5 or 6, then return here. Do not hand the whole
task to another skill.

## Artifact convention

| File | Meaning |
|---|---|
| `reconify.yaml` | the validated config; commit it |
| `result.json` | the retained reconciliation artifact; regenerate it, and commit only as a reviewed baseline |
| `explanation.json` | the explanation of that result; regenerate it alongside |

Any other file you create is scratch. Say so in the final report rather than leaving it unexplained.

## 1. Discover the build and inputs

Locate every input before inspecting it, then query the installed binary:

```bash
reconify capabilities
reconify inspect INPUT_LEFT
reconify inspect INPUT_RIGHT
```

Inspect every counterpart in a multi-source run. This checkpoint is complete when the actual input
paths are known and `capabilities` confirms the commands, formats, schemas, and passes needed by the
task.

## 1a. Repair an existing workspace

If discovery finds a `reconify.yaml` already present, repair it rather than replacing it.

Preserve the original first — copy it to `reconify.yaml.bak` and leave that copy uncommitted. Never
overwrite the file before diagnosing it.

Then diagnose in this order, because each command sees a different class of failure:

```bash
reconify config validate --config reconify.yaml
reconify config check-source --config reconify.yaml --source SOURCE --file INPUT
reconify reconcile --config reconify.yaml --pair PAIR --format json \
  --result-mode all --deterministic --out result.json
```

| Command | What its failure means |
|---|---|
| `config validate` | The config shape is wrong: a missing required field, an unknown key, an invalid enum |
| `config check-source` | The parser mapping no longer fits the file you named: a renamed header, an unparseable date layout, a wrong column |
| `reconcile` | Everything above is consistent, but a `file_pattern` resolves to no file, or the pair is misnamed |

`check-source` takes an explicit `--file` and therefore cannot detect a stale `file_pattern`; only
`reconcile` resolves the pattern. Compare each configured `file_pattern` against the paths found in
checkpoint 1 before assuming the mappings are at fault.

Make the smallest change that clears the reported diagnostic, rerun from `validate`, and repeat. Do
not rewrite mappings that already pass `check-source` against the real inputs.

Treat any pre-existing `result.json` as stale evidence, not an answer key; regenerate it at
checkpoint 4. Once validation and every source check pass but the counters still contradict the
business scenario, read `../reconify-engine-debug/SKILL.md`.

## 2. Record the evidence and policy

From each profile, record the candidate date, amount, reference, name, currency, and grouping
columns; copy exact date layouts and note ambiguous inferences. Settle these business decisions with
the user or state an explicit assumption:

- the shared transaction identifier;
- the amount difference that is acceptable, expressed in minor units;
- the permitted date offset;
- whether repeated references represent duplicates, one-to-many groups, or many-to-many groups;
- whether gross, net, fees, taxes, or other effects must satisfy a financial expectation; record the
  expected sign operation and tolerance in minor units.

Classify every such decision before acting on it:

| Tier | Definition | Action |
|---|---|---|
| Proven | The value is visible in `inspect` output or a source file | Use it and cite the evidence |
| Safe default | Ambiguous, but every plausible reading produces the same matching outcome | Choose it and record it as an assumption |
| User decision | Plausible readings produce different matching outcomes | Ask the user before proceeding |

A repeated reference is the canonical user decision: read as a duplicate it raises
`duplicate_count`, read as a one-to-many settlement group it raises `grouped_matched_count`, and
the two answers are not interchangeable. A tolerance wide enough to absorb a real fee is the same
kind of decision.

This checkpoint is complete when every mapping and tolerance is proven, defaulted with a recorded
assumption, or decided by the user.

## 3. Build and prove the config

Read `../reconify-engine-config/SKILL.md` and follow its workflow. Keep each `file_pattern` relative
to the config file, then run:

```bash
reconify config validate --config reconify.yaml
reconify config check-source --config reconify.yaml --source SOURCE --file INPUT
```

Run `check-source` for every configured source. This checkpoint is complete only when validation and
all source checks pass against the real inputs.

When both inputs have at least 100 rows, `config infer` is a fast path to a first draft:

```bash
reconify config infer LEFT RIGHT
```

Treat the proposal as evidence, never as a config: copy only mappings you have confirmed against the
`inspect` profiles, then run `validate` and `check-source` as above. A single-pair config lets
`reconcile` and `verify` omit `--pair`; keep it explicit in retained commands anyway.

## 4. Produce the retained result

Use explicit artifact settings so the result includes clean matches:

```bash
reconify reconcile --config reconify.yaml --pair PAIR \
  --format json --result-mode all --deterministic --out result.json
```

For inputs too large to buffer, read `../reconify-engine-performance/SKILL.md` and choose a streaming
artifact deliberately. This checkpoint is complete when the command succeeds, the output parses in
its declared format, and its final summary is present.

If `parser.financials` is configured, also inspect the financial and settlement event categories and
their summary counters. Financial findings are additive: a financial difference does not reclassify
a normal transaction match. Under `exceptions_only`, clean financial matches and unchecked findings
are suppressed while financial and settlement differences remain visible.

## 5. Explain and challenge the result

```bash
reconify explain result.json > explanation.json
```

Compare the summary counters with the expected business scenario. A surprising counter is evidence
to investigate; read `../reconify-engine-debug/SKILL.md`, correct the earliest unsupported mapping or
policy decision, and rerun from config validation.

This checkpoint is complete when the explanation names the same counters as the result and every
surprising difference is either corrected or reported as unresolved.

## 6. Verify the deliverables

```bash
reconify verify --config reconify.yaml --pair PAIR
```

`verify` checks the config, each source file, that `result.json` reproduces on a fresh deterministic
run, and that `explanation.json` agrees with `result.json`. It exits `0` when every check passes or
is skipped and `2` when any check fails. Read the failing entries
in `checks[]`, correct the cause at the earliest checkpoint it points to, regenerate the artifacts,
and rerun. A `skip` means that artifact does not exist yet; produce it rather than accepting the skip.

This checkpoint is complete when `verify` exits `0` with no skipped artifact check.

## Recovery and completion

On any command failure, read its diagnostic, correct the current checkpoint, and rerun that command.
A missing guessed path is recoverable: discover the actual path and continue. Diagnostics carry a
stable `code`; recover by code:

| Diagnostic | Recovery |
|---|---|
| `USAGE_ERROR` | Read `details.usage` and `details.did_you_mean`, run `reconify COMMAND --help`, and correct the invocation. Never guess flags. |
| `CONFIG_INVALID` | Fix each entry in `details.errors[]` at its `path`, then rerun `config validate`. With several pairs and no `--pair`, `details.pairs` lists the names. |
| `INPUT_MISMATCH`, `INPUT_UNREADABLE` | The file does not fit the mapping, or cannot be read. Rerun `inspect` on the real file and `check-source`; fix the path, `file_pattern`, header, or date layout. |
| `INFERENCE_AMBIGUOUS` | Read `details.reasons`, choose each uncertain mapping from the `inspect` profile, and write it explicitly. |
| `VERIFICATION_FAILED` | Read the failing entries in `checks[]` and fix the artifact or mapping each names. |

Finish with all three deliverables, and only after `reconify verify` exits `0`:

- validated `reconify.yaml`;
- retained `result.json`;
- retained `explanation.json`.

The final report states, in this order:

- the config, result, and explanation paths;
- the pair reconciled;
- the summary counters: matched, unmatched left, unmatched right, amount differences, timing
  differences, duplicates, and grouped matches;
- every assumption, tagged with its tier from checkpoint 2;
- unresolved warnings or diagnostics;
- the exact commands used to verify the work, ending with the passing `reconify verify`.

Writing YAML or obtaining one successful command is an intermediate state, not completion. The
deliverables are done only when `reconify verify` passes.
