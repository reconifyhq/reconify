package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Exit codes the CLI uses for "completed with findings".
const (
	exitUnmatched  = 3
	exitExceptions = 4
	exitVerifyFail = 5
)

func runCapabilities(ctx context.Context, s *Server, args map[string]any) toolResult {
	run, errRes := s.invoke(ctx, args, []string{"capabilities"})
	if errRes != nil {
		return *errRes
	}
	return stdoutJSON(run)
}

func runInspectFile(ctx context.Context, s *Server, args map[string]any) toolResult {
	argv := []string{"inspect", "--format=json"}
	if argBool(args, "full", false) {
		argv = append(argv, "--full")
	}
	if _, ok := args["sample_values"]; ok {
		argv = append(argv, "--sample-values="+strconv.Itoa(argInt(args, "sample_values", 0)))
	}
	if sheet := argStr(args, "sheet"); sheet != "" {
		argv = append(argv, "--sheet="+sheet)
	}
	argv = append(argv, "--", argStr(args, "path"))
	run, errRes := s.invoke(ctx, args, argv)
	if errRes != nil {
		return *errRes
	}
	return stdoutJSON(run)
}

func runInferConfig(ctx context.Context, s *Server, args map[string]any) toolResult {
	argv := []string{"config", "infer", "--left=" + argStr(args, "left"), "--right=" + argStr(args, "right")}
	if out := argStr(args, "out"); out != "" {
		if res := rejectStdout(out); res != nil {
			return *res
		}
		argv = append(argv, "--out="+out)
	}
	run, errRes := s.invoke(ctx, args, argv)
	if errRes != nil {
		return *errRes
	}
	return stdoutJSON(run)
}

func configArgs(args map[string]any) []string {
	if config := argStr(args, "config"); config != "" {
		return []string{"--config=" + config}
	}
	return nil
}

func runValidateConfig(ctx context.Context, s *Server, args map[string]any) toolResult {
	argv := append([]string{"config", "validate"}, configArgs(args)...)
	run, errRes := s.invoke(ctx, args, argv)
	if errRes != nil {
		return *errRes
	}
	result := map[string]any{"valid": true}
	if config := argStr(args, "config"); config != "" {
		result["config"] = config
	}
	if message := strings.TrimSpace(string(run.stderr)); message != "" {
		result["message"] = message
	}
	return jsonResult(result)
}

func runCheckSource(ctx context.Context, s *Server, args map[string]any) toolResult {
	argv := append([]string{"config", "check-source"}, configArgs(args)...)
	argv = append(argv, "--source="+argStr(args, "source"), "--file="+argStr(args, "file"))
	if _, ok := args["rows"]; ok {
		argv = append(argv, "--rows="+strconv.Itoa(argInt(args, "rows", 10)))
	}
	run, errRes := s.invoke(ctx, args, argv)
	if errRes != nil {
		return *errRes
	}
	// check-source writes its report to stdout or stderr depending on the profile.
	report := strings.TrimSpace(string(run.stdout))
	if report == "" {
		report = strings.TrimSpace(string(run.stderr))
	}
	return jsonResult(map[string]any{
		"ok":     true,
		"source": argStr(args, "source"),
		"file":   argStr(args, "file"),
		"report": report,
	})
}

func runReconcile(ctx context.Context, s *Server, args map[string]any) toolResult {
	out := argStr(args, "out")
	if res := rejectStdout(out); res != nil {
		return *res
	}
	format := argStr(args, "format")
	if format == "" {
		format = "json"
	}
	argv := append([]string{"reconcile"}, configArgs(args)...)
	argv = append(argv, "--out="+out, "--format="+format)
	if pair := argStr(args, "pair"); pair != "" {
		argv = append(argv, "--pair="+pair)
	}
	if mode := argStr(args, "result_mode"); mode != "" {
		argv = append(argv, "--result-mode="+mode)
	}
	if format == "json" && argBool(args, "deterministic", true) {
		argv = append(argv, "--deterministic")
	}
	if argBool(args, "fail_if_exceptions", false) {
		argv = append(argv, "--fail-if-exceptions")
	}
	if argBool(args, "fail_if_unmatched", false) {
		argv = append(argv, "--fail-if-unmatched")
	}
	return s.reconcileResult(ctx, args, argv, out, format)
}

func runReconcileAuto(ctx context.Context, s *Server, args map[string]any) toolResult {
	out := argStr(args, "out")
	if res := rejectStdout(out); res != nil {
		return *res
	}
	argv := []string{"reconcile", "--auto", "--out=" + out, "--", argStr(args, "left"), argStr(args, "right")}
	return s.reconcileResult(ctx, args, argv, out, "")
}

// reconcileResult runs a reconcile invocation and reports status plus summary
// counters read back from the result file. Exit codes 3 and 4 mean the run
// completed with findings and are not errors.
func (s *Server) reconcileResult(ctx context.Context, args map[string]any, argv []string, out, format string) toolResult {
	dir, errRes := s.workDirFor(args)
	if errRes != nil {
		return *errRes
	}
	run, errRes := s.invoke(ctx, args, argv, exitUnmatched, exitExceptions)
	if errRes != nil {
		return *errRes
	}
	outPath := resolvePath(dir, out)
	result := map[string]any{
		"status":    "completed",
		"exit_code": run.exitCode,
		"out":       out,
		"out_path":  outPath,
	}
	if run.exitCode != 0 {
		if _, diag, ok := lastJSONObjectLine(strings.TrimSpace(string(run.stderr))); ok {
			result["diagnostic"] = diag
		}
		result["note"] = "The run completed; this exit code reports findings (see summary), it is not a failure."
	}
	if info, err := os.Stat(outPath); err == nil {
		result["bytes"] = info.Size()
	}
	if ri, err := readResultInfo(outPath); err != nil {
		result["summary_error"] = err.Error()
		result["format"] = format
	} else {
		result["format"] = ri.format
		if ri.summary != nil {
			result["summary"] = ri.summary
		}
		if len(ri.bySource) > 0 {
			result["by_source"] = ri.bySource
		}
		if ri.inferredConfig != "" {
			result["inferred_config"] = ri.inferredConfig
		}
	}
	result["next_steps"] = []string{
		"list_exceptions {result: " + strconv.Quote(out) + "} to page through exception events",
		"explain_result {result: " + strconv.Quote(out) + "} for the deterministic explanation",
		"verify_workspace to check the deliverables",
	}
	return jsonResult(result)
}

// resultInfo is the lightweight digest of a result file.
type resultInfo struct {
	format         string
	summary        json.RawMessage
	bySource       map[string]json.RawMessage
	inferredConfig string
	resultMode     string
}

func readResultInfo(path string) (*resultInfo, error) {
	ri := &resultInfo{bySource: map[string]json.RawMessage{}}
	format, err := scanResult(path, func(typ string, data json.RawMessage) error {
		switch typ {
		case "summary":
			ri.summary = data
			var sum struct {
				ResultMode string `json:"result_mode"`
			}
			_ = json.Unmarshal(data, &sum)
			ri.resultMode = sum.ResultMode
		case "by_source":
			var m map[string]json.RawMessage
			if json.Unmarshal(data, &m) == nil {
				for k, v := range m {
					ri.bySource[k] = v
				}
			}
		case "source_summary":
			var ss struct {
				Source  string          `json:"source"`
				Summary json.RawMessage `json:"summary"`
			}
			if json.Unmarshal(data, &ss) == nil && ss.Source != "" {
				ri.bySource[ss.Source] = ss.Summary
			}
		case "run_info":
			var r struct {
				InferredConfig string `json:"inferred_config"`
			}
			if json.Unmarshal(data, &r) == nil {
				ri.inferredConfig = r.InferredConfig
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	ri.format = format
	return ri, nil
}

// openResultInfo resolves and reads a result path argument for the read tools.
func (s *Server) openResultInfo(args map[string]any) (*resultInfo, string, *toolResult) {
	dir, errRes := s.workDirFor(args)
	if errRes != nil {
		return nil, "", errRes
	}
	given := argStr(args, "result")
	path := resolvePath(dir, given)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		res := errorResult("INPUT_UNREADABLE", fmt.Sprintf("result file %q not found", given))
		return nil, "", &res
	}
	ri, err := readResultInfo(path)
	if err != nil {
		res := errorResult("INPUT_MISMATCH", fmt.Sprintf("read result %q: %v", given, err))
		return nil, "", &res
	}
	return ri, path, nil
}

func runGetSummary(_ context.Context, s *Server, args map[string]any) toolResult {
	ri, _, errRes := s.openResultInfo(args)
	if errRes != nil {
		return *errRes
	}
	if ri.summary == nil {
		return errorResult("INPUT_MISMATCH", fmt.Sprintf("result %q has no summary; it may be truncated or not a reconify result", argStr(args, "result")))
	}
	out := map[string]any{
		"result":  argStr(args, "result"),
		"format":  ri.format,
		"summary": ri.summary,
	}
	if len(ri.bySource) > 0 {
		out["by_source"] = ri.bySource
	}
	return jsonResult(out)
}

func runListExceptions(_ context.Context, s *Server, args map[string]any) toolResult {
	dir, errRes := s.workDirFor(args)
	if errRes != nil {
		return *errRes
	}
	given := argStr(args, "result")
	path := resolvePath(dir, given)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return errorResult("INPUT_UNREADABLE", fmt.Sprintf("result file %q not found", given))
	}
	limit := argInt(args, "limit", 20)
	offset := argInt(args, "offset", 0)
	wanted := map[string]bool{}
	if raw, ok := args["types"].([]any); ok {
		for _, item := range raw {
			if name, ok := item.(string); ok {
				wanted[name] = true
			}
		}
	}

	type event struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	events := []event{}
	counts := map[string]int{}
	total := 0
	var resultMode string
	format, err := scanResult(path, func(typ string, data json.RawMessage) error {
		if typ == "summary" {
			var sum struct {
				ResultMode string `json:"result_mode"`
			}
			_ = json.Unmarshal(data, &sum)
			resultMode = sum.ResultMode
			return nil
		}
		if !isExceptionType(typ) {
			return nil
		}
		counts[typ]++
		if len(wanted) > 0 && !wanted[typ] {
			return nil
		}
		if total >= offset && len(events) < limit {
			events = append(events, event{Type: typ, Data: data})
		}
		total++
		return nil
	})
	if err != nil {
		return errorResult("INPUT_MISMATCH", fmt.Sprintf("read result %q: %v", given, err))
	}
	out := map[string]any{
		"result":         given,
		"format":         format,
		"total":          total,
		"counts_by_type": counts,
		"offset":         offset,
		"limit":          limit,
		"returned":       len(events),
		"exceptions":     events,
	}
	if next := offset + len(events); next < total {
		out["next_offset"] = next
	}
	if len(counts) == 0 && resultMode == "summary_only" {
		out["note"] = "This result was written with result_mode summary_only, so it holds no events. Rerun reconcile with result_mode exceptions_only or all."
	}
	return jsonResult(out)
}

func runExplainResult(ctx context.Context, s *Server, args map[string]any) toolResult {
	argv := []string{"explain"}
	if _, ok := args["top"]; ok {
		argv = append(argv, "--top="+strconv.Itoa(argInt(args, "top", 10)))
	}
	argv = append(argv, "--", argStr(args, "result"))
	run, errRes := s.invoke(ctx, args, argv)
	if errRes != nil {
		return *errRes
	}
	return stdoutJSON(run)
}

func runVerifyWorkspace(ctx context.Context, s *Server, args map[string]any) toolResult {
	dir, errRes := s.workDirFor(args)
	if errRes != nil {
		return *errRes
	}
	argv := append([]string{"verify"}, configArgs(args)...)
	for _, key := range []string{"pair", "result", "explanation"} {
		if v := argStr(args, key); v != "" {
			argv = append(argv, "--"+key+"="+v)
		}
	}
	run, err := s.runEngine(ctx, dir, argv)
	if err != nil {
		return errorResult("EXECUTION_FAILED", err.Error())
	}
	if isUnknownVerify(string(run.stderr)) {
		return errorResult("EXECUTION_FAILED", "this reconify binary does not include the verify command; upgrade reconify to a release that supports `reconify verify`, "+
			"or check the deliverables manually with validate_config, reconcile, and explain_result")
	}
	if run.exitCode == 0 || run.exitCode == exitVerifyFail {
		return stdoutJSON(run)
	}
	return failureResult(run)
}

// isUnknownVerify recognises cobra's unknown-command failure for verify, in
// plain text or embedded in a JSON diagnostic (where quotes are escaped).
func isUnknownVerify(stderr string) bool {
	return strings.Contains(stderr, "unknown command \"verify\"") || strings.Contains(stderr, "unknown command \\\"verify\\\"")
}
