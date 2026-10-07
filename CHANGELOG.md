# Changelog

All notable changes to Reconify will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- **`reconify verify`** — checks the agent workflow deliverables and prints a `reconify.engine.verification.v1` checklist: `config_valid`, `source_check:<name>` (each `file_pattern` resolved relative to the config file, then the same checks as `config check-source`), `result_present`, `result_reproducible` (a fresh run must reproduce the recorded summary counters, for any result format or mode), `explanation_present`, and `explanation_consistent` (equals `reconify explain`). Exits `0` when every check passes or is skipped and `2` when any check fails: `CONFIG_INVALID` for an invalid config, `VERIFICATION_FAILED` otherwise. It reuses the Agent Protocol exit codes rather than adding one. Published as `reconify schema verification`.
- **`RECONIFY_TRACE_FILE`** — when set, every `reconify` process appends one JSON line (`argv`, `exit_code`, `duration_ms`, `diagnostic_code`) to that file on exit. Write failures never change output or exit codes.
- **Structured validation errors** — `CONFIG_INVALID` diagnostics from config validation carry `details.errors`, an array of `{path, message}`. Under `--agent` or `--error-format json` the human `is invalid` listing is no longer written to stderr; the JSON envelope is the only stderr output.
- **`reconify config infer LEFT RIGHT`** accepts the two files positionally. Mixing positional files with `--left`/`--right` is a usage error.
- **Default `--pair`** — `reconcile` and `verify` use the only pair when the config defines exactly one. With several pairs, the `CONFIG_INVALID` diagnostic lists them in `details.pairs`.
- The published diagnostic schema allows the `usage` and `verification` categories, and `capabilities` lists `verify`, `schema verification`, and the `USAGE_ERROR` and `VERIFICATION_FAILED` codes.

### Changed

- **Usage errors now exit `2` with `USAGE_ERROR`** instead of exiting `1` as `INTERNAL_ERROR`. This covers unknown commands and flags, wrong argument counts, and invalid flag values (`--top abc`). The diagnostic has category `usage`, legacy code `config_error`, `details.usage`, `details.did_you_mean` when a close command or flag exists, and a suggestion naming `reconify <command> --help`. Scripts that matched exit code `1` for these cases must now match `2`. Bare group commands (`reconify config`) still print help; an unknown subcommand under them is now a usage error rather than silently printing help.
- Commands that accept no positional arguments now reject them as usage errors instead of ignoring them.

### Fixed

- **Stable event order** — `duplicate` groups, `unmatched_right` rows, ambiguous groups, `source_summary` events, and rows replayed under `duplicate_policy: latest` were emitted in map-iteration order, so repeated runs over the same files produced different bytes. They now follow input row order (duplicate groups by first occurrence) in every format and execution path, including the disk and partitioned backends.
- **`--deterministic` with `--format json`** — the flag was silently ignored because the result-mode wrapper hid the writer's setter. It now takes effect, and deterministic ordering sorts by source and numeric row (`left-2` before `left-10`). Formats that cannot honor the flag still warn on stderr.

## [0.7.0] - 2026-08-30

### Added

- **Financial effects and settlement reconciliation** — optional `parser.financials` configuration supports fixed, percentage, fixed-plus-percentage, and component-sum expectations in minor units, with gross/net settlement checks, dependency validation, exact rounding, and overflow protection ([#131](https://github.com/ReconifyHQ/reconify/pull/131), [#132](https://github.com/ReconifyHQ/reconify/issues/132)).
- **Financial result events** — reconciliation outputs now support `financial_effect_match`, `financial_effect_diff`, `financial_unchecked`, `settlement_match`, and `settlement_diff` across JSON, JSON-stream, NDJSON, CSV, and table formats.
- **Financial CI and agent workflows** — financial differences participate in `exceptions_only`, `--fail-if-exceptions`, explanations, published schemas, and the canonical Reconify agent skills.

### Changed

- Financial findings are additive to ordinary matching; a fee or settlement discrepancy does not reclassify a normal transaction match.
- The `@reconifyhq/skills` package and Reconify Engine release metadata are prepared for minor version `0.7.0`.

## [0.6.0] - 2026-08-24

### Added

- **`subset_sum` matching pass** — matches one left row against a combinatorially bounded subset of right rows summing within tolerance, for settlement workflows with no reliable shared key. Includes candidate filters, branch-and-bound pruning over mixed-sign amounts, ambiguity detection, and a candidate limit; the pass, its events, and its config keys are published in the capabilities contract, the config schema, and the result and explanation schemas ([#38](https://github.com/ReconifyHQ/reconify/issues/38), [#42](https://github.com/ReconifyHQ/reconify/issues/42)).
- **Agent skill release gate** — `reconify-eval release` evaluates the working-tree skills package against an explicitly published baseline and a no-skill control over the same corpus, with seeded arm randomization, explicit per-agent model selection, retained trial workspaces, resume, experiment provenance, arm comparisons, and a verdict. Available as `make eval-release BASELINE_VERSION=x.y.z`.
- **Semantic result grading in the evaluator** — a trial's classification now compares a normalized event multiset against the independently verified answer key, so a config that produces the right summary counters through the wrong matching is graded as a failure. The counter equality check remains as the `assertions_match` diagnostic, and `exact_match` remains reported rather than gating.
- **Skill routing table** — `reconify-engine-reconcile` now states which situation belongs to which skill, and that it owns the end-to-end sequence and delegates for detail rather than handing off the whole task.
- **Repair workflow for existing configs** — the reconcile and config skills describe preserving the original `reconify.yaml`, then diagnosing with `config validate`, `config check-source`, and `reconcile` in that order, because each detects a different class of failure. `config check-source` takes an explicit `--file` and therefore cannot detect a stale `file_pattern`; only `reconcile` resolves the pattern.
- **Ambiguity tiers** — the skills now separate values proven by the files, safe defaults that are recorded as assumptions, and decisions that require the user because plausible readings produce different matching outcomes, such as a repeated reference read as a duplicate versus a one-to-many settlement group.

### Changed

- **Agent artifact convention** — the skills now produce `reconify.yaml`, `result.json`, and `explanation.json`. The previous `agent-result.json` and `agent-explanation.json` names were evaluator-specific and no longer ship to users; the evaluator accepts either name so packages published before this release remain comparable in a release matrix.
- **Neutral evaluation task prompt** — the evaluator's task wrapper states the business problem and the required artifacts instead of dictating the command sequence it is meant to be measuring.
- **Agent final report** — the reconcile skill now requires a fixed report: config, result, and explanation paths, the pair reconciled, the summary counters, every assumption tagged with its tier, unresolved warnings, and the commands used to verify.

### Fixed

- `subset_sum` events were dropped in the batch CLI drain path.
- Streaming ambiguous-match counters could go negative because right rows were decremented once per alternative instead of once per unique row.
- Zero eligible candidates was reported as `candidate_limit_exceeded`.
- An empty subset could satisfy a large tolerance and register as a match.
- The batch summary dropped ambiguity-consumed right-row amounts from `TotalDiscrepancy` instead of tracking them as ambiguous.
- `candidate_filters` with every field explicitly false was indistinguishable from unset and silently ignored.
- Branch-and-bound pruning only handled non-negative amounts, so `same_sign: false` could miss valid mixed-sign subsets.
- The token-buffer pressure warning did not fire for `subset_sum`-only streaming runs.
- Bounded skills-package extraction in the release gate so a malformed archive cannot exhaust local disk.

## [0.5.0] - 2026-08-22

### Added

- **Reconify Engine Agent Protocol v1** — defines the stable discovery, inspection, configuration, execution, diagnosis, and evaluation contract for agents integrating with an installed Reconify Engine.
- **`reconify capabilities`** — emits `reconify.engine.capabilities.v1`, allowing agents and scripts to discover supported commands, formats, matching passes, result modes, schemas, diagnostic codes, and exit codes from the installed binary.
- **Published Engine schemas** — `reconify schema` now exposes versioned contracts for capabilities, reconciliation results, structured diagnostics, file profiles, config proposals, explanations, and evaluation scenarios.
- **Structured diagnostics** — `--error-format json` emits `reconify.engine.diagnostic.v1` envelopes on stderr with stable codes, categories, structured details, and remediation suggestions.
- **Canonical Engine agent skills** — `@reconifyhq/skills` now installs the complete `reconify-engine-reconcile`, `reconify-engine-cli`, `reconify-engine-config`, `reconify-engine-debug`, `reconify-engine-performance`, `reconify-engine-bootstrap`, and `reconify-engine-ci` workflows, plus deprecated `reconify-*` compatibility adapters.
- **Engine agent evaluation corpus** — eight graded fixtures under `evals/` that measure how well an external coding agent can configure the Engine from a natural-language problem description: missing payment, duplicate, settlement fee, timing difference, one-to-many, many-to-many, multi-provider, and ambiguous reference. Each scenario publishes a `reconify.engine.eval-scenario.v1` contract (`reconify schema eval-scenario`) naming its inputs, reference config, expected result, and asserted summary counters. Candidates are graded on reconciliation behavior rather than YAML text, through four gates: `valid`, `runs`, `summary_match`, and `exact_match`. Every scenario also ships counter-examples that must *not* reproduce the expected result, so the corpus proves it discriminates on every `make check` ([#109](https://github.com/ReconifyHQ/reconify/issues/109)).
- **Global `--agent` execution profile** — defaults errors to structured JSON and reconciliation output to NDJSON with `exceptions_only`, while preserving explicit flag overrides and rejecting interactive commands with a structured alternative ([#107](https://github.com/ReconifyHQ/reconify/issues/107)).
- **`reconcile --auto`** — confidence-gated zero-config reconciliation from exactly two input files, with the inferred YAML and mapping confidence embedded in structured run metadata for reproducibility ([#112](https://github.com/ReconifyHQ/reconify/issues/112)).
- **`reconify explain`** — reads JSON, JSON-stream, or NDJSON reconciliation results and emits deterministic `reconify.engine.explanation.v1` findings with bounded top exception events; no reconciliation or subjective severity is added.
- **`reconify config infer`** — non-interactively proposes a confidence-gated `reconify.yaml` from two input files. It emits `reconify.engine.config-proposal.v1` with mappings, alternatives, validation counts, and YAML; `--out` writes only ready proposals. `reconify schema config-proposal` prints the published schema.
- **Sample-row validation for `config check-source`** — checks the first 10 data rows by default for date and amount parsing errors. Use `--rows 0` for headers only or set a bounded custom sample size.
- **`reconify inspect FILE`** — deterministically profiles a CSV, JSON, NDJSON, XLSX, or XLSM file before a `reconify.yaml` mapping exists: format, per-column type inference (date layouts, amount formats, ambiguity flags), and representative sample values. Emits `reconify.engine.profile.v1` to stdout (`reconify schema profile` prints the published schema). Scans 1,000 rows by default; `--full` performs an exact scan. `--sample-values` controls how many distinct raw values are included per column (`0` disables). Part of the Reconify Engine Agent Protocol v1 roadmap (AP-5).
- **`--fail-if-exceptions` flag for `reconcile`** — exits with code 4 after a completed run if any `amount_diff`, `timing_diff`, or unmatched event was emitted. Superset of `--fail-if-unmatched`; when both flags are set and both conditions hold, exit code 4 takes precedence over exit code 3. Useful for strict financial pipelines where any discrepancy (not just a missing match) should fail the process.
- **`group_key` column in `parse` CSV and table output** — `parse --format csv` and `parse --format table` now emit `group_key` as the last column, matching the NDJSON and JSON formats. Note: this shifts nothing but adds a trailing column, so scripts reading CSV by column position are unaffected unless they assert on column count.
- **`left_currency` and `right_currency` columns in `csv` result output** — per-row `match`, `amount_diff`, `timing_diff`, and `unmatched_*` events now carry the transaction currency, so multi-currency reconciliations can be read without switching to JSON or NDJSON. Breaking: the columns are inserted after `left_name` / `right_name`, shifting all later columns — scripts reading the CSV by column position must be updated.
- **`config check-source` available columns hint** — when a required column is missing, the command now prints the available column names from the input file's header to stderr, so the user does not have to inspect the file separately.

## [0.4.1] - 2026-07-16

### Added

- **Partition-level parallel reconciliation** — partition workers now process independent partitions concurrently and publish results through a bounded, disk-backed queue while preserving deterministic final-writer ordering.
- **Partition queue controls** — `--partition-workers`, `--partition-queue-capacity`, and `--partition-max-chunk-mb` configure concurrency and temporary result chunk limits for the partitioned backend.
- **Partitioned parallelism documentation** — documents worker scheduling, spool cleanup, cancellation, queue bounds, and multi-source ordering guarantees.
- **`one_to_many` reconciliation pass** — matches one left transaction against N right transactions by summing amounts; includes ambiguous group detection, monetary invariants, and a `GroupedEventWriter` interface for grouped result emission.
- **`group_by` field for `one_to_many` passes** — defaults to `"reference"`, validated against known built-in keys, and wired into `matchByReferenceOneToMany`.
- **Many-to-many reconciliation pass** — supports matching left and right sets with arbitrary cardinality.
- **Configurable duplicate handling policy (`duplicate_policy`)** — four values: `flag` (default), `keep`, `merge`, `latest`; both batch and streaming paths (including multi-source) respect the policy.
- **Bounded-memory partitioned reconciliation backend** — new internal backend that splits large datasets into memory-safe partitions, enabling reconciliation of datasets that exceed available RAM.
- **Partitioned multi-source reconciliation** — extends the partitioned backend to run across multiple counterpart sources in a single pass.
- **Resource-aware index selection** — automatically selects the memory or disk index backend based on available RAM at runtime; Windows resource probing supported.
- **Reconciliation progress telemetry** — structured lifecycle events and progress reporting emitted to stderr during long-running reconciliations.
- **Configurable result emission modes (`--result-mode`)** — `all` (default), `exceptions_only`, and `summary_only` control which events are written to output, reducing noise for large runs.
- **Preflight quality and security gate** — `make check` now runs dependency audit, formatting, lint, security scan, race-tested coverage, build, and smoke benchmarks as a single local gate before opening a PR.
- **LLM-agent-ready CLI** — adds `--error-format json` for machine-readable error output, meaningful exit codes (0 = success, 1 = unmatched, 2 = validation failure, 3 = fatal error), `--fail-if-unmatched` flag, and `reconify config schema` command to emit the full config JSON schema.
- **Agent skill installer** — `npx @reconifyhq/skills` copies the canonical `.agents/skills/` files into any project; new skills added: `reconify-debug`, `reconify-bootstrap`, `reconify-ci`. Expanded `llms.txt` for LLM discoverability.
- **Benchmark suite** — deterministic and realistic benchmarks for the partitioned backend and multi-source reconciliation scenarios.

### Performance

- Partitioned single-source, grouped, and multi-source reconciliation can use bounded parallel workers without sharing the final result writer.
- Disk index inserts are now batched in transactions, reducing write overhead on large right-side files.
- Reduced disk index match write amplification — match entries are written more efficiently during the match pass.
- Bounded grouped partition reconciliation memory — grouped partitions no longer accumulate unbounded row sets.
- Optimized partitioned duplicate pre-scan — reduces redundant work when deduplicating rows across partition boundaries.

### Fixed

- Grouped partition sort failures are now propagated instead of producing an empty successful partition result.
- Partition workers preserve per-partition telemetry reporting for single-source and multi-source runs.
- Preserved partitioned grouped reconciliation semantics — grouping logic is now correctly applied within each partition boundary.
- Hardened resource-aware index selection — stabilized the probing heuristic and fixed a lint warning in the selection path.
- Hardened telemetry output and lifecycle events — telemetry events are flushed and reported correctly even on early cancellation.
- Hardened reconciliation correctness — addressed correctness risks around match scoring and aggregate accounting.
- Propagate cancellation signals and close sort cursors cleanly to avoid resource leaks on early exit.
- Report telemetry lifecycle failures consistently instead of silently swallowing errors.
- Hardened reconciliation diagnostics and aggregate computation for edge cases in large datasets.

### Refactored

- Split engine and CLI responsibilities — engine packages no longer import CLI concerns, making the library surface cleaner.
- Extracted acyclic packages from the engine to remove import cycles and improve testability.

## [0.3.0] - 2026-06-21

### Added

- **Multi-counterpart reconciliation (`rights`)** — A pair can now reconcile one left source against several ordered counterpart sources, carrying unmatched left rows forward between passes.
- **Per-counterpart output summaries** — JSON, JSON stream, and NDJSON outputs now include source-level breakdowns via `by_source` / `source_summary` alongside the aggregate summary.
- **Explicit one-to-one reconciliation passes** — Pairs can define `passes` with `reference_one_to_one` and `name_tokens_one_to_one` to make the matching pipeline explicit.
- **Benchmark infrastructure** — Added deterministic and realistic benchmark generators, runners, validation helpers, and CI benchmark workflow scaffolding.
- **Pull request template** — Added a GitHub PR template and documented the expected PR workflow for agents.

### Changed

- Clarified multi-counterpart docs, examples, and configuration references, including the distinction between ordered counterpart sources (`rights`) and matching strategies (`passes`).
- Improved reconciliation correctness around explicit pass ordering, duplicate annotation, best-candidate selection, summary accounting, and batch/streaming parity.
- Updated docs links to the current Fumadocs content paths.

### Fixed

- `one_to_many` is no longer accepted as a configured pass type. It is not implemented in v0.3.0 and now fails validation instead of silently behaving like a no-op in batch reconciliation.

### Known Limitations

- Multi-counterpart CLI runs do not yet support `--audit`, `--right-file`, or token-mode streaming reconciliation.

## [0.2.0] - 2026-05-17

### Added

- **`reconify config init`** — Interactive wizard (powered by Huh TUI) that reads source file headers, asks you to map transaction fields, and writes a validated `reconify.yaml`. Flags: `--out` (destination path, default `reconify.yaml`) and `--force` (overwrite existing file).
- **Multi-format input parsing** — The parser now supports JSON arrays (`.json`), NDJSON/JSON-L (`.ndjson`), and Excel workbooks (`.xlsx`, `.xlsm`) in addition to CSV. Format is auto-detected from the file extension when `parser_type` is `auto` or unset.
- **Fumadocs documentation site** — New `docs/` site built with Fumadocs and Next.js, covering getting started, configuration reference, engine internals, and performance.

### Fixed

- Trailing content after a JSON array is now rejected with a descriptive parse error instead of being silently ignored.
- Reconcile audit `hashFile` now captures file size and modification time alongside the SHA-256 hash, surfacing metadata divergence in audit output.
- CSV output fields are sanitised against spreadsheet formula injection — values starting with `=`, `+`, `-`, `@`, `\t`, or `\r` are escaped via `SanitizeCSVField`.

### Changed

- `JSONStreamWriter` documentation clarified: it is not O(1) memory. Use `NDJSONWriter` or `CSVWriter` when memory must stay constant with result size.

## [0.1.1] - 2026-03-23

### Fixed

- Corrected Go module path to `github.com/reconifyhq/reconify` across all import references.

## [0.1.0] - 2026-03-23

### Added

#### CLI Commands
- **`reconify reconcile`** — Core reconciliation command that matches transactions between two CSV sources using a 3-tier matching engine (reference exact match, name token similarity via Jaccard scoring, and duplicate detection).
- **`reconify parse`** — Parse and inspect a single CSV file according to source configuration. Useful for debugging parser setup. Supports `ndjson`, `csv`, `table`, and `json` output formats.
- **`reconify config validate`** — Validate configuration file structure and syntax, checking all required fields.
- **`reconify config check-source`** — Validate a CSV file's column structure against a source definition in the config.
- **`reconify validate`** — Standalone data quality validation for source files before reconciliation. Reports record-level, source-level, and cross-source issues with error/warning severity.

#### Reconciliation Engine
- 3-tier matching algorithm: reference exact match, optional name-token similarity (Jaccard, threshold > 0.5), and duplicate detection.
- Configurable date window (`date_window`) and amount tolerance (`amount_tolerance_minor`) per pair.
- Streaming two-pass architecture: build right-side index in pass 1, match left-side in pass 2, with O(1) memory for matching.
- Throughput of ~91k–105k rows/sec on 20M x 20M reconciliations.

#### Index Backends
- **Memory index** — Hash map backed, fastest performance, default backend.
- **Disk index** — SQLite-backed with WAL journaling for lower RAM usage on large datasets.
- **Auto backend** — Switches between memory and disk based on right-file size threshold (`auto_max_right_file_mb`, default 2048 MB).

#### Output Formats
- `json` — Pretty-printed full result object (best for < 500k rows).
- `json-stream` — Line-by-line JSON encoding with lower GC pressure.
- `ndjson` — One tagged JSON line per event, O(1) memory, crash-safe.
- `csv` — Fixed-schema tab-separated output, O(1) memory.
- `table` — Aligned ASCII table for interactive inspection.

#### Data Validation
- Record-level checks: parseable dates and amounts, valid currency format.
- Source-level checks: currency consistency, duplicate references, empty sources, high rate of empty references.
- Cross-source checks: currency compatibility, date range overlap, row count ratio disparity.
- `--fail-on-warning` flag to treat warnings as errors.
- Validation runs by default before reconciliation (skip with `--skip-validation`).

#### Audit & Reproducibility
- `--audit` flag embeds run provenance in output: SHA-256 file hashes, timestamps, tool version, and pair config snapshot.
- `--audit-fixed-timestamp` freezes timestamp and run ID for byte-identical reruns.
- `--deterministic` flag sorts output sections for stable diff-based audit trails.

#### CSV Parser
- Case-insensitive column lookup with trimmed whitespace.
- Configurable date layout, timezone, decimal/thousands separators.
- Parenthetical negative amount support (e.g., `(1,234.56)`).
- Minor-unit multiplier for currency conversion (e.g., dollars to cents).
- 1 MB read buffer with streaming architecture (`ParseCSVEach`).
- Optional `skip_raw` to skip per-row raw map allocation for memory optimization.
- 1000-entry date parse cache for repeated date values.

#### CLI Flags & Configuration
- Global `--config` / `-c` flag with `RECONIFY_CONFIG` env var fallback.
- Global `--verbose` / `-v` for verbose output.
- `--version` flag for version and build time display.
- `--progress` and `--progress-every` for stderr progress logging on large files.
- `--max-token-buffer` for token-mode unmatched buffer row limit.
- `--left-file` / `--right-file` overrides for explicit CSV paths (bypasses glob patterns).

#### Configuration File
- YAML-based config (`reconify.yaml`) with version, timezone, sources, pairs, and index backend settings.
- Source definitions with file glob patterns, parser type, column mappings, and format options.
- Pair definitions with left/right source references, date window, amount tolerance, and name matching mode.

#### Build & Distribution
- Cross-compilation targets: Linux x86_64, macOS x86_64/ARM64, Windows.
- Version and build time injected via linker flags.
- Usable as a Go library via `config` and `engine` packages.

### Internal

- Module path set to `github.com/reconifyhq/reconify`.
- Saturating uint8 counter for duplicate tracking (capped at 2, memory-efficient).
- Bucket struct optimization: no `time.Time` pointer or redundant reference string in index entries.
- Conditional pass-3 file re-scan only when duplicates are detected.
