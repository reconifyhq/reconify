---
title: MCP Server
description: Run the Reconify Engine as an MCP server so Claude Code, Codex, and other MCP clients can reconcile local files with typed tools.
icon: Plug
---

# Reconify Engine MCP server

`reconify mcp` serves the Reconify Engine over the [Model Context Protocol](https://modelcontextprotocol.io)
(MCP) using the stdio transport. An MCP client such as Claude Code or Codex launches the process, lists its
tools, and calls them to run the same workflow a person would run in a terminal:

```
capabilities -> inspect_file -> infer_config -> validate_config -> check_source
             -> reconcile -> get_summary / list_exceptions / explain_result -> verify_workspace
```

The server identifies itself as `reconify-engine`. It works on local files only, needs no Reconify account,
and makes no network calls. Cloud operations, authentication, integrations, and monitoring are not part of it.

## Register the server

Install `reconify`, then register the command `reconify` with the argument `mcp`.

**Claude Code**

```bash
claude mcp add reconify-engine -- reconify mcp
```

Or commit a project-scoped `.mcp.json` (see `.mcp.json.example` at the repository root):

```json
{
  "mcpServers": {
    "reconify-engine": {
      "command": "reconify",
      "args": ["mcp"]
    }
  }
}
```

**Codex**

```bash
codex mcp add reconify-engine -- reconify mcp
```

Or add it to `~/.codex/config.toml`:

```toml
[mcp_servers.reconify-engine]
command = "reconify"
args = ["mcp"]
```

Any other MCP client works the same way: spawn `reconify mcp`, speak newline-delimited JSON-RPC 2.0 on
stdin/stdout.

Use `--workdir DIR` to pin the directory that relative paths resolve against. It defaults to the directory
the client launched the server in, which for project-scoped registrations is the project root.

## Tools

Every tool also accepts an optional `cwd` argument that overrides the working directory for that call. All
input schemas are closed (`additionalProperties: false`), and invalid arguments come back as a tool error
with a `USAGE_ERROR` diagnostic so the model can correct itself.

| Tool | Purpose | Key inputs | Writes files |
| --- | --- | --- | --- |
| `capabilities` | Describe the installed Engine: commands, formats, schemas, error codes. | none | no |
| `inspect_file` | Profile an input file's format, columns, types, and sample values. | `path`, `full?`, `sample_values?`, `sheet?` | no |
| `infer_config` | Propose a `reconify.yaml` from two files, gated on confidence. | `left`, `right`, `out?` | `out` only |
| `validate_config` | Validate `reconify.yaml`; failures list every problem in `details.errors`. | `config?` | no |
| `check_source` | Check a file against one configured source mapping. | `source`, `file`, `config?`, `rows?` | no |
| `reconcile` | Run one configured pair and write the result to a file. | `out`, `config?`, `pair?`, `format?`, `result_mode?`, `deterministic?` | `out` |
| `reconcile_auto` | Infer a config and reconcile two files in one call, when confident. | `left`, `right`, `out` | `out` |
| `get_summary` | Read only the summary counters from a result file. | `result` | no |
| `list_exceptions` | Page through exception events in a result file. | `result`, `types?`, `limit?`, `offset?` | no |
| `explain_result` | Deterministic `reconify.engine.explanation.v1` summary of a result. | `result`, `top?` | no |
| `verify_workspace` | Run `reconify verify` on the workflow deliverables. | `config?`, `pair?`, `result?`, `explanation?` | no |

### Results go to files, not into the conversation

`reconcile` and `reconcile_auto` require `out` and write the full result there. The tool response carries only
the run status, summary counters, and the output path. This keeps large reconciliations from flooding the
model's context. Read the result back in small pieces:

- `get_summary` returns the summary counters (and per-source breakdowns) without reading event payloads.
- `list_exceptions` streams the file and returns only exception events, never clean matches.
- `explain_result` returns the deterministic explanation with the top exceptions.

`list_exceptions` is paginated. It returns `total`, `counts_by_type`, `returned`, and `next_offset` while
more events remain. Events keep file order, so pages are stable. Exception types are `unmatched_left`,
`unmatched_right`, `amount_diff`, `timing_diff`, `duplicate`, `grouped_amount_diff`, `grouped_timing_diff`,
`many_to_many_amount_diff`, `many_to_many_timing_diff`, `ambiguous_group`, `financial_effect_diff`, and
`settlement_diff`. A result written with `result_mode: summary_only` holds no events to list.

Under the agent profile the default `result_mode` is `exceptions_only`, so clean matches are not written to
the result file unless you ask for `result_mode: all`. Summary counters always include them.

### Results and errors

Tools run the current `reconify` binary with `--agent` and explicit arguments (no shell), so output is the
CLI contract unchanged. A successful call returns `content` with the JSON as text and `structuredContent` with
the parsed object.

A failed call returns `isError: true` and the `reconify.engine.diagnostic.v1` envelope the CLI printed on
stderr, both as text and as `structuredContent`. Branch on `diagnostic.code` and read
`diagnostic.details`, for example `details.errors` for `CONFIG_INVALID` or `details.reasons` for
`INFERENCE_AMBIGUOUS`. Two cases are deliberately not errors:

- `reconcile` exit code `3` or `4` (only possible with `fail_if_unmatched` or `fail_if_exceptions`) means the
  run completed with findings. The response reports `exit_code` and the diagnostic, and the result file is
  written.
- `verify_workspace` exit code `2` with a verification checklist on stdout means the checks ran and at least one
  failed. Read `ok` and `checks`.

`verify_workspace` needs a `reconify` build that includes the `verify` command. On an older binary it returns
an error that says so.

## Trust boundary and cancellation

The server runs with the permissions of the user who launched it and can read and write any file that user
can. Tool paths are resolved against the working directory (or `cwd`), paths containing NUL bytes are
rejected, and `reconcile` and `reconcile_auto` refuse `-` as `out` so results can never be streamed inline.
Run it only from clients you trust, as you would the CLI itself.

Tool calls execute one at a time in the order received. While one runs, `ping` and cancellation still work:
a client `notifications/cancelled` for an in-flight call interrupts the underlying `reconify` process and no
response is sent for the cancelled request.

## Protocol details

- Transport: newline-delimited JSON-RPC 2.0 on stdin/stdout. stdout carries protocol messages only. Logs go to
  stderr, and only with `--verbose`.
- Methods: `initialize`, `ping`, `tools/list`, `tools/call`, and the `notifications/initialized` and
  `notifications/cancelled` notifications. Unknown methods return `-32601` and malformed JSON returns
  `-32700`. Notifications never receive a response.
- Protocol versions: the server echoes the client's requested version when it is one of `2025-11-25`,
  `2025-06-18`, `2025-03-26`, or `2024-11-05`, and otherwise answers with `2025-11-25`.
- Capabilities: `tools` only. The server does not expose resources, prompts, or sampling.
- Tool annotations: read-only tools set `readOnlyHint: true`. Tools that write files (`infer_config`,
  `reconcile`, `reconcile_auto`) set `readOnlyHint: false` and `destructiveHint: false`.

## Related

- [Agent Protocol v1](/docs/cli/reference/agent-protocol) for the schemas, diagnostics, and exit codes the
  tools return.
- [CLI reference](/docs/cli/reference/cli) for the commands behind each tool.
