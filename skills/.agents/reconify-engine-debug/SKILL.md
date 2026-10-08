---
name: reconify-engine-debug
description: Diagnose unexpected Reconify results. Use when counters, exception events, duplicates, or grouped matches disagree with the expected business scenario.
---

# Reconify Engine Debug

Trace the first unsupported assumption; preserve the original config and result as evidence.

## 1. Gather first-line evidence

```bash
reconify verify --config reconify.yaml --pair PAIR
```

Read the failing checks before reading any events. `result_reproducible` failing means the retained
`result.json` is stale or the run is not deterministic: regenerate it before trusting its counters.
`explanation_consistent` failing means `explanation.json` does not describe that result: regenerate
it. A `source_check` failure points at the mapping or `file_pattern` that no longer fits the file.
When every check passes, the artifacts are sound and the cause is in the mapping or policy.

## 2. Read the result contract and explanation

```bash
reconify schema result
reconify explain RESULT > explanation.json
```

For NDJSON, inspect individual events as well as the final summary. This checkpoint is complete when
the unexpected counter is tied to concrete event rows and source names.

## 3. Trace symptom to cause

| Symptom | First evidence to test |
|---|---|
| Everything unmatched | Both `ref_col` mappings contain the same identifier domain; both date layouts parse |
| Similar `amount_diff` values | A documented fee or FX policy explains the magnitude |
| `financial_effect_diff` events | The mapped field, expectation form, percentage base/rate, tolerance, and sign operation explain the difference |
| `settlement_diff` events | Gross, net, and configured effect components do not satisfy the declared settlement identity |
| `financial_unchecked` events | The field is mapped but no expectation is configured; treat it as informational unless the business requires a rule |
| Amounts differ by a factor of 100 | Both source multipliers express the same minor unit |
| One- or two-day `timing_diff` events | A documented settlement delay explains the offset |
| Duplicate events | The mapped reference is unique, or the config intentionally groups repeats |
| Ambiguous groups | The chosen group key and pass express the real cardinality |
| Empty names | Each parser maps the intended name column |

This checkpoint is complete when one mapping, parser rule, pass, or business policy explains the
observed rows.

## 4. Prove the correction

Reduce the inputs to the smallest fixture that retains the symptom. Correct the mapping or parser
first; change a tolerance only after identifying the exact rows it would newly absorb and confirming
they are the same transaction.

Then rerun:

```bash
reconify config validate --config reconify.yaml
reconify config check-source --config reconify.yaml --source SOURCE --file INPUT
reconify reconcile --config reconify.yaml --pair PAIR --format json \
  --result-mode all --deterministic --out corrected-result.json
reconify explain corrected-result.json > corrected-explanation.json
```

Debugging is complete when the reduced fixture proves the correction, the full run preserves all
expected counters, and the report names the causal config change. A lower match rate is acceptable;
silencing an unverified financial difference is not.

Financial differences are independent of ordinary matching. Do not widen `amount_tolerance_minor` to
hide a fee or settlement discrepancy; correct the financial expectation or source mapping instead.

When the correction ends the task, promote the corrected artifacts to `result.json` and
`explanation.json`, require `reconify verify` to pass, and report them using the completion
checklist in `../reconify-engine-reconcile/SKILL.md`.
