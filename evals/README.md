# Reconify Engine agent evaluation corpus

Graded fixtures that measure how well an external coding agent (Claude Code, Codex,
Gemini CLI, or OpenCode) can configure the Reconify Engine from a
natural-language description of a reconciliation problem.

This directory is fixture data plus a machine-readable contract. The cross-agent runner
that consumes it is AP-10 (#110); the active contract is
`reconify.engine.eval-scenario.v2`, published in `schemas/` and printable with
`reconify schema eval-scenario-v2`. The original v1 contract remains published
for third-party fixture compatibility.

The v2 corpus exercises the non-interactive workflow: `capabilities`, `config validate`,
`reconcile`, and `explain`. It does not require `inspect` or `config infer`, although
scenario `012` is built so that inference can succeed.

## Tiers and tags

Every scenario carries `tags` in `scenario.json`. Runners filter on them (`--tag`); tags
never change grading. Lane 1 (`make eval-smoke`) runs `core,messy`.

| Tag | What it measures | Scenarios |
|---|---|---|
| `core` | One matching behavior per scenario on a handful of clean rows | 001-008 |
| `messy` | Real-world export formats the config must describe: number and date formats, bracketed negatives, JSON next to CSV | 009, 010, 011 |
| `scale` | A realistic row count (about 120 per side) with a few planted exceptions among clean rows; `config infer` can reach `ready` | 012 |
| `repair` | A broken `reconify.yaml` is already in the workspace and must be repaired, keeping the rules it encodes | 013, 014 |
| `ask-user` | The business meaning is undecided; the answer key is the conservative reconciliation and the agent should surface the open decision | 015 |

| Scenario | Tags | Business situation | Characterizing counters |
|---|---|---|---|
| 001-008 | core | see each `scenario.json` | see each `scenario.json` |
| 009-european-export | messy | Bank portal exports day-first dotted dates and `1.250,00` amounts with different column names | 5 matched, 1 amount diff, 1 timing diff, 1 unmatched each side |
| 010-accounting-negatives | messy | Report writer groups thousands with commas and shows refunds as `(320.00)` | 4 matched, 1 amount diff, 1 unmatched each side |
| 011-mixed-formats | messy | CSV ledger with its own columns and `03-Jan-2024` dates against a JSON bank export with timestamps | 4 matched, 1 amount diff, 1 unmatched each side |
| 012-scale-exceptions | scale | Month of card settlements, 120 vs 119 rows, two missing sales, one unknown credit, two wrong amounts, two very late payments | 114 matched, 2 amount diffs, 2 timing diffs, 2 unmatched left, 1 unmatched right |
| 013-repair-renamed-column | repair | The bank renamed its identifier column; the existing config must be fixed without losing its 3-day lag and 1.50 fee rules | 3 matched, 1 amount diff, 1 timing diff, 1 unmatched each side |
| 014-repair-stale-pattern | repair | The bank renamed its export file; last month's statement is still in the folder and must not be read | 3 matched, 1 amount diff, 1 unmatched each side |
| 015-ask-user-repeated-references | ask-user | One invoice appears twice on the bank statement; finance has not decided between duplicate and split payment | 2 matched, 1 amount diff, 1 unmatched right, 2 duplicate rows flagged |

### Grading ask-user scenarios

The answer key for an `ask-user` scenario is the conservative interpretation: the
reconciliation that flags the ambiguity for review instead of resolving it. `classification`
gates exactly as it does everywhere else, so an agent that guesses (for example by
merging the repeated rows or totaling them as a split payment) fails.

`decision_keywords` feeds the diagnostic `decision_surfaced` grade: it passes when the
agent's final output mentions any keyword, case-insensitively. It never gates a
release. It records whether the agent told the user what decision is needed, which a
behavior comparison cannot see.

## Release gate

The opt-in release gate compares the working tree package with an explicitly published
package and a no-skill control. It retains each trial workspace and report below
`.context/evals/`:

```bash
make eval-release BASELINE_VERSION=0.6.0 \
  CLAUDE_MODEL=... CODEX_MODEL=... GEMINI_MODEL=... OPENCODE_MODEL=...
```

The qualifying matrix uses all four supported agents and three trials per scenario.
Arm order is randomized from a recorded seed and defaults to one active agent call.
The neutral task wrapper describes only the business problem and required artifacts;
protocol evidence remains diagnostic while task success is the release metric. Exit
status 3 means a confirmed regression, 2 means an invalid or inconclusive run, and 0
means pass or pass-with-warnings.

## Layout

```
evals/
  README.md
  003-settlement-fee/
    scenario.json                       # the contract: prompt, paths, assertions
    inputs/left.csv right.csv           # what the agent is given
    reference/reconify.yaml             # a config known to behave correctly
    expected/result.json                # the reconciliation outcome to reproduce
    expected/explanation.json           # deterministic explanation answer key
    counter_examples/                   # configs that must NOT reproduce it
      tolerance-too-wide.yaml
  013-repair-renamed-column/
    reconify.yaml                       # repair scenarios only: the broken starting config
```

Repair scenarios list `"initial_files": ["reconify.yaml"]`. A runner copies each initial
file to the *same relative path* inside the agent workspace, so the broken config sits at
the scenario root precisely so that it lands at the workspace root, where the agent is
expected to repair it. The corpus tests prove it is genuinely broken: it must fail a gate
or produce different counters than the answer key.

`file_pattern` resolves relative to the **config file**, not the process working
directory. A runner therefore materializes a working directory per candidate config —
the scenario `inputs/` plus the config at the working-directory root — so the reference
config and an agent's config run through an identical path. `internal/cli/evals_test.go`
does exactly this and is the reference implementation.

## Scoring

**Grade behavior, not text.** Many different configs are legitimately correct: date
windows can differ, passes can be explicit or implicit, YAML anchors are a style choice.
Diffing an agent's YAML against `reference/reconify.yaml` produces false negatives. Run
the agent's config and compare the *result*. `reference/reconify.yaml` is a worked answer
for human reviewers, not the grading key.

The evaluator grades five observable workflow dimensions per trial:

| Dimension | Check | What it catches |
|---|---|---|
| discovery | agent invokes `reconify capabilities` | unsupported assumptions about the installed Engine |
| configuration | agent validates a root `reconify.yaml` | missing required fields such as `multiplier` |
| execution | agent reconciles to a retained result artifact (`result.json`, or `agent-result.json` from older packages) | file patterns that resolve to nothing |
| classification | the verified run's event multiset semantically matches the answer key | the wrong matching strategy, including summary-equivalent wrong outcomes |
| explanation | agent writes the expected deterministic explanation | unexplained or incorrect result artifacts |

`assertions_match` is reported alongside these as a diagnostic: it records whether the
scenario's declared `assertions` counters equal the verified run's summary. It is
deliberately weaker than `classification`, because two different reconciliations can
produce identical counters, so it does not gate a release.

**The headline metric is `summary_match` pass rate across the corpus.** Report
`exact_match` separately rather than gating on it — it is stricter than "the agent
understood the problem." The evaluator reports both `pass@1` and `pass^k` for
classification and exact-result correctness.

Results are byte-stable. `reconcile --format json --deterministic` emits no `run_id` or
timestamp (those appear only under `--audit`), so `exact_match` is a plain file
comparison with no normalization.

Agents are stochastic, so a single run is not a measurement. Run k trials per scenario
and report both `pass^k` (every trial passed — reliability) and `pass@1` (any trial
passed — the capability ceiling). The gap between the two is itself the interesting
signal for a tool that has to be trusted with financial data.

### `assertions` and the characterizing counter

`matched` counts reference one-to-one matches only. Grouped outcomes land in
`grouped_matched_count` and `many_to_many_matched_count`. Each scenario asserts the
counter that actually characterizes it, and zero values are meaningful: `005` and `006`
assert `duplicate_count: 0` because grouped rows sharing a reference get flagged as
duplicates unless the source sets a per-row-unique `group_col`.

## Keeping the corpus honest

An evaluation that everything passes measures nothing. Two properties are enforced by
`internal/cli/evals_test.go` on every `make check`:

- **The answer key is correct** — every reference config still reproduces its
  `expected/result.json` and its asserted counters.
- **Every scenario discriminates** — each counter-example must fail a gate or produce
  different counters. A scenario with no counter-example fails the test.

Discrimination is what forces the fixtures to carry rows that punish laziness. Without
the extra row in `003` whose amounts differ by far more than a fee, `amount_tolerance_minor:
100000` scores identically to the correct answer. The counter-example is the thing that
makes that row necessary.

- **Metadata is consistent** — tags are known, `decision_keywords` appear on exactly the
  `ask-user` scenarios, `inputs` lists exactly the files in `inputs/`, and repair
  scenarios start from a config that does not already reproduce the answer key.
- **Generated fixtures have not drifted** — see below.

Prompt leakage is partly automated: `TestEvalScenarioMetadata` rejects prompts that contain
config key names such as `multiplier` or `one_to_many`. The rest is a review rule, because
it can't be automated:

- **No answer leakage** — a prompt describes the business situation, never the config key
  under test. "The customer settled this invoice across several smaller payments" is a
  scenario; "configure a `one_to_many` pass" is a typing exercise. Describing the *data*
  (a date written `31.01.2024`, amounts shown as `1.250,00`) is fine, because a person
  holding the files would know it.

## Fixture generator

Inputs of scenarios `009`-`015` are written by `cmd/generate-eval-fixtures`, so the
committed CSV and JSON files are reproducible rather than hand-edited:

```bash
go run ./cmd/generate-eval-fixtures -corpus evals
```

The generator is deterministic: layouts are fixed, and the only randomness (the 120 rows
of `012`) comes from a `math/rand` source with an explicit seed. A test in that package
regenerates every file into a temporary directory and requires byte equality with the
committed fixture, and each generated file must stay under 20 KB. Change a fixture by
editing the generator, running it, and regenerating the answer key.

The generator writes **inputs only**. `expected/` comes from running the reference config
through the real CLI, which keeps the answer key an independent check on the fixtures.
`001`-`008` are small hand-written fixtures and are not generated.

## Authoring constraints the Engine imposes

Build scenarios only from what the Engine can do today. These limits shaped the corpus:

- CSV is comma-delimited; there is no delimiter option, so a semicolon-delimited export is
  not a scenario. Quoted fields (`"1.250,00"`) are.
- A header with a UTF-8 BOM leaves the first column unreachable by its plain name,
  preamble lines before the header cannot be skipped, and amounts are compared signed, so a bank that exports outflows as negatives against a
  ledger with positives cannot be reconciled by configuration.
- A `file_pattern` that matches several files reads only the first match in sorted order,
  and `date_window: 0d` disables the date check rather than requiring an exact date; use
  `1d` for a strict window.
- Duplicate groups and unmatched rows are not emitted in a stable order when there are
  several of them, which breaks the byte-stable answer key. Keep at most one duplicate
  group and one unmatched row per side in any scenario that has duplicates.

## Adding a scenario

1. Create `NNN-short-name/` with `inputs/`, and write the prompt as a business situation.
   For a generated scenario, add a builder to `cmd/generate-eval-fixtures` instead of
   writing the files by hand.
2. Write `reference/reconify.yaml` against `inputs/`, using `inputs/…` file patterns.
3. Generate the expected result from a materialized working directory:
   ```
   reconcile -c reconify.yaml --pair <pair> --format json --deterministic
   ```
   and save it as `expected/result.json`. Run
   `go run ./cmd/generate-eval-explanations` to refresh every deterministic
   `expected/explanation.json` fixture. Then read the result against the business story:
   the answer key must be what an accountant expects, not merely what the Engine printed.
4. Add at least one counter-example: a config a competent agent might plausibly write
   that is nonetheless wrong. If you can't make one fail, the fixture is too permissive —
   add a row that distinguishes the correct answer.
5. Write `scenario.json` with `tags` and the characterizing counters in `assertions`. For
   a `repair` scenario also list the broken config in `initial_files`; for an `ask-user`
   scenario add `decision_keywords`.
6. `make check`.
