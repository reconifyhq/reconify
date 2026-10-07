package evals

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func trials(passes ...bool) []TrialReport {
	result := make([]TrialReport, len(passes))
	for i, pass := range passes {
		result[i] = TrialReport{Trial: i + 1, Classification: pass}
	}
	return result
}

func agentReport(agent string, scenarios map[string][]TrialReport) AgentReport {
	report := AgentReport{Agent: agent}
	for _, id := range []string{"a-easy", "b-hard", "c-extra"} {
		if items, ok := scenarios[id]; ok {
			report.Scenarios = append(report.Scenarios, ScenarioReport{ID: id, Trials: items})
		}
	}
	return report
}

func plain(agents ...AgentReport) Report { return Report{Schema: reportSchemaV1, Agents: agents} }

func release(variants map[string][]AgentReport) Report {
	report := Report{Schema: reportSchemaV1}
	for _, name := range []string{"candidate", "released", "no-skill"} {
		if agents, ok := variants[name]; ok {
			report.Variants = append(report.Variants, VariantReport{Name: name, Agents: agents})
		}
	}
	return report
}

func TestCompareClassifiesNewlyFailingAndPassing(t *testing.T) {
	base := plain(agentReport("claude", map[string][]TrialReport{
		"a-easy":  trials(true, true, true),
		"b-hard":  trials(false, false, false),
		"c-extra": trials(true, true, true),
	}))
	head := plain(agentReport("claude", map[string][]TrialReport{
		"a-easy":  trials(false, false, true),
		"b-hard":  trials(true, true, false),
		"c-extra": trials(true, true, true),
	}))
	diff, err := Compare(base, head, "")
	if err != nil {
		t.Fatal(err)
	}
	if diff.Schema != "reconify.engine.eval-diff.v1" {
		t.Fatalf("schema = %q", diff.Schema)
	}
	if len(diff.NewlyFailing) != 1 || diff.NewlyFailing[0].Scenario != "a-easy" {
		t.Fatalf("newly failing = %+v", diff.NewlyFailing)
	}
	if len(diff.NewlyPassing) != 1 || diff.NewlyPassing[0].Scenario != "b-hard" {
		t.Fatalf("newly passing = %+v", diff.NewlyPassing)
	}
	if diff.Overall.Base.Passed != 6 || diff.Overall.Head.Passed != 6 || diff.Overall.Base.Total != 9 {
		t.Fatalf("overall = %+v", diff.Overall)
	}
	if diff.Overall.Delta != 0 || diff.Overall.Significant {
		t.Fatalf("overall delta = %v significant = %v", diff.Overall.Delta, diff.Overall.Significant)
	}
	if diff.Overall.Base.ConfidenceLower <= 0 || diff.Overall.Base.ConfidenceUpper >= 1 {
		t.Fatalf("wilson interval missing: %+v", diff.Overall.Base)
	}
	if !diff.Regression {
		t.Fatal("a-easy dropped 66.7 points; expected regression")
	}
	if len(diff.Cells) != 3 {
		t.Fatalf("cells = %d", len(diff.Cells))
	}
}

func TestRegressionThreshold(t *testing.T) {
	tests := []struct {
		name       string
		base, head []bool
		regressed  bool
		failing    int
	}{
		{"one of three lost is noise", []bool{true, true, true}, []bool{true, true, false}, false, 1},
		{"two of three lost", []bool{true, true, true}, []bool{true, false, false}, true, 1},
		{"single trial flip", []bool{true}, []bool{false}, true, 1},
		{"two trials, one lost", []bool{true, true}, []bool{true, false}, true, 1},
		{"improvement", []bool{false, false}, []bool{true, true}, false, 0},
		{"unchanged failing", []bool{false}, []bool{false}, false, 0},
		{"ten trials, one lost", make10(10), make10(9), false, 1},
		{"ten trials, four lost", make10(10), make10(6), true, 1},
		{"ten trials, three lost", make10(10), make10(7), false, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			diff, err := Compare(
				plain(agentReport("claude", map[string][]TrialReport{"a-easy": trials(test.base...)})),
				plain(agentReport("claude", map[string][]TrialReport{"a-easy": trials(test.head...)})), "")
			if err != nil {
				t.Fatal(err)
			}
			if diff.Regressed() != test.regressed || diff.Regression != test.regressed {
				t.Fatalf("Regressed() = %v, want %v", diff.Regressed(), test.regressed)
			}
			if len(diff.NewlyFailing) != test.failing {
				t.Fatalf("newly failing = %d, want %d", len(diff.NewlyFailing), test.failing)
			}
		})
	}
}

func make10(passes int) []bool {
	result := make([]bool, 10)
	for i := 0; i < passes; i++ {
		result[i] = true
	}
	return result
}

func TestCompareExcludesPairsMissingFromOneSide(t *testing.T) {
	base := plain(agentReport("claude", map[string][]TrialReport{"a-easy": trials(true), "b-hard": trials(true)}))
	head := plain(agentReport("claude", map[string][]TrialReport{"a-easy": trials(true), "c-extra": trials(false)}))
	diff, err := Compare(base, head, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.OnlyInBase) != 1 || diff.OnlyInBase[0] != "claude/b-hard" || len(diff.OnlyInHead) != 1 || diff.OnlyInHead[0] != "claude/c-extra" {
		t.Fatalf("only base=%v head=%v", diff.OnlyInBase, diff.OnlyInHead)
	}
	if diff.Overall.Base.Total != 1 || diff.Overall.Head.Total != 1 || diff.Regression {
		t.Fatalf("overall = %+v regression = %v", diff.Overall, diff.Regression)
	}
}

func TestCompareVariantSelection(t *testing.T) {
	base := release(map[string][]AgentReport{
		"candidate": {agentReport("claude", map[string][]TrialReport{"a-easy": trials(true, true)})},
		"released":  {agentReport("claude", map[string][]TrialReport{"a-easy": trials(false, false)})},
	})
	head := release(map[string][]AgentReport{
		"candidate": {agentReport("claude", map[string][]TrialReport{"a-easy": trials(true, false)})},
		"released":  {agentReport("claude", map[string][]TrialReport{"a-easy": trials(true, true)})},
	})
	diff, err := Compare(base, head, "")
	if err != nil {
		t.Fatal(err)
	}
	if diff.BaseVariant != "candidate" || diff.HeadVariant != "candidate" || len(diff.NewlyFailing) != 1 {
		t.Fatalf("default variant diff = %+v", diff)
	}
	diff, err = Compare(base, head, "released")
	if err != nil {
		t.Fatal(err)
	}
	if diff.BaseVariant != "released" || len(diff.NewlyPassing) != 1 || diff.Regression {
		t.Fatalf("released variant diff = %+v", diff)
	}
	if _, err := Compare(base, head, "no-skill"); err == nil || !strings.Contains(err.Error(), "no-skill") || !strings.Contains(err.Error(), "candidate") {
		t.Fatalf("missing variant error = %v", err)
	}
}

func TestCompareMixesPlainAndReleaseReports(t *testing.T) {
	base := plain(agentReport("claude", map[string][]TrialReport{"a-easy": trials(true)}))
	head := release(map[string][]AgentReport{"candidate": {agentReport("claude", map[string][]TrialReport{"a-easy": trials(false)})}})
	diff, err := Compare(base, head, "")
	if err != nil {
		t.Fatal(err)
	}
	if diff.BaseVariant != "" || diff.HeadVariant != "candidate" || !diff.Regression {
		t.Fatalf("diff = %+v", diff)
	}
}

func TestSelectAgentsSingleVariantNeedsNoName(t *testing.T) {
	report := Report{Variants: []VariantReport{{Name: "only", Agents: []AgentReport{{Agent: "claude"}}}}}
	name, agents, err := selectAgents(report, "")
	if err != nil || name != "only" || len(agents) != 1 {
		t.Fatalf("name=%q agents=%v err=%v", name, agents, err)
	}
}

func intp(value int) *int           { return &value }
func floatp(value float64) *float64 { return &value }

func TestCompareFailureLabelsAndMedians(t *testing.T) {
	makeTrial := func(pass bool, label string, calls, failed, usageErrors int, usage *Usage) TrialReport {
		trial := TrialReport{Classification: pass, Efficiency: &Efficiency{EngineCalls: calls, FailedCalls: failed, UsageErrors: usageErrors}, Usage: usage}
		if label != "" {
			trial.Failure = &FailureReport{Label: label}
		}
		return trial
	}
	base := plain(agentReport("claude", map[string][]TrialReport{"a-easy": {
		makeTrial(false, "config_invalid", 4, 2, 0, &Usage{Turns: intp(10), InputTokens: intp(1000), OutputTokens: intp(200), CostUSD: floatp(0.10), WallMS: 1000}),
		makeTrial(false, "config_invalid", 6, 3, 1, &Usage{Turns: intp(20), InputTokens: intp(3000), OutputTokens: intp(400), CostUSD: floatp(0.30), WallMS: 3000}),
		makeTrial(true, "", 5, 0, 0, &Usage{Turns: intp(30), InputTokens: intp(2000), OutputTokens: intp(300), CostUSD: floatp(0.20), WallMS: 2000}),
	}}))
	head := plain(agentReport("claude", map[string][]TrialReport{"a-easy": {
		makeTrial(false, "wrong_tolerance", 2, 0, 0, nil),
		makeTrial(true, "", 2, 0, 0, nil),
		makeTrial(true, "", 4, 0, 0, nil),
	}}))
	diff, err := Compare(base, head, "")
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]LabelDiff{}
	for _, label := range diff.FailureLabels {
		labels[label.Label] = label
	}
	if labels["config_invalid"] != (LabelDiff{Label: "config_invalid", Base: 2, Head: 0, Delta: -2}) || labels["wrong_tolerance"].Head != 1 {
		t.Fatalf("labels = %+v", diff.FailureLabels)
	}
	metrics := map[string]MetricDiff{}
	for _, metric := range diff.Metrics {
		metrics[metric.Name] = metric
	}
	calls := metrics["engine_calls"]
	if calls.Base == nil || *calls.Base != 5 || calls.Head == nil || *calls.Head != 2 || calls.Delta == nil || *calls.Delta != -3 {
		t.Fatalf("engine_calls = %+v", calls)
	}
	if usageErrors := metrics["usage_errors"]; usageErrors.Base == nil || *usageErrors.Base != 0 {
		t.Fatalf("usage_errors = %+v", usageErrors)
	}
	tokens := metrics["tokens"]
	if tokens.Base == nil || *tokens.Base != 2300 || tokens.Head != nil || tokens.Delta != nil || tokens.BaseCount != 3 || tokens.HeadCount != 0 {
		t.Fatalf("tokens = %+v", tokens)
	}
	if turns := metrics["turns"]; turns.Base == nil || *turns.Base != 20 {
		t.Fatalf("turns = %+v", turns)
	}
	if cost := metrics["cost_usd"]; cost.Base == nil || *cost.Base < 0.199 || *cost.Base > 0.201 {
		t.Fatalf("cost = %+v", cost)
	}
	data, err := json.Marshal(diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"name":"tokens","base":2300,"base_count":3,"head_count":0`) {
		t.Fatalf("nil head should be omitted from JSON: %s", data)
	}
}

func TestCompareHandlesAbsentUsageAndEfficiency(t *testing.T) {
	base := plain(agentReport("claude", map[string][]TrialReport{"a-easy": trials(true, false)}))
	head := plain(agentReport("claude", map[string][]TrialReport{"a-easy": trials(true, true)}))
	diff, err := Compare(base, head, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Metrics) != 0 || len(diff.FailureLabels) != 0 {
		t.Fatalf("metrics=%+v labels=%+v", diff.Metrics, diff.FailureLabels)
	}
	markdown := RenderDiffMarkdown(diff)
	if strings.Contains(markdown, "Medians per trial") || !strings.Contains(markdown, "No labeled failures.") {
		t.Fatalf("markdown:\n%s", markdown)
	}
}

func TestCompareFallsBackToScenarioMetricsWithoutTrials(t *testing.T) {
	scenario := func(passed, total int) AgentReport {
		return AgentReport{Agent: "claude", Scenarios: []ScenarioReport{{ID: "a-easy", Classification: Metrics{Passed: passed, Total: total}}}}
	}
	diff, err := Compare(plain(scenario(3, 3)), plain(scenario(0, 3)), "")
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Regression || diff.Overall.Base.Total != 3 {
		t.Fatalf("diff = %+v", diff)
	}
}

func TestRenderDiffMarkdown(t *testing.T) {
	base := plain(agentReport("claude", map[string][]TrialReport{
		"a-easy": {{Classification: true, Efficiency: &Efficiency{EngineCalls: 4}}},
		"b-hard": trials(false),
	}))
	head := plain(agentReport("claude", map[string][]TrialReport{
		"a-easy": {{Classification: false, Failure: &FailureReport{Label: "wrong_date_layout"}, Efficiency: &Efficiency{EngineCalls: 7}}},
		"b-hard": trials(true),
	}))
	diff, err := Compare(base, head, "")
	if err != nil {
		t.Fatal(err)
	}
	markdown := RenderDiffMarkdown(diff)
	for _, want := range []string{
		"## Agent eval comparison",
		"| | Base | Head | Delta |",
		"| Classification pass rate | 50.0% (1/2, 95% CI",
		"| 50.0% (1/2, 95% CI", // head side
		"+0.0 pts",
		"1 newly failing, 1 newly passing. Status: **REGRESSION**",
		"### Newly failing",
		"| claude | a-easy | 1/1 (100.0%) | 0/1 (0.0%) | -100.0 pts |",
		"### Newly passing",
		"| claude | b-hard | 0/1 (0.0%) | 1/1 (100.0%) | +100.0 pts |",
		"### Failure labels",
		"| wrong_date_layout | 0 | 1 | +1 |",
		"| Engine calls | 4 | 7 | +3 |",
		"<details><summary>All agent and scenario pass rates</summary>",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("markdown missing %q:\n%s", want, markdown)
		}
	}
	if strings.Index(markdown, "### Newly failing") > strings.Index(markdown, "### Newly passing") {
		t.Error("regressions must come before improvements")
	}
	if strings.Index(markdown, "### Newly failing") > strings.Index(markdown, "### Failure labels") {
		t.Error("regressions must come before the failure label table")
	}
}

func TestRenderDiffMarkdownWithoutRegression(t *testing.T) {
	report := plain(agentReport("claude", map[string][]TrialReport{"a-easy": trials(true)}))
	diff, err := Compare(report, report, "")
	if err != nil {
		t.Fatal(err)
	}
	markdown := RenderDiffMarkdown(diff)
	if !strings.Contains(markdown, "Status: no regression") || strings.Count(markdown, "None.") != 2 {
		t.Fatalf("markdown:\n%s", markdown)
	}
}

func TestSummarizeAndRender(t *testing.T) {
	report := plain(
		agentReport("claude", map[string][]TrialReport{
			"a-easy": {
				{Classification: true, Usage: &Usage{Turns: intp(8), WallMS: 4000, CostUSD: floatp(0.5)}},
				{Classification: false, Failure: &FailureReport{Label: "timeout"}},
			},
			"b-hard": trials(false),
		}),
		agentReport("codex", map[string][]TrialReport{"a-easy": trials(true)}),
	)
	summary, err := Summarize(report, "")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Schema != "reconify.engine.eval-summary.v1" || summary.Overall.Passed != 2 || summary.Overall.Total != 4 || len(summary.Cells) != 3 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(summary.FailureLabels) != 1 || summary.FailureLabels[0] != (LabelCount{Label: "timeout", Count: 1}) {
		t.Fatalf("labels = %+v", summary.FailureLabels)
	}
	var names []string
	for _, metric := range summary.Metrics {
		names = append(names, metric.Name)
	}
	if strings.Join(names, ",") != "turns,cost_usd,wall_ms" {
		t.Fatalf("metrics = %v (nil values must be skipped)", names)
	}
	markdown := RenderSummaryMarkdown(summary)
	for _, want := range []string{
		"## Agent eval summary",
		"Classification pass rate: **50.0% (2/4, 95% CI",
		"| claude | a-easy | 1/2 | 50.0% |",
		"| codex | a-easy | 1/1 | 100.0% |",
		"| timeout | 1 |",
		"| Agent turns | 8 | 1 |",
		"| Cost (USD) | $0.5000 | 1 |",
		"| Wall time | 4.0s | 1 |",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("markdown missing %q:\n%s", want, markdown)
		}
	}
}

func TestSummarizeEmptyReport(t *testing.T) {
	summary, err := Summarize(Report{Schema: reportSchemaV1}, "")
	if err != nil {
		t.Fatal(err)
	}
	markdown := RenderSummaryMarkdown(summary)
	if !strings.Contains(markdown, "No agent results in this report.") || !strings.Contains(markdown, "n/a") {
		t.Fatalf("markdown:\n%s", markdown)
	}
}

func TestMedian(t *testing.T) {
	if median(nil) != nil {
		t.Fatal("median of nothing must be nil")
	}
	if got := median([]float64{3, 1, 2}); *got != 2 {
		t.Fatalf("odd median = %v", *got)
	}
	if got := median([]float64{4, 1, 2, 3}); *got != 2.5 {
		t.Fatalf("even median = %v", *got)
	}
}

func TestLoadReportAndWriters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := WriteReport(plain(agentReport("claude", map[string][]TrialReport{"a-easy": trials(true)})), path, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReport(path)
	if err != nil || len(loaded.Agents) != 1 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"schema":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReport(bad); err == nil {
		t.Fatal("unsupported schema accepted")
	}
	if _, err := LoadReport(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
	var out bytes.Buffer
	if err := WriteJSON(map[string]int{"a": 1}, "", &out); err != nil || !strings.HasSuffix(out.String(), "}\n") {
		t.Fatalf("stdout=%q err=%v", out.String(), err)
	}
	target := filepath.Join(dir, "out.md")
	if err := WriteText("hello\n", target, &out); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "hello\n" { // #nosec G304 -- test temp dir.
		t.Fatalf("file = %q", data)
	}
}
