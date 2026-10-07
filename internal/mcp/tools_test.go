package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilitiesTool(t *testing.T) {
	c := startToolClient(t, settlementWorkspace(t))
	out := c.callTool("capabilities", nil)
	if out.IsError {
		t.Fatalf("capabilities failed: %+v", out)
	}
	assertJSONText(t, out)
	if out.Structured["schema"] != "reconify.engine.capabilities.v1" {
		t.Fatalf("schema = %v", out.Structured["schema"])
	}
	commands, _ := out.Structured["commands"].(map[string]any)
	mcpCmd, ok := commands["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities does not list the mcp command: %v", commands)
	}
	if mcpCmd["interactive"] != false {
		t.Fatalf("mcp interactive = %v, want false", mcpCmd["interactive"])
	}
}

func TestInspectFileTool(t *testing.T) {
	dir := settlementWorkspace(t)
	c := startToolClient(t, dir)
	out := c.callTool("inspect_file", map[string]any{"path": "inputs/left.csv", "full": true, "sample_values": 1})
	if out.IsError {
		t.Fatalf("inspect_file failed: %+v", out)
	}
	if out.Structured["schema"] != "reconify.engine.profile.v1" {
		t.Fatalf("schema = %v", out.Structured["schema"])
	}
	scan, _ := out.Structured["scan"].(map[string]any)
	if scan["full"] != true {
		t.Fatalf("scan = %v, want full", scan)
	}

	missing := c.callTool("inspect_file", map[string]any{"path": "inputs/nope.csv"})
	if !missing.IsError || diagnosticCode(t, missing) != "INPUT_UNREADABLE" {
		t.Fatalf("missing file: %+v", missing)
	}
}

func TestValidateConfigTool(t *testing.T) {
	dir := settlementWorkspace(t)
	c := startToolClient(t, dir)

	ok := c.callTool("validate_config", nil)
	if ok.IsError || ok.Structured["valid"] != true {
		t.Fatalf("validate_config ok case: %+v", ok)
	}
	assertJSONText(t, ok)

	// A config with a missing parser column produces a CONFIG_INVALID diagnostic.
	writeFile(t, filepath.Join(dir, "bad.yaml"), "version: 1\nsources:\n  left:\n    file_pattern: inputs/left.csv\n    parser:\n      type: csv\npairs: {}\n")
	bad := c.callTool("validate_config", map[string]any{"config": "bad.yaml"})
	if !bad.IsError {
		t.Fatalf("expected isError for invalid config: %+v", bad)
	}
	if code := diagnosticCode(t, bad); code != "CONFIG_INVALID" {
		t.Fatalf("diagnostic code = %q: %+v", code, bad.Structured)
	}
	if bad.Structured["schema"] != "reconify.engine.diagnostic.v1" {
		t.Fatalf("structuredContent is not a diagnostic envelope: %+v", bad.Structured)
	}
	assertJSONText(t, bad)

	missing := c.callTool("validate_config", map[string]any{"config": "does-not-exist.yaml"})
	if !missing.IsError || diagnosticCode(t, missing) != "CONFIG_INVALID" {
		t.Fatalf("missing config: %+v", missing)
	}
}

func TestCheckSourceTool(t *testing.T) {
	c := startToolClient(t, settlementWorkspace(t))
	ok := c.callTool("check_source", map[string]any{"source": "left", "file": "inputs/left.csv"})
	if ok.IsError || ok.Structured["ok"] != true {
		t.Fatalf("check_source ok case: %+v", ok)
	}
	if report, _ := ok.Structured["report"].(string); !strings.Contains(report, "matches file") {
		t.Fatalf("report = %q", report)
	}
	bad := c.callTool("check_source", map[string]any{"source": "nope", "file": "inputs/left.csv"})
	if !bad.IsError {
		t.Fatalf("expected isError for unknown source: %+v", bad)
	}
}

func TestReconcileTool(t *testing.T) {
	dir := settlementWorkspace(t)
	c := startToolClient(t, dir)

	out := c.callTool("reconcile", map[string]any{"out": "result.json", "pair": "left_vs_right"})
	if out.IsError {
		t.Fatalf("reconcile failed: %+v", out)
	}
	assertJSONText(t, out)
	if out.Structured["status"] != "completed" || out.Structured["exit_code"] != float64(0) {
		t.Fatalf("status = %v exit_code = %v", out.Structured["status"], out.Structured["exit_code"])
	}
	// The result is in the file, never inline.
	if _, inline := out.Structured["matched"]; inline {
		t.Fatal("result events must not be returned inline")
	}
	summary, _ := out.Structured["summary"].(map[string]any)
	if mustFloat(t, summary, "matched") != 1 || mustFloat(t, summary, "amount_diff_count") != 1 {
		t.Fatalf("summary = %v", summary)
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.json")) // #nosec G304 -- test temp dir.
	if err != nil {
		t.Fatalf("result file not written: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil || doc["schema"] != "reconify.engine.result.v1" {
		t.Fatalf("result file is not a v1 result document: %v", err)
	}
	if out.Structured["out_path"] != filepath.Join(dir, "result.json") {
		t.Fatalf("out_path = %v", out.Structured["out_path"])
	}
}

func TestReconcileToolFormatsAndModes(t *testing.T) {
	dir := settlementWorkspace(t)
	c := startToolClient(t, dir)
	for _, format := range []string{"json", "ndjson", "json-stream"} {
		t.Run(format, func(t *testing.T) {
			out := c.callTool("reconcile", map[string]any{
				"out": "result-" + format, "format": format, "result_mode": "all", "pair": "left_vs_right",
			})
			if out.IsError {
				t.Fatalf("reconcile failed: %+v", out)
			}
			summary, _ := out.Structured["summary"].(map[string]any)
			if mustFloat(t, summary, "matched") != 1 || mustFloat(t, summary, "amount_diff_count") != 1 {
				t.Fatalf("summary = %v", summary)
			}
			wantFormat := format
			if format == "json-stream" {
				wantFormat = "json"
			}
			if out.Structured["format"] != wantFormat {
				t.Fatalf("format = %v, want %s", out.Structured["format"], wantFormat)
			}
			// get_summary reads every format the same way.
			sum := c.callTool("get_summary", map[string]any{"result": "result-" + format})
			if sum.IsError {
				t.Fatalf("get_summary failed: %+v", sum)
			}
			got, _ := sum.Structured["summary"].(map[string]any)
			if mustFloat(t, got, "total_left") != 2 {
				t.Fatalf("summary = %v", got)
			}
		})
	}
}

func TestReconcileFindingsExitCodesAreNotErrors(t *testing.T) {
	dir := settlementWorkspace(t)
	c := startToolClient(t, dir)
	out := c.callTool("reconcile", map[string]any{"out": "result.ndjson", "pair": "left_vs_right", "format": "ndjson", "fail_if_exceptions": true})
	if out.IsError {
		t.Fatalf("exit code 4 must not be reported as an error: %+v", out)
	}
	if out.Structured["exit_code"] != float64(4) || out.Structured["status"] != "completed" {
		t.Fatalf("structured = %v", out.Structured)
	}
	diag, _ := out.Structured["diagnostic"].(map[string]any)
	inner, _ := diag["diagnostic"].(map[string]any)
	if inner["code"] != "EXCEPTIONS_FOUND" {
		t.Fatalf("diagnostic = %v", diag)
	}
	if _, err := os.Stat(filepath.Join(dir, "result.ndjson")); err != nil {
		t.Fatalf("result file missing: %v", err)
	}
}

func TestReconcileToolErrors(t *testing.T) {
	dir := settlementWorkspace(t)
	c := startToolClient(t, dir)
	out := c.callTool("reconcile", map[string]any{"out": "r.json", "pair": "no_such_pair"})
	if !out.IsError {
		t.Fatalf("expected isError for an unknown pair: %+v", out)
	}
	if code := diagnosticCode(t, out); code != "CONFIG_INVALID" {
		t.Fatalf("diagnostic code = %q", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "r.json")); err == nil {
		t.Fatal("no result file should be written on failure")
	}
}

func TestCwdArgument(t *testing.T) {
	parent := t.TempDir()
	ws := filepath.Join(parent, "nested", "ws")
	src := settlementWorkspace(t)
	copyFile(t, filepath.Join(src, "reconify.yaml"), filepath.Join(ws, "reconify.yaml"))
	copyFile(t, filepath.Join(src, "inputs", "left.csv"), filepath.Join(ws, "inputs", "left.csv"))
	copyFile(t, filepath.Join(src, "inputs", "right.csv"), filepath.Join(ws, "inputs", "right.csv"))
	c := startToolClient(t, parent)

	// Without cwd there is no config in the server working directory.
	if out := c.callTool("validate_config", nil); !out.IsError {
		t.Fatalf("expected isError without cwd: %+v", out)
	}
	if out := c.callTool("validate_config", map[string]any{"cwd": "nested/ws"}); out.IsError {
		t.Fatalf("validate_config with relative cwd failed: %+v", out)
	}
	if out := c.callTool("validate_config", map[string]any{"cwd": ws}); out.IsError {
		t.Fatalf("validate_config with absolute cwd failed: %+v", out)
	}
	if out := c.callTool("validate_config", map[string]any{"cwd": "missing-dir"}); !out.IsError || diagnosticCode(t, out) != "INPUT_UNREADABLE" {
		t.Fatalf("missing cwd: %+v", out)
	}

	// Results written under cwd are read back relative to the same cwd.
	if out := c.callTool("reconcile", map[string]any{"cwd": "nested/ws", "out": "result.json", "pair": "left_vs_right"}); out.IsError {
		t.Fatalf("reconcile with cwd failed: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(ws, "result.json")); err != nil {
		t.Fatalf("result not written under cwd: %v", err)
	}
	if out := c.callTool("get_summary", map[string]any{"cwd": "nested/ws", "result": "result.json"}); out.IsError {
		t.Fatalf("get_summary with cwd failed: %+v", out)
	}
}

func TestGetSummaryErrors(t *testing.T) {
	dir := settlementWorkspace(t)
	c := startToolClient(t, dir)
	missing := c.callTool("get_summary", map[string]any{"result": "nope.json"})
	if !missing.IsError || diagnosticCode(t, missing) != "INPUT_UNREADABLE" {
		t.Fatalf("missing result: %+v", missing)
	}
	writeFile(t, filepath.Join(dir, "junk.json"), "this is not json")
	junk := c.callTool("get_summary", map[string]any{"result": "junk.json"})
	if !junk.IsError || diagnosticCode(t, junk) != "INPUT_MISMATCH" {
		t.Fatalf("junk result: %+v", junk)
	}
	writeFile(t, filepath.Join(dir, "empty.json"), "")
	empty := c.callTool("get_summary", map[string]any{"result": "empty.json"})
	if !empty.IsError {
		t.Fatalf("empty result: %+v", empty)
	}
}

// exceptionWorkspace builds a workspace whose reconciliation yields 3 amount
// diffs, 4 unmatched-left rows and 2 unmatched-right rows (9 exceptions).
func exceptionWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyFile(t, filepath.Join(moduleRoot, "evals", "003-settlement-fee", "reference", "reconify.yaml"), filepath.Join(dir, "reconify.yaml"))
	var left, right strings.Builder
	left.WriteString("date,amount,currency,reference,description\n")
	right.WriteString("date,amount,currency,reference,description\n")
	for i := 1; i <= 5; i++ { // 5 clean matches
		fmt.Fprintf(&left, "2024-01-01,10.00,USD,OK-%d,Sale\n", i)
		fmt.Fprintf(&right, "2024-01-01,10.00,USD,OK-%d,Sale\n", i)
	}
	for i := 1; i <= 3; i++ { // 3 amount diffs well beyond the 2.00 tolerance
		fmt.Fprintf(&left, "2024-01-02,100.00,USD,DIFF-%d,Sale\n", i)
		fmt.Fprintf(&right, "2024-01-02,%d.00,USD,DIFF-%d,Sale\n", 90-i, i)
	}
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&left, "2024-01-03,5.00,USD,LEFTONLY-%d,Sale\n", i)
	}
	for i := 1; i <= 2; i++ {
		fmt.Fprintf(&right, "2024-01-03,7.00,USD,RIGHTONLY-%d,Sale\n", i)
	}
	writeFile(t, filepath.Join(dir, "inputs", "left.csv"), left.String())
	writeFile(t, filepath.Join(dir, "inputs", "right.csv"), right.String())
	return dir
}

func TestListExceptionsPagination(t *testing.T) {
	dir := exceptionWorkspace(t)
	c := startToolClient(t, dir)
	for _, format := range []string{"json", "ndjson", "json-stream"} {
		t.Run(format, func(t *testing.T) {
			resultName := "result-" + format
			run := c.callTool("reconcile", map[string]any{"out": resultName, "pair": "left_vs_right", "format": format, "result_mode": "all"})
			if run.IsError {
				t.Fatalf("reconcile failed: %+v", run)
			}

			var all []string
			offset := 0
			pages := 0
			for {
				page := c.callTool("list_exceptions", map[string]any{"result": resultName, "limit": 4, "offset": offset})
				if page.IsError {
					t.Fatalf("list_exceptions failed: %+v", page)
				}
				assertJSONText(t, page)
				if mustFloat(t, page.Structured, "total") != 9 {
					t.Fatalf("total = %v, want 9", page.Structured["total"])
				}
				events, _ := page.Structured["exceptions"].([]any)
				for _, e := range events {
					ev, _ := e.(map[string]any)
					typ, _ := ev["type"].(string)
					if typ == "match" {
						t.Fatal("clean matches must never be listed")
					}
					if ev["data"] == nil {
						t.Fatalf("event has no data: %v", ev)
					}
					all = append(all, typ)
				}
				pages++
				next, more := page.Structured["next_offset"].(float64)
				if !more {
					break
				}
				if int(next) != offset+len(events) {
					t.Fatalf("next_offset = %v, want %d", next, offset+len(events))
				}
				offset = int(next)
				if pages > 10 {
					t.Fatal("pagination did not terminate")
				}
			}
			if pages != 3 || len(all) != 9 {
				t.Fatalf("pages = %d events = %d (%v), want 3 pages and 9 events", pages, len(all), all)
			}
			counts := map[string]int{}
			for _, typ := range all {
				counts[typ]++
			}
			if counts["amount_diff"] != 3 || counts["unmatched_left"] != 4 || counts["unmatched_right"] != 2 {
				t.Fatalf("counts = %v", counts)
			}

			filtered := c.callTool("list_exceptions", map[string]any{"result": resultName, "types": []any{"amount_diff"}, "limit": 2})
			if filtered.IsError || mustFloat(t, filtered.Structured, "total") != 3 || mustFloat(t, filtered.Structured, "returned") != 2 {
				t.Fatalf("filtered = %+v", filtered.Structured)
			}
			if filtered.Structured["next_offset"] != float64(2) {
				t.Fatalf("filtered next_offset = %v", filtered.Structured["next_offset"])
			}
			byType, _ := filtered.Structured["counts_by_type"].(map[string]any)
			if byType["unmatched_left"] != float64(4) {
				t.Fatalf("counts_by_type must describe the whole file: %v", byType)
			}

			defaults := c.callTool("list_exceptions", map[string]any{"result": resultName})
			if mustFloat(t, defaults.Structured, "limit") != 20 || mustFloat(t, defaults.Structured, "returned") != 9 {
				t.Fatalf("defaults = %+v", defaults.Structured)
			}
			if _, more := defaults.Structured["next_offset"]; more {
				t.Fatal("next_offset must be absent on the last page")
			}

			beyond := c.callTool("list_exceptions", map[string]any{"result": resultName, "offset": 50})
			if beyond.IsError || mustFloat(t, beyond.Structured, "returned") != 0 {
				t.Fatalf("offset past the end: %+v", beyond.Structured)
			}
		})
	}
}

func TestListExceptionsSummaryOnlyNote(t *testing.T) {
	dir := exceptionWorkspace(t)
	c := startToolClient(t, dir)
	if run := c.callTool("reconcile", map[string]any{"out": "s.json", "pair": "left_vs_right", "result_mode": "summary_only"}); run.IsError {
		t.Fatalf("reconcile failed: %+v", run)
	}
	out := c.callTool("list_exceptions", map[string]any{"result": "s.json"})
	if out.IsError || mustFloat(t, out.Structured, "total") != 0 {
		t.Fatalf("list_exceptions = %+v", out)
	}
	if note, _ := out.Structured["note"].(string); !strings.Contains(note, "summary_only") {
		t.Fatalf("note = %q", note)
	}
}

func TestExplainResultTool(t *testing.T) {
	dir := exceptionWorkspace(t)
	c := startToolClient(t, dir)
	if run := c.callTool("reconcile", map[string]any{"out": "result.json", "pair": "left_vs_right"}); run.IsError {
		t.Fatalf("reconcile failed: %+v", run)
	}
	out := c.callTool("explain_result", map[string]any{"result": "result.json", "top": 2})
	if out.IsError {
		t.Fatalf("explain_result failed: %+v", out)
	}
	assertJSONText(t, out)
	if out.Structured["schema"] != "reconify.engine.explanation.v1" {
		t.Fatalf("schema = %v", out.Structured["schema"])
	}
	top, _ := out.Structured["top_exceptions"].([]any)
	if len(top) != 2 || out.Structured["truncated"] != true || mustFloat(t, out.Structured, "exceptions_total") != 9 {
		t.Fatalf("explanation = %v", out.Structured)
	}
	missing := c.callTool("explain_result", map[string]any{"result": "nope.json"})
	if !missing.IsError || diagnosticCode(t, missing) != "INPUT_UNREADABLE" {
		t.Fatalf("missing result: %+v", missing)
	}
}

func largeCSV(t *testing.T, path string, amountShift int) {
	t.Helper()
	var data strings.Builder
	data.WriteString("date,amount,reference\n")
	for i := 0; i < 120; i++ {
		amount := i + 1
		if i == 7 {
			amount += amountShift
		}
		fmt.Fprintf(&data, "2024-01-%02d,%d.00,REF-%03d\n", (i%28)+1, amount, i)
	}
	writeFile(t, path, data.String())
}

func TestInferConfigTool(t *testing.T) {
	dir := t.TempDir()
	largeCSV(t, filepath.Join(dir, "a.csv"), 0)
	largeCSV(t, filepath.Join(dir, "b.csv"), 0)
	c := startToolClient(t, dir)

	out := c.callTool("infer_config", map[string]any{"left": "a.csv", "right": "b.csv", "out": "inferred.yaml"})
	if out.IsError {
		t.Fatalf("infer_config failed: %+v", out)
	}
	if out.Structured["schema"] != "reconify.engine.config-proposal.v1" || out.Structured["status"] != "ready" {
		t.Fatalf("proposal = %v", out.Structured)
	}
	if _, err := os.Stat(filepath.Join(dir, "inferred.yaml")); err != nil {
		t.Fatalf("inferred.yaml not written: %v", err)
	}
	if v := c.callTool("validate_config", map[string]any{"config": "inferred.yaml"}); v.IsError {
		t.Fatalf("inferred config does not validate: %+v", v)
	}

	// Proposal-only (no out) never writes a file.
	proposal := c.callTool("infer_config", map[string]any{"left": "a.csv", "right": "b.csv"})
	if proposal.IsError || proposal.Structured["status"] != "ready" {
		t.Fatalf("proposal-only: %+v", proposal)
	}
}

func TestInferConfigAmbiguousIsError(t *testing.T) {
	dir := settlementWorkspace(t) // 2-row inputs cannot pass the 100-row gate
	c := startToolClient(t, dir)
	out := c.callTool("infer_config", map[string]any{"left": "inputs/left.csv", "right": "inputs/right.csv", "out": "x.yaml"})
	if !out.IsError || diagnosticCode(t, out) != "INFERENCE_AMBIGUOUS" {
		t.Fatalf("expected INFERENCE_AMBIGUOUS: %+v", out)
	}
	if len(out.Text) < 2 {
		t.Fatalf("the proposal on stdout should be preserved as a second content block: %v", out.Text)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.yaml")); err == nil {
		t.Fatal("no config file may be written for an ambiguous proposal")
	}
}

func TestReconcileAutoTool(t *testing.T) {
	dir := t.TempDir()
	largeCSV(t, filepath.Join(dir, "a.csv"), 0)
	largeCSV(t, filepath.Join(dir, "b.csv"), 50)
	c := startToolClient(t, dir)

	out := c.callTool("reconcile_auto", map[string]any{"left": "a.csv", "right": "b.csv", "out": "auto.ndjson"})
	if out.IsError {
		t.Fatalf("reconcile_auto failed: %+v", out)
	}
	if out.Structured["format"] != "ndjson" {
		t.Fatalf("format = %v", out.Structured["format"])
	}
	summary, _ := out.Structured["summary"].(map[string]any)
	if mustFloat(t, summary, "total_left") != 120 || mustFloat(t, summary, "amount_diff_count") != 1 {
		t.Fatalf("summary = %v", summary)
	}
	if yaml, _ := out.Structured["inferred_config"].(string); !strings.Contains(yaml, "sources:") {
		t.Fatalf("inferred_config = %q", yaml)
	}
	list := c.callTool("list_exceptions", map[string]any{"result": "auto.ndjson"})
	if list.IsError || mustFloat(t, list.Structured, "total") != 1 {
		t.Fatalf("list_exceptions = %+v", list)
	}
}

func TestReconcileAutoAmbiguousIsError(t *testing.T) {
	dir := settlementWorkspace(t)
	c := startToolClient(t, dir)
	out := c.callTool("reconcile_auto", map[string]any{"left": "inputs/left.csv", "right": "inputs/right.csv", "out": "auto.ndjson"})
	if !out.IsError || diagnosticCode(t, out) != "INFERENCE_AMBIGUOUS" {
		t.Fatalf("expected INFERENCE_AMBIGUOUS: %+v", out)
	}
	details, _ := out.Structured["diagnostic"].(map[string]any)["details"].(map[string]any)
	if reasons, _ := details["reasons"].([]any); len(reasons) == 0 {
		t.Fatalf("diagnostic should carry reasons: %v", details)
	}
	if _, err := os.Stat(filepath.Join(dir, "auto.ndjson")); err == nil {
		t.Fatal("no result may be written when inference is ambiguous")
	}
}

func TestVerifyWorkspaceTool(t *testing.T) {
	dir := settlementWorkspace(t)
	c := startToolClient(t, dir)
	out := c.callTool("verify_workspace", nil)
	if out.IsError {
		// The verify command may not exist in this build; the failure must say so clearly.
		if !strings.Contains(strings.Join(out.Text, " "), "does not include the verify command") {
			t.Fatalf("unexpected verify_workspace error: %+v", out)
		}
		return
	}
	if out.Structured["schema"] != "reconify.engine.verification.v1" {
		t.Fatalf("verification = %v", out.Structured)
	}
}

func TestVerifyWorkspaceUnknownSubcommandMessage(t *testing.T) {
	// A binary that lacks `verify` must produce a clear, actionable error.
	script := filepath.Join(t.TempDir(), "old-reconify")
	body := "#!/bin/sh\necho '{\"error\":\"unknown command \\\"verify\\\" for \\\"reconify\\\"\"}' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil { // #nosec G306 -- test executable.
		t.Fatal(err)
	}
	c := startClient(t, Options{Executable: script, WorkDir: t.TempDir()})
	out := c.callTool("verify_workspace", nil)
	if !out.IsError || !strings.Contains(strings.Join(out.Text, " "), "does not include the verify command") {
		t.Fatalf("verify_workspace = %+v", out)
	}
}

func TestVerifyWorkspaceReportsFailedChecksWithoutError(t *testing.T) {
	// Exit code 5 means the checks ran and at least one failed: not a tool error.
	script := filepath.Join(t.TempDir(), "verify-reconify")
	body := "#!/bin/sh\necho '{\"schema\":\"reconify.engine.verification.v1\",\"ok\":false,\"checks\":[]}'\nexit 5\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil { // #nosec G306 -- test executable.
		t.Fatal(err)
	}
	c := startClient(t, Options{Executable: script, WorkDir: t.TempDir()})
	out := c.callTool("verify_workspace", map[string]any{"pair": "p", "result": "r.json"})
	if out.IsError || out.Structured["ok"] != false {
		t.Fatalf("verify_workspace = %+v", out)
	}
}

func TestToolArgumentsAreForwardedExplicitly(t *testing.T) {
	// A stand-in executable records its argv so the exact CLI mapping is pinned.
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	script := filepath.Join(dir, "recorder")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\necho '{}'\n", argvFile)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil { // #nosec G306 -- test executable.
		t.Fatal(err)
	}
	c := startClient(t, Options{Executable: script, WorkDir: dir})

	tests := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"inspect", "inspect_file", map[string]any{"path": "-weird.csv", "full": true, "sample_values": 0, "sheet": "S1"},
			"--agent inspect --format=json --full --sample-values=0 --sheet=S1 -- -weird.csv"},
		{"explain", "explain_result", map[string]any{"result": "r.json", "top": 3},
			"--agent explain --top=3 -- r.json"},
		{"infer", "infer_config", map[string]any{"left": "a.csv", "right": "b.csv", "out": "c.yaml"},
			"--agent config infer --left=a.csv --right=b.csv --out=c.yaml"},
		{"reconcile", "reconcile", map[string]any{"config": "c.yaml", "pair": "p", "out": "r.json", "result_mode": "exceptions_only", "fail_if_unmatched": true},
			"--agent reconcile --config=c.yaml --out=r.json --format=json --pair=p --result-mode=exceptions_only --deterministic --fail-if-unmatched"},
		{"reconcile ndjson is not deterministic-flagged", "reconcile", map[string]any{"out": "r.ndjson", "format": "ndjson"},
			"--agent reconcile --out=r.ndjson --format=ndjson"},
		{"auto", "reconcile_auto", map[string]any{"left": "-a.csv", "right": "b.csv", "out": "r.ndjson"},
			"--agent reconcile --auto --out=r.ndjson -- -a.csv b.csv"},
		{"check-source", "check_source", map[string]any{"config": "c.yaml", "source": "left", "file": "l.csv", "rows": 0},
			"--agent config check-source --config=c.yaml --source=left --file=l.csv --rows=0"},
		{"validate", "validate_config", map[string]any{"config": "c.yaml"},
			"--agent config validate --config=c.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Remove(argvFile)
			c.callTool(tt.tool, tt.args)
			data, err := os.ReadFile(argvFile) // #nosec G304 -- test temp dir.
			if err != nil {
				t.Fatalf("recorder was not run: %v", err)
			}
			got := strings.Join(strings.Fields(string(data)), " ")
			if got != tt.want {
				t.Fatalf("argv = %q\nwant   %q", got, tt.want)
			}
		})
	}
}
