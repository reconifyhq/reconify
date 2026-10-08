package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reconifyhq/reconify/internal/evals"
)

func TestAbsoluteReconifyPathRequiresValue(t *testing.T) {
	if _, err := absoluteReconifyPath(""); err == nil || err.Error() != "--reconify is required" {
		t.Fatalf("empty path error = %v", err)
	}
}

func TestParseModelArguments(t *testing.T) {
	got, err := parseModelArguments([]string{"claude=sonnet", "codex = gpt=5"})
	if err != nil || got["claude"] != "sonnet" || got["codex"] != "gpt=5" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if got, err := parseModelArguments(nil); err != nil || got != nil {
		t.Fatalf("empty: got=%v err=%v", got, err)
	}
	for _, bad := range []string{"claude", "=sonnet", "claude="} {
		if _, err := parseModelArguments([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func writeReport(t *testing.T, name string, passes ...bool) string {
	t.Helper()
	trials := make([]evals.TrialReport, len(passes))
	for i, pass := range passes {
		trials[i] = evals.TrialReport{Trial: i + 1, Classification: pass}
	}
	report := evals.Report{Schema: "reconify.engine.eval-report.v1", Agents: []evals.AgentReport{{Agent: "claude", Scenarios: []evals.ScenarioReport{{ID: "basic", Trials: trials}}}}}
	path := filepath.Join(t.TempDir(), name)
	if err := evals.WriteReport(report, path, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCompareMainExitCodes(t *testing.T) {
	good := writeReport(t, "good.json", true, true)
	bad := writeReport(t, "bad.json", false, false)
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"unchanged", []string{good, good}, exitOK},
		{"improved", []string{bad, good}, exitOK},
		{"regressed", []string{good, bad}, exitRegression},
		{"flags after positionals", []string{good, bad, "--markdown"}, exitRegression},
		{"flags before positionals", []string{"--markdown", good, good}, exitOK},
		{"missing argument", []string{good}, exitUsage},
		{"unreadable report", []string{good, filepath.Join(t.TempDir(), "missing.json")}, exitUsage},
		{"unknown variant is ignored for plain reports", []string{"--variant", "released", good, good}, exitOK},
		{"unknown flag", []string{"--nope", good, good}, exitUsage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := compareMain(test.args, &stdout, &stderr); got != test.want {
				t.Fatalf("exit = %d, want %d; stderr: %s", got, test.want, stderr.String())
			}
		})
	}
}

func TestCompareMainOutput(t *testing.T) {
	good := writeReport(t, "good.json", true, true)
	bad := writeReport(t, "bad.json", false, false)
	var stdout, stderr bytes.Buffer
	if got := compareMain([]string{good, bad}, &stdout, &stderr); got != exitRegression {
		t.Fatalf("exit = %d", got)
	}
	var diff map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &diff); err != nil || diff["schema"] != "reconify.engine.eval-diff.v1" || diff["regression"] != true {
		t.Fatalf("stdout = %s err = %v", stdout.String(), err)
	}
	stdout.Reset()
	compareMain([]string{good, bad, "--markdown"}, &stdout, &stderr)
	if !strings.Contains(stdout.String(), "## Agent eval comparison") || !strings.Contains(stdout.String(), "**REGRESSION**") {
		t.Fatalf("markdown = %s", stdout.String())
	}
	stdout.Reset()
	target := filepath.Join(t.TempDir(), "diff.md")
	if got := compareMain([]string{good, bad, "--markdown", "--out", target}, &stdout, &stderr); got != exitRegression || stdout.Len() != 0 {
		t.Fatalf("exit = %d stdout = %q", got, stdout.String())
	}
}

func TestSummarizeMain(t *testing.T) {
	report := writeReport(t, "report.json", true, false)
	var stdout, stderr bytes.Buffer
	if got := summarizeMain([]string{report, "--markdown"}, &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d: %s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "| claude | basic | 1/2 | 50.0% |") {
		t.Fatalf("markdown = %s", stdout.String())
	}
	stdout.Reset()
	if got := summarizeMain([]string{report}, &stdout, &stderr); got != exitOK || !strings.Contains(stdout.String(), "reconify.engine.eval-summary.v1") {
		t.Fatalf("exit = %d stdout = %s", got, stdout.String())
	}
	if got := summarizeMain(nil, &stdout, &stderr); got != exitUsage {
		t.Fatalf("no args exit = %d", got)
	}
}
