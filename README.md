# Reconify

[![MIT License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Reconify is an open-source Go CLI and library for reconciling financial records from banks, payment service providers, ledgers, and spreadsheets.

It normalizes rows from different systems, matches transactions using explicit rules, and reports the evidence needed to investigate exceptions:

- exact matches;
- missing records on either side;
- amount and timing differences;
- duplicate groups; and
- grouped settlements such as one-to-many and many-to-many payouts.

## Install

Install Reconify from a release binary, with Go, or from source. Release binaries are published for macOS (Apple Silicon and Intel), Linux (amd64), and Windows (amd64).

Before installing, check whether Reconify is already available and identify a directory on `PATH` that is appropriate for this machine. Do not install a binary for a different operating system or CPU architecture.

### Release binaries

Find every release on the [Reconify releases page](https://github.com/ReconifyHQ/reconify/releases), or use the latest-release URLs below after confirming the target matches the machine.

#### macOS (Apple Silicon)

~~~bash
curl -L -o reconify https://github.com/ReconifyHQ/reconify/releases/latest/download/reconify-darwin-arm64
chmod +x reconify
sudo mv reconify /usr/local/bin/
reconify --version
~~~

#### macOS (Intel)

~~~bash
curl -L -o reconify https://github.com/ReconifyHQ/reconify/releases/latest/download/reconify-darwin-amd64
chmod +x reconify
sudo mv reconify /usr/local/bin/
reconify --version
~~~

#### Linux (amd64)

~~~bash
curl -L -o reconify https://github.com/ReconifyHQ/reconify/releases/latest/download/reconify-linux-amd64
chmod +x reconify
sudo mv reconify /usr/local/bin/
reconify --version
~~~

#### Windows (amd64)

~~~powershell
Invoke-WebRequest -Uri "https://github.com/ReconifyHQ/reconify/releases/latest/download/reconify-windows-amd64.exe" -OutFile "reconify.exe"
~~~

Move `reconify.exe` into a directory on `PATH`, then open a new terminal and verify it:

~~~powershell
reconify --version
~~~

On Unix-like systems, prefer a user-writable directory already on `PATH`; use elevated privileges only when necessary. If there is no compatible release binary, use the Go installation below only after confirming that Go is installed.

### Go install

~~~bash
go install github.com/reconifyhq/reconify/cmd/reconify@latest
~~~

Make sure your Go binary directory is on `PATH`, then verify the installation:

~~~bash
reconify --version
reconify capabilities
~~~

### Build from source

~~~bash
git clone https://github.com/ReconifyHQ/reconify.git
cd reconify
go mod download
make build
./reconify --version
~~~

The build uses linker flags for version and build time.

<details>
<summary>Install the agent skills</summary>

If you are driving Reconify from a coding agent, install the guided workflows too:

~~~bash
npx @reconifyhq/skills
~~~

This installs the canonical workflows and tool-specific adapters into:

~~~text
.agents/skills/   # tool-agnostic workflows
.claude/skills/   # Claude adapters
.codex/skills/    # Codex adapters
~~~

The package includes workflows for reconciliation, bootstrapping, configuration, CLI usage, debugging, performance, and CI. The canonical names are `reconify-engine-*`; the older `reconify-*` names remain compatibility adapters.

For an unfamiliar task, read [AGENTS.md](AGENTS.md), [llms.txt](llms.txt), and the `reconify-engine-reconcile` workflow. The installed CLI exposes its current machine-readable contract through:

~~~bash
reconify capabilities
reconify config schema
reconify schema result
reconify schema diagnostic
reconify schema verification
~~~

</details>

### Where Reconify writes files

Reconify reads local files. Your config controls input paths through `file_pattern`, `--left-file`, and `--right-file`. When using the disk index backend, Reconify writes temporary SQLite-backed index files to `index.spill_dir` or the system temporary directory.


## Quickstart

Create a configuration interactively:

~~~bash
reconify config init
~~~

For a hand-written configuration, map each source to its input columns and define the pair to compare:

~~~yaml
version: 1
timezone: UTC

sources:
  ledger:
    file_pattern: "data/ledger/*.csv"
    parser:
      type: csv
      date_col: Date
      date_layout: "2006-01-02"
      amount_col: Amount
      multiplier: 100
      ref_col: Reference
      name_col: Description

  psp:
    file_pattern: "data/psp/*.csv"
    parser:
      type: csv
      date_col: Date
      date_layout: "2006-01-02"
      amount_col: Amount
      multiplier: 100
      ref_col: Reference
      name_col: Description

pairs:
  ledger_vs_psp:
    left: ledger
    right: psp
    date_window: "1d"
    amount_tolerance_minor: 0
    name_mode: none
~~~

Then validate and run it:

~~~bash
reconify config validate --config reconify.yaml
reconify reconcile --config reconify.yaml \
  --pair ledger_vs_psp --format json --out result.json
~~~

See [examples/reconify.yaml](examples/reconify.yaml) for a larger configuration with resource budgets, multiple counterpart sources, grouped passes, and parser options.

## Inputs and normalization

Sources can read CSV, JSON, NDJSON, XLSX, and XLSM files. Set `type: auto` or omit `type` to infer the parser from the file extension. Legacy `.xls` files are not supported; save them as `.xlsx` or `.csv` first.

Every input row is normalized into a transaction. Amounts are stored as integer minor units: with `multiplier: 100`, `1500.00` becomes `150000`. Dates use Go layouts such as `2006-01-02`, and the parser can apply a configured timezone, decimal separator, and thousands separator.

### Financial effects and settlement checks

Sources may declare additional monetary columns under `parser.financials`. They use the same
normalization rules as `amount_col` and support field, fixed, percentage, fixed-plus-percentage,
and component-sum expectations:

~~~yaml
financials:
  gross_col: Gross
  net_col: Net
  fields: {fee: Fee, tax: Tax}
  expectations:
    fee:
      percentage: {base: gross, rate: 1.5}
      operation: subtract
      tolerance_minor: 1
~~~

Configured cells must be present, non-empty, and valid. Financial findings are independent from
transaction match classification. `financial_effect_diff` and `settlement_diff` are exception
events; mapped fields without an expectation produce informational `financial_unchecked` events.
Sources without `financials` retain the existing output behavior.

Optional mappings include:

- `ref_col` for the reference used by exact matching;
- `group_col` for duplicate detection when several valid rows share a reference;
- `name_col` for optional token matching; and
- `currency_col` for currency metadata and monetary totals.

Run `reconify inspect FILE --format json` to inspect a file before choosing these mappings. Run `reconify parse --config reconify.yaml --source SOURCE --file FILE` to inspect normalized transactions.

## Matching behavior

The default pipeline matches one row on the left to one row on the right by reference, amount tolerance, and date window. A result is classified as:

- `match` when reference, amount, and date all satisfy the pair rules;
- `amount_diff` when the reference and date match but the amount is outside tolerance;
- `timing_diff` when the reference and amount match but the date is outside the window; or
- `unmatched_left` / `unmatched_right` when no counterpart can be reconciled.

Optional matching passes include:

- `name_tokens_one_to_one`, using Jaccard similarity for rows without a reference match;
- `one_to_many`, for one aggregate row against several rows sharing a group key;
- `many_to_many`, for groups on both sides whose totals should be compared; and
- `subset_sum`, for a bounded subset of right-side rows whose amounts sum within tolerance.

Duplicate detection is an annotation pass. It reports duplicate groups but does not discard rows or prevent them from participating in matching.

Read the [engine guide](docs/content/docs/cli/engine/index.md) before changing matching behavior.

## Results and output modes

`reconcile` supports these formats:

| Format | Memory profile | Use when |
|---|---|---|
| `json` | Buffers the full result | You need a conventional JSON object or deterministic output |
| `json-stream` | Releases Go objects early, but bytes accumulate | You need a streaming JSON object |
| `ndjson` | O(1) result memory | You need a crash-safe event stream or large-file output |
| `csv` | O(1) result memory | You need a flat tabular export |
| `table` | Buffers the full result | You need a human-readable terminal view |

For large jobs, prefer `ndjson` or `csv`:

~~~bash
reconify reconcile --config reconify.yaml --pair ledger_vs_psp \
  --format ndjson --out result.ndjson \
  --progress --progress-out progress.ndjson \
  --heartbeat-every 30s
~~~

Progress and diagnostics go to stderr. Reconciliation data goes to stdout or `--out`. `--progress-out` must be different from `--out`.

Use `--result-mode` to control emitted item events without changing the classification counters or monetary totals in the summary:

| Mode | Emits |
|---|---|
| `all` | Every event; default |
| `exceptions_only` | Diffs, unmatched rows, duplicates, and ambiguous groups; clean matches are suppressed |
| `summary_only` | Only the final summary |

For audits, `--audit` adds file hashes, timestamps, tool version, and the pair configuration snapshot. Combine it with `--audit-fixed-timestamp` when byte-identical reruns are required and the format supports audit data.

## Large files and resource limits

The right-side index can use one of four backends:

- `memory` for the fastest lookups and highest RAM use;
- `disk` for lower RAM use with SQLite temporary files;
- `auto` to choose based on file size and configured resource budgets; or
- `partitioned` for bounded-memory CSV reconciliation with disk-backed partitions.

Configure the backend and safety budgets under `index`:

~~~yaml
index:
  backend: auto
  spill_dir: /tmp/reconify
  max_memory_mb: 8192
  max_temp_disk_mb: 16384
  partition_count: 0
~~~

Budgets are safeguards, not throughput guarantees. Reconify reports the selected backend and its estimates in structured output. A run fails before completion if the selected strategy cannot satisfy its configured memory or temporary-disk budget.

Read the [performance guide](docs/content/docs/cli/performance/index.md) before changing streaming, indexing, or large-file behavior. Read [partitioned parallelism](docs/architecture/partitioned-parallelism.md) before changing partition workers, result chunks, carry-forward, or queue behavior.

## Automation contract

Use `--agent` for machine-readable defaults and `--error-format json` for structured diagnostics on stderr:

~~~bash
reconify reconcile --agent --error-format json \
  --config reconify.yaml --pair ledger_vs_psp \
  --format ndjson --out result.ndjson
~~~

Exit codes are stable for scripts and CI:

| Code | Meaning |
|---:|---|
| `0` | Command succeeded |
| `1` | Unexpected or internal error |
| `2` | Configuration, validation, or usage error (unknown command or flag, wrong argument count, invalid flag value) |
| `3` | Reconciliation completed with unmatched rows when `--fail-if-unmatched` is set |
| `4` | Reconciliation completed with exception events when `--fail-if-exceptions` is set; takes precedence over `3` |
| `5` | `reconify verify` found a failing check; exit `2` is used instead when the config itself is invalid |

Usage errors previously exited `1` as `INTERNAL_ERROR`. They now exit `2` with diagnostic code `USAGE_ERROR`, `details.usage`, and, when a close match exists, `details.did_you_mean`.

Config validation failures carry every problem as `details.errors`, an array of `{"path", "message"}` objects. Under `--agent` or `--error-format json` the JSON envelope is the only stderr output.

When the config defines exactly one pair, `reconcile` and `verify` default `--pair` to it. With several pairs, pass `--pair`; without it the `CONFIG_INVALID` diagnostic lists the names in `details.pairs`.

`reconify config infer LEFT RIGHT` accepts the two input files positionally, equivalent to `--left` and `--right`.

### Verify the deliverables

`reconify verify` checks the three files an agent workflow produces and prints a `reconify.engine.verification.v1` checklist:

~~~bash
reconify --agent verify --config reconify.yaml --result result.json --explanation explanation.json
~~~

It confirms that the config validates, that each source's `file_pattern` still resolves and matches its file, that a fresh run reproduces the summary counters in `result.json` (whatever `--format` or `--result-mode` wrote it), and that `explanation.json` equals `reconify explain result.json`. A missing `result.json` or `explanation.json` is skipped unless `--result` or `--explanation` names it. The checklist is JSON under `--agent` or when stdout is not a terminal, and a text checklist on a terminal (`--format json|text` overrides).

To record every invocation, set `RECONIFY_TRACE_FILE=/path/trace.jsonl`; each `reconify` process then appends one JSON line with `argv`, `exit_code`, `duration_ms`, and `diagnostic_code`.

The versioned schemas are available through `reconify schema capabilities`, `reconify schema result`, `reconify schema diagnostic`, `reconify schema profile`, `reconify schema explanation`, `reconify schema verification`, and `reconify config schema`.

## Go library

The `config` and `engine` packages can be imported by other Go modules:

~~~go
package main

import (
	"github.com/reconifyhq/reconify/config"
	"github.com/reconifyhq/reconify/engine"
)

func reconcile() error {
	cfg, err := config.Load("reconify.yaml")
	if err != nil {
		return err
	}

	left, err := engine.Parse("ledger", "ledger.csv", cfg.Sources["ledger"].Parser)
	if err != nil {
		return err
	}
	right, err := engine.Parse("psp", "psp.csv", cfg.Sources["psp"].Parser)
	if err != nil {
		return err
	}

	_, err = engine.Reconcile("ledger_vs_psp", "ledger", "psp", left, right, cfg.Pairs["ledger_vs_psp"])
	return err
}
~~~

For large inputs, prefer the CLI streaming path or the engine streaming APIs documented in the engine package.

## Development

Prerequisite: Go 1.25.0 or newer.

~~~bash
go mod download
go run ./cmd/reconify --help
go test ./...
make build
~~~

Before opening a pull request after a code change, run the repository quality gate:

~~~bash
make check
~~~

`make check` covers module drift, formatting, dependency boundaries, linting, security scans, race-tested coverage, builds, and smoke benchmarks. `make preflight` is an alias.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the pull request process. The documentation site lives under [docs/](docs/), and the installable agent workflows are packaged under [skills/](skills/).

## License

Reconify is released under the [MIT License](LICENSE).
