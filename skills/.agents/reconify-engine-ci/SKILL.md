---
name: reconify-engine-ci
description: Automate Reconify in CI. Use when producing deterministic artifacts, mapping reconciliation outcomes to job status, or retaining failure evidence.
---

# Reconify Engine CI

## 1. Define the contract and policy

Run `reconify capabilities` for the binary installed in CI. Choose whether unmatched rows or all
exception events—including financial-effect and settlement differences—should fail the job, and record the corresponding flag and exit codes from that
contract. This checkpoint is complete when config failures and reconciliation-policy failures have
separate handling.

## 2. Produce a complete artifact

```bash
reconify reconcile --config reconify.yaml --pair PAIR \
  --format json --result-mode all --deterministic --out result.json \
  --fail-if-exceptions
```

Add `--audit` when provenance is required. Supply `--audit-fixed-timestamp RFC3339` when identical
inputs must produce byte-identical artifacts. Keep the config in version control beside the
workflow.

## 3. Preserve the Engine exit code

GitHub Actions runs shell steps with fail-fast behavior, so capture the status before applying the
job policy:

```bash
set +e
reconify reconcile --config reconify.yaml --pair PAIR \
  --format json --result-mode all --deterministic --out result.json \
  --fail-if-exceptions
status=$?
set -e

case "$status" in
  0) echo "clean" ;;
  2) echo "configuration failure" >&2; exit 1 ;;
  3|4) echo "reconciliation differences" >&2; exit 1 ;;
  *) echo "unexpected Engine failure: $status" >&2; exit "$status" ;;
esac
```

Verify these codes against `capabilities` when adopting a different Engine protocol version.

To gate the agent deliverables themselves, run `reconify --agent verify` after the artifacts exist.
Exit `0` is clean (skipped checks are not failures), `2` is a config failure handled like the
reconcile case above, and `5` means a deliverable is stale or inconsistent: treat it as a job
failure and retain the verification output with the artifacts. Name `--result` and `--explanation`
explicitly in CI so a missing artifact fails instead of being skipped.

When financial checks are configured, retain `financial_effect_diff` and `settlement_diff` events in
the failure artifact. `financial_unchecked` is informational and does not fail the job by itself;
`--fail-if-exceptions` fails for financial and settlement differences as well as ordinary exceptions.

## 4. Retain evidence on failure

Upload `result.json` with the CI platform's always-run condition so policy failures retain the
artifact that explains them. For large inputs, read `../reconify-engine-performance/SKILL.md` and
choose a streaming artifact plus explicit retention limits.

CI work is complete when the same commit and inputs reproduce the artifact, every opted-in exit code
has a deliberate job outcome, and failure evidence remains downloadable.
