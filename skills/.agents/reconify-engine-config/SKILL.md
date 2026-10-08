---
name: reconify-engine-config
description: Configure Reconify from real input files. Use when creating or correcting reconify.yaml, mapping columns, setting tolerances, or selecting matching passes.
---

# Reconify Engine Config

Treat the installed binary and the input profiles as the sources of truth.

## 1. Load the contract

```bash
reconify capabilities
reconify config schema
```

This checkpoint is complete when the build exposes every parser, key, enum, and matching pass the
planned config requires.

## 2. Profile every source

```bash
reconify inspect INPUT
```

For each source, record candidate mappings, ambiguity flags, representative values, and the exact
date layout. This checkpoint is complete when every configured column points to an observed header
and every date layout comes from the profile or a verified sample.

## 3. Establish mapping and policy

When a config already exists, repair it instead of rewriting it. Preserve the original as
`reconify.yaml.bak`, profile every source first, and change only the mappings the profiles
contradict or `validate` and `check-source` reject. Report what you preserved and why, alongside
what you changed.

Apply these rules together with the schema:

- Resolve `file_pattern` relative to the config file.
- Use Go date layouts such as `2006-01-02`.
- Set `multiplier` from the input unit. Engine amounts and `amount_tolerance_minor` are integers in
  the resulting minor unit.
- Map `ref_col` to the shared transaction identifier. Use `group_col` when a separate grouping key
  carries one-to-many or many-to-many semantics.
- Map `name_col` when name-token matching or names in result rows are required.
- Choose one `right` or an ordered `rights` list according to the business flow.
- Select matching `passes` from `capabilities`; keep their order intentional.

When sources expose fees, taxes, commissions, or gross/net amounts, configure the optional
`parser.financials` block after ordinary mappings are proven. Map `gross_col` and `net_col`
together when settlement arithmetic is required. Add named `fields` for parsed financial values and
`expectations` for derived values. Expectation values are minor units; percentage rates are written
as percentages (`1.5` means 1.5%), and percentage bases may reference `gross`, `net`, or another
mapped financial field. Use `components` plus `operation: add` or `subtract` for a settlement
identity. Missing mapped values produce informational `financial_unchecked` findings; invalid
arithmetic or overflow is a configuration/run error.

Set tolerances from business policy. For example, a permitted difference of 2.00 with
`multiplier: 100` becomes `amount_tolerance_minor: 200`. This checkpoint is complete when every
mapping and tolerance has evidence or a named assumption.

## 4. Use inference as evidence when useful

```bash
reconify config infer LEFT RIGHT
```

The positional form is equivalent to `--left INPUT_LEFT --right INPUT_RIGHT`; do not mix the two.
Read the proposal status, reasons, alternatives, and validation counts. Confirm every proposed
mapping against the profiles before copying it. A `needs_input` proposal routes the uncertain
mappings back to inspection and user policy; it is not a terminal state.

## 5. Validate and prove every source

```bash
reconify config validate --config reconify.yaml
reconify config check-source --config reconify.yaml --source SOURCE --file INPUT
```

Run `check-source` once per configured source and rerun both checks after every correction. A config
with exactly one pair lets later `reconcile` and `verify` runs omit `--pair`. When validation fails,
fix each entry in `details.errors[]` (its `path` names the field) and rerun. Config
work is complete only when validation and all real-file source checks pass. Report the validated
config path and every assumption that affects matching.
