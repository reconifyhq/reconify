package evals

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeReconify stands in for the Engine: it appends a trace line when the trace
// variable is set (as the real binary does), exits 0, and copies $FAKE_RESULT to --out.
const fakeReconify = `#!/bin/sh
if [ -n "$RECONIFY_TRACE_FILE" ]; then
  args=""
  for a in "$@"; do args="$args\"$a\","; done
  printf '{"argv":[%s],"exit_code":0,"duration_ms":1}\n' "${args%,}" >> "$RECONIFY_TRACE_FILE"
fi
out=""; prev=""
for a in "$@"; do
  if [ "$prev" = "--out" ]; then out="$a"; fi
  prev="$a"
done
if [ -n "$out" ]; then cp "$FAKE_RESULT" "$out"; fi
exit 0
`

// fakeClaude emulates a well-behaved headless agent session in its workspace.
const fakeClaude = `#!/bin/sh
reconify capabilities >/dev/null
printf 'version: 1\n' > reconify.yaml
reconify config validate --config reconify.yaml >/dev/null
reconify reconcile --config reconify.yaml --pair left_vs_right --out result.json >/dev/null
printf '{}' > explanation.json
reconify explain result.json >/dev/null
printf '%s' "$FAKE_CLAUDE_JSON"
`

const e2eResult = `{"schema":"reconify.engine.result.v1","summary":{"matched":1,"unmatched_left":0,"unmatched_right":0,"amount_diff_count":1,"timing_diff_count":0,"duplicate_count":0},"matched":[],"unmatched_left":[],"unmatched_right":[],"amount_diff":[],"timing_diff":[],"duplicates":[]}`

func e2eCorpus(t *testing.T) (root, bin string) {
	t.Helper()
	root, bin = t.TempDir(), t.TempDir()
	dir := filepath.Join(root, "010-e2e")
	for _, sub := range []string{"expected", "reference"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"scenario.json":             `{"schema":"reconify.engine.eval-scenario.v2","id":"010-e2e","prompt":"p","inputs":[],"reference_config":"reference/reconify.yaml","expected_result":"expected/result.json","expected_explanation":"expected/explanation.json","pair":"left_vs_right","assertions":{"matched":1,"unmatched_left":0,"unmatched_right":0,"amount_diff_count":1,"timing_diff_count":0,"duplicate_count":0,"grouped_matched_count":0,"many_to_many_matched_count":0,"ambiguous_group_count":0},"counter_examples":[],"tags":["core"],"decision_keywords":["which currency"]}`,
		"expected/result.json":      e2eResult,
		"expected/explanation.json": `{}`,
		"reference/reconify.yaml":   baseConfig().yaml(),
	}
	for name, content := range map[string]string{"reconify": fakeReconify, "claude": fakeClaude} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o700); err != nil { // #nosec G306 -- test executables.
			t.Fatal(err)
		}
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil { // #nosec G306 -- test executables.
			t.Fatal(err)
		}
	}
	return root, bin
}

func TestRunTrialWithFakeAgentRecordsTraceUsageGradesAndNoFailure(t *testing.T) {
	root, fakeBin := e2eCorpus(t)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_RESULT", filepath.Join(root, "010-e2e", "expected", "result.json"))
	t.Setenv("FAKE_CLAUDE_JSON", `{"type":"result","result":"Done: matched: 1 and amount differences: 1","num_turns":4,"total_cost_usd":0.25,"usage":{"input_tokens":10,"cache_read_input_tokens":5,"output_tokens":7}}`)
	t.Setenv(traceEnv, "/should/not/leak.jsonl")
	scenarios, err := loadScenarios(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := t.TempDir()
	options := Options{CorpusDir: root, SkillsDir: t.TempDir(), ReconifyPath: filepath.Join(fakeBin, "reconify"), ArtifactDir: artifacts}
	report := runTrial(context.Background(), options, AgentClaude, scenarios[0], 1)

	if report.Error != "" {
		t.Fatalf("error = %q", report.Error)
	}
	// Only the agent's four calls are traced; the evaluator's own validate and
	// reconcile run without the trace variable.
	if len(report.Trace) != 4 || report.Trace[0].Argv[0] != "capabilities" || report.Trace[3].Argv[0] != "explain" {
		t.Fatalf("trace = %+v", report.Trace)
	}
	if report.Efficiency == nil || report.Efficiency.EngineCalls != 4 || report.Efficiency.FailedCalls != 0 || report.Efficiency.CallsToFirstValidation == nil || *report.Efficiency.CallsToFirstValidation != 2 {
		t.Fatalf("efficiency = %+v", report.Efficiency)
	}
	if report.Usage == nil || report.Usage.Turns == nil || *report.Usage.Turns != 4 || *report.Usage.InputTokens != 15 || *report.Usage.OutputTokens != 7 || *report.Usage.CostUSD != 0.25 || report.Usage.WallMS < 0 {
		t.Fatalf("usage = %+v", report.Usage)
	}
	if !report.Discovery || !report.Configuration || !report.Execution || !report.Classification || !report.ExactResult || !report.AssertionsMatch || !report.Explanation {
		t.Fatalf("legacy fields = %+v", report)
	}
	if len(report.Commands) != 4 || report.Failure != nil {
		t.Fatalf("commands = %v failure = %+v", report.Commands, report.Failure)
	}
	var names []string
	for _, g := range report.Grades {
		names = append(names, g.Name)
		if g.Name == gradeDecisionSurfaced {
			if g.Pass {
				t.Errorf("decision surfaced without keyword: %+v", g)
			}
			continue
		}
		if !g.Pass {
			t.Errorf("grade %s failed: %s", g.Name, g.Evidence)
		}
	}
	if !strings.Contains(strings.Join(names, ","), gradeDecisionSurfaced) {
		t.Fatalf("grades = %v", names)
	}
	if report.ArtifactPath == "" || !fileExists(filepath.Join(report.ArtifactPath, "verified-result.json")) || !fileExists(filepath.Join(report.ArtifactPath, traceFileName)) {
		t.Fatalf("artifact path = %q", report.ArtifactPath)
	}
}

func TestRunTrialLabelsFailureWhenAgentLeavesNoConfig(t *testing.T) {
	root, fakeBin := e2eCorpus(t)
	if err := os.WriteFile(filepath.Join(fakeBin, "claude"), []byte("#!/bin/sh\nreconify capabilities >/dev/null\nexit 1\n"), 0o700); err != nil { // #nosec G306 -- test executable.
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	scenarios, err := loadScenarios(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	report := runTrial(context.Background(), Options{SkillsDir: t.TempDir(), ReconifyPath: filepath.Join(fakeBin, "reconify")}, AgentClaude, scenarios[0], 1)
	if report.Classification || !report.Discovery {
		t.Fatalf("report = %+v", report)
	}
	if report.Failure == nil || report.Failure.Label != labelAgentError {
		t.Fatalf("failure = %+v", report.Failure)
	}
	if report.Usage == nil || report.Usage.Turns != nil || report.Usage.WallMS < 0 {
		t.Fatalf("usage = %+v", report.Usage)
	}
}
