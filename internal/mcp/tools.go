package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/reconifyhq/reconify/schemas"
)

// tool is one MCP tool: its public definition plus the function that runs it.
type tool struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	Annotations map[string]any
	resolved    *jsonschema.Resolved
	run         func(ctx context.Context, s *Server, args map[string]any) toolResult
}

func (t *tool) definition() map[string]any {
	return map[string]any{
		"name":        t.Name,
		"title":       t.Title,
		"description": t.Description,
		"inputSchema": t.InputSchema,
		"annotations": t.Annotations,
	}
}

// toolResult is the outcome of a tools/call.
type toolResult struct {
	// text holds one text content block per entry. The first block mirrors the
	// structured content for clients that ignore structuredContent.
	text       []string
	structured any
	isError    bool
}

func (r toolResult) toMCP() map[string]any {
	content := make([]map[string]any, 0, len(r.text))
	for _, text := range r.text {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	out := map[string]any{"content": content, "isError": r.isError}
	if r.structured != nil {
		out["structuredContent"] = r.structured
	}
	return out
}

// jsonResult returns structured as both a JSON text block and structuredContent.
func jsonResult(structured any) toolResult {
	data, err := json.Marshal(structured)
	if err != nil {
		return errorResult("EXECUTION_FAILED", fmt.Sprintf("encode tool result: %v", err))
	}
	return toolResult{text: []string{string(data)}, structured: structured}
}

// errorResult builds an isError result carrying a reconify.engine.diagnostic.v1
// envelope, so MCP-originated failures look like CLI failures.
func errorResult(code, message string) toolResult {
	return errorResultExit(code, message, 0)
}

// errorResultExit is errorResult with an explicit process exit code recorded in
// the diagnostic details (0 keeps the default for the code).
func errorResultExit(code, message string, exitCode int) toolResult {
	category, exit, legacy := "execution", 1, "error"
	suggestion := "Rerun the tool; if the problem persists, check the reconify binary and file permissions."
	switch code {
	case "INVALID_ARGUMENTS":
		code, category, exit, legacy = "USAGE_ERROR", "usage", 2, "usage_error"
		suggestion = "Fix the tool arguments to match the tool's inputSchema and call it again."
	case "INPUT_UNREADABLE":
		category, exit, legacy = "input", 2, "config_error"
		suggestion = "Check that the path exists, is readable, and is relative to the working directory or absolute."
	case "INPUT_MISMATCH":
		category, exit, legacy = "input", 2, "config_error"
		suggestion = "Check that the file is a complete reconify result written by reconcile or reconcile_auto."
	}
	if exitCode > 0 {
		exit = exitCode
	}
	env := schemas.DiagnosticEnvelope{
		Error:  message,
		Code:   legacy,
		OK:     false,
		Schema: schemas.DiagnosticSchemaV1,
		Diagnostic: schemas.Diagnostic{
			Code:        code,
			Category:    category,
			Message:     message,
			Details:     map[string]any{"exit_code": exit, "legacy_code": legacy},
			Suggestions: []string{suggestion},
		},
	}
	data, _ := json.Marshal(env)
	var structured map[string]any
	_ = json.Unmarshal(data, &structured)
	return toolResult{text: []string{string(data)}, structured: structured, isError: true}
}

// ---------------------------------------------------------------------------
// Schema helpers
// ---------------------------------------------------------------------------

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "minLength": 1}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func intProp(desc string, minimum, maximum int) map[string]any {
	prop := map[string]any{"type": "integer", "description": desc, "minimum": minimum}
	if maximum > 0 {
		prop["maximum"] = maximum
	}
	return prop
}

func enumProp(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

const cwdDescription = "Optional working directory for this call; relative paths resolve against it. " +
	"Defaults to the server working directory."

// objectSchema builds a closed object schema and adds the shared cwd property.
func objectSchema(required []string, props map[string]any) map[string]any {
	all := map[string]any{"cwd": strProp(cwdDescription)}
	for k, v := range props {
		all[k] = v
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           all,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func annotations(title string, readOnly, idempotent bool) map[string]any {
	a := map[string]any{
		"title":         title,
		"readOnlyHint":  readOnly,
		"openWorldHint": false,
	}
	if !readOnly {
		a["destructiveHint"] = false
	}
	if idempotent {
		a["idempotentHint"] = true
	}
	return a
}

func resolveSchema(schemaMap map[string]any) (*jsonschema.Resolved, error) {
	data, err := json.Marshal(schemaMap)
	if err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return s.Resolve(nil)
}

// ---------------------------------------------------------------------------
// Tool catalogue
// ---------------------------------------------------------------------------

var resultFormats = []string{"json", "ndjson", "json-stream"}
var resultModes = []string{"all", "exceptions_only", "summary_only"}

func toolCatalogue() []*tool {
	exceptionTypes := strings.Join(exceptionTypeNames(), ", ")
	return []*tool{
		{
			Name:  "capabilities",
			Title: "Describe the Reconify Engine",
			Description: "Describe the installed Reconify Engine: version, commands, output formats, matching passes, " +
				"result modes, schema ids, diagnostic codes, and exit codes. Call this first in a new session; it is cheap and read-only.",
			InputSchema: objectSchema(nil, nil),
			Annotations: annotations("Describe the Reconify Engine", true, true),
			run:         runCapabilities,
		},
		{
			Name:  "inspect_file",
			Title: "Inspect an input file",
			Description: "Deterministically profile one input file (CSV, TSV, XLSX, ...) before writing a config: detected format, " +
				"delimiter, headers, per-column types, date layouts, amount formats, and representative raw values. " +
				"Scans the first 1,000 rows unless full is true. Use it on each side of a reconciliation before infer_config or hand-writing reconify.yaml.",
			InputSchema: objectSchema([]string{"path"}, map[string]any{
				"path":          strProp("Input file to profile, relative to the working directory or absolute."),
				"full":          boolProp("Scan the whole file for exact statistics instead of the first 1,000 rows. Slower on large files."),
				"sample_values": intProp("Representative raw values to return per column (default 3, 0 disables samples).", 0, 0),
				"sheet":         strProp("Sheet name for XLSX/XLSM files. Defaults to the first sheet."),
			}),
			Annotations: annotations("Inspect an input file", true, true),
			run:         runInspectFile,
		},
		{
			Name:  "infer_config",
			Title: "Infer a reconify.yaml from two files",
			Description: "Infer date, amount, and reference column mappings for two input files and return a config proposal " +
				"(reconify.engine.config-proposal.v1). Status is ready only when confidence gates pass; otherwise it is needs_input with " +
				"alternatives, and nothing is guessed. When out is set and the proposal is ready, the YAML config is written to that path; " +
				"if the proposal is ambiguous no file is written and the call returns an INFERENCE_AMBIGUOUS error carrying the reasons.",
			InputSchema: objectSchema([]string{"left", "right"}, map[string]any{
				"left":  strProp("Left input file (for example the bank export)."),
				"right": strProp("Right input file (for example the ledger export)."),
				"out":   strProp("Optional path to write the proposed reconify.yaml to. Overwrites an existing file."),
			}),
			Annotations: annotations("Infer a reconify.yaml", false, true),
			run:         runInferConfig,
		},
		{
			Name:  "validate_config",
			Title: "Validate reconify.yaml",
			Description: "Validate the structure of a reconify.yaml. Returns {valid: true} on success. On failure returns an error " +
				"whose diagnostic lists every problem in details.errors as {path, message}; fix them all and call again.",
			InputSchema: objectSchema(nil, map[string]any{
				"config": strProp("Path to the config file. Defaults to RECONIFY_CONFIG or reconify.yaml in the working directory."),
			}),
			Annotations: annotations("Validate reconify.yaml", true, true),
			run:         runValidateConfig,
		},
		{
			Name:  "check_source",
			Title: "Check an input file against a source",
			Description: "Check that an input file's headers and sample rows match the parser mapping of one configured source " +
				"(date, amount, currency, reference columns, layouts). Use it after validate_config and before reconcile to catch " +
				"wrong column names, date layouts, or amount formats cheaply.",
			InputSchema: objectSchema([]string{"source", "file"}, map[string]any{
				"config": strProp("Path to the config file. Defaults to RECONIFY_CONFIG or reconify.yaml in the working directory."),
				"source": strProp("Source name as defined under sources: in the config."),
				"file":   strProp("Input file to check against that source."),
				"rows":   intProp("Data rows to parse after the header check (default 10, 0 checks headers only).", 0, 0),
			}),
			Annotations: annotations("Check an input file against a source", true, true),
			run:         runCheckSource,
		},
		{
			Name:  "reconcile",
			Title: "Run a reconciliation",
			Description: "Run a configured reconciliation for one pair and write the full result to the file given in out. " +
				"Results are never returned inline; the call returns the run status and summary counters, and you then use " +
				"get_summary, list_exceptions, explain_result, or verify_workspace on the out file. Validate the config first. " +
				"Default format json is deterministic and best for small and medium files; use ndjson for very large inputs. " +
				"result_mode defaults to exceptions_only (clean matches are counted in the summary but not written); use all to keep them. Exit codes 3 and 4 (only possible with the fail_if_* options) " +
				"mean the run completed with findings and are not errors. Overwrites out if it exists.",
			InputSchema: objectSchema([]string{"out"}, map[string]any{
				"config":             strProp("Path to the config file. Defaults to RECONIFY_CONFIG or reconify.yaml in the working directory."),
				"pair":               strProp("Pair name under pairs: in the config. Always safe to pass; newer builds default it when the config has exactly one pair."),
				"out":                strProp("File to write the result to. Required; '-' (stdout) is not allowed."),
				"format":             enumProp("Result file format: json (default, single deterministic document), ndjson (one event per line, streaming), or json-stream.", resultFormats...),
				"result_mode":        enumProp("Which events to write: exceptions_only (default; omits clean matches), all, or summary_only (counters only, no events).", resultModes...),
				"deterministic":      boolProp("Sort the output for stable diffs (default true; only affects the json format)."),
				"fail_if_exceptions": boolProp("Exit 4 (reported as completed with findings, not an error) when any exception exists."),
				"fail_if_unmatched":  boolProp("Exit 3 (reported as completed with findings, not an error) when any row is unmatched."),
			}),
			Annotations: annotations("Run a reconciliation", false, true),
			run:         runReconcile,
		},
		{
			Name:  "reconcile_auto",
			Title: "Infer and reconcile two files",
			Description: "Zero-config reconciliation of exactly two files: infers the config and reconciles in one call, writing " +
				"the result (NDJSON, starting with a run_info event that records the inferred config) to out. It only runs when " +
				"inference passes its confidence gates; otherwise it fails with INFERENCE_AMBIGUOUS and the reasons, and writes no result. " +
				"In that case fall back to inspect_file, infer_config, and a hand-written config. Overwrites out if it exists.",
			InputSchema: objectSchema([]string{"left", "right", "out"}, map[string]any{
				"left":  strProp("Left input file."),
				"right": strProp("Right input file."),
				"out":   strProp("File to write the result to. Required; '-' (stdout) is not allowed."),
			}),
			Annotations: annotations("Infer and reconcile two files", false, true),
			run:         runReconcileAuto,
		},
		{
			Name:  "get_summary",
			Title: "Read result summary counters",
			Description: "Read only the summary counters (matched, unmatched, diffs, totals, rates, per-source breakdowns) from a result " +
				"file written by reconcile or reconcile_auto. Reads json, json-stream, and ndjson results without loading event payloads; " +
				"cheap even for huge files.",
			InputSchema: objectSchema([]string{"result"}, map[string]any{
				"result": strProp("Result file path, relative to the working directory or absolute."),
			}),
			Annotations: annotations("Read result summary counters", true, true),
			run:         runGetSummary,
		},
		{
			Name:  "list_exceptions",
			Title: "List reconciliation exceptions",
			Description: "Page through the exception events in a result file: " + exceptionTypes + ". Clean matches are never returned. " +
				"Events keep file order, so pages are stable. The response includes the total and per-type counts, and next_offset " +
				"while more remain. If the result was produced with result_mode summary_only there are no events to list; rerun reconcile.",
			InputSchema: objectSchema([]string{"result"}, map[string]any{
				"result": strProp("Result file path, relative to the working directory or absolute."),
				"types": map[string]any{
					"type":        "array",
					"description": "Only return these exception types. Defaults to all exception types.",
					"items":       map[string]any{"type": "string", "enum": exceptionTypeNames()},
					"uniqueItems": true,
				},
				"limit":  intProp("Maximum events to return (default 20, max 500).", 1, 500),
				"offset": intProp("Number of matching events to skip (default 0).", 0, 0),
			}),
			Annotations: annotations("List reconciliation exceptions", true, true),
			run:         runListExceptions,
		},
		{
			Name:  "explain_result",
			Title: "Explain a reconciliation result",
			Description: "Produce the deterministic explanation (reconify.engine.explanation.v1) of a result file: summary, per-category " +
				"finding counts, the total exception count, and the top exception events. Facts only; no severity judgments are added. " +
				"Save the output as explanation.json when the task asks for one.",
			InputSchema: objectSchema([]string{"result"}, map[string]any{
				"result": strProp("Result file path, relative to the working directory or absolute."),
				"top":    intProp("Maximum exception events to include (default 10).", 0, 0),
			}),
			Annotations: annotations("Explain a reconciliation result", true, true),
			run:         runExplainResult,
		},
		{
			Name:  "verify_workspace",
			Title: "Verify workspace deliverables",
			Description: "Check the deliverables of the agent workflow (reconify.yaml, result.json, explanation.json): the config is valid, " +
				"source files resolve, the result is present and reproducible by a fresh deterministic run, and the explanation is consistent. " +
				"Returns reconify.engine.verification.v1 with a pass/fail/skip status per check; ok=false means at least one check failed. " +
				"Run it before reporting the task finished. Requires a reconify build that includes the verify command.",
			InputSchema: objectSchema(nil, map[string]any{
				"config":      strProp("Path to the config file. Defaults to RECONIFY_CONFIG or reconify.yaml."),
				"pair":        strProp("Pair name under pairs: in the config. Newer builds default it when the config has exactly one pair."),
				"result":      strProp("Result file to verify. Defaults to result.json when present."),
				"explanation": strProp("Explanation file to verify. Defaults to explanation.json when present."),
			}),
			Annotations: annotations("Verify workspace deliverables", true, true),
			run:         runVerifyWorkspace,
		},
	}
}

func buildTools() ([]*tool, error) {
	tools := toolCatalogue()
	for _, t := range tools {
		resolved, err := resolveSchema(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s: invalid input schema: %w", t.Name, err)
		}
		t.resolved = resolved
	}
	return tools, nil
}
