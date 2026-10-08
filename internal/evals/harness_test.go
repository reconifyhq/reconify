package evals

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeScenario(t *testing.T, root, id, body string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func v2Scenario(id, extra string) string {
	return `{"schema":"reconify.engine.eval-scenario.v2","id":"` + id + `","prompt":"p","inputs":[],"reference_config":"reference/reconify.yaml","expected_result":"expected/result.json","expected_explanation":"expected/explanation.json","pair":"pair","assertions":{"matched":0,"unmatched_left":0,"unmatched_right":0,"amount_diff_count":0,"timing_diff_count":0,"duplicate_count":0,"grouped_matched_count":0,"many_to_many_matched_count":0,"ambiguous_group_count":0},"counter_examples":[]` + extra + `}`
}

func TestLoadScenariosReadsTagsKeywordsAndReference(t *testing.T) {
	root := t.TempDir()
	writeScenario(t, root, "010-ask", v2Scenario("010-ask", `,"tags":["ask-user","messy"],"decision_keywords":["which currency"]`))
	scenarios, err := loadScenarios(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := scenarios[0]
	if !reflect.DeepEqual(got.Tags, []string{"ask-user", "messy"}) || !reflect.DeepEqual(got.DecisionKeywords, []string{"which currency"}) || got.ReferenceConfig != "reference/reconify.yaml" {
		t.Fatalf("scenario = %+v", got)
	}
}

func TestFilterByTags(t *testing.T) {
	scenarios := []scenario{
		{ID: "a", Tags: []string{"core"}},
		{ID: "b", Tags: []string{"core", "messy"}},
		{ID: "c", Tags: []string{"scale"}},
		{ID: "d"},
	}
	ids := func(items []scenario) []string {
		var result []string
		for _, item := range items {
			result = append(result, item.ID)
		}
		return result
	}
	for _, testCase := range []struct {
		tags []string
		want []string
	}{
		{nil, []string{"a", "b", "c", "d"}},
		{[]string{"core"}, []string{"a", "b"}},
		{[]string{"messy", "scale"}, []string{"b", "c"}},
		{[]string{"repair"}, nil},
	} {
		if got := ids(filterByTags(scenarios, testCase.tags)); !reflect.DeepEqual(got, testCase.want) {
			t.Errorf("tags %v kept %v, want %v", testCase.tags, got, testCase.want)
		}
	}
}

func TestRunRejectsTagsMatchingNoScenario(t *testing.T) {
	root, binary := t.TempDir(), filepath.Join(t.TempDir(), "reconify")
	writeScenario(t, root, "010-core", v2Scenario("010-core", `,"tags":["core"]`))
	if err := os.WriteFile(binary, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), Options{CorpusDir: root, ReconifyPath: binary, Agents: []string{"codex"}, Trials: 1, Tags: []string{"scale"}, LookPath: func(string) (string, error) { return "/bin/codex", nil }})
	if err == nil || !strings.Contains(err.Error(), "tags") {
		t.Fatalf("err = %v", err)
	}
}

func emptyScenarioDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "scenario")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMaterializeInstallsHooksWithExecPermission(t *testing.T) {
	skills := t.TempDir()
	hooks := filepath.Join(skills, ".hooks", "claude")
	if err := os.MkdirAll(filepath.Join(hooks, "hooks"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(hooks, "hooks", "verify.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil { // #nosec G302 -- test fixture needs the exec bit.
		t.Fatal(err)
	}
	skillDir := filepath.Join(skills, ".claude", "reconify-engine-reconcile")
	if err := os.MkdirAll(skillDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("skill"), 0o600); err != nil {
		t.Fatal(err)
	}

	workspace, cleanup, err := materialize(Options{SkillsDir: skills, Hooks: true}, scenario{Dir: emptyScenarioDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := os.Stat(filepath.Join(workspace, ".claude", "hooks", "verify.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("hook script lost its exec bit: %v", info.Mode())
	}
	if !fileExists(filepath.Join(workspace, ".claude", "settings.json")) || !fileExists(filepath.Join(workspace, ".claude", "skills", "reconify-engine-reconcile", "SKILL.md")) {
		t.Fatal("hooks and skills were not merged into .claude")
	}
	// Without the Hooks option nothing from .hooks is installed.
	plain, plainCleanup, err := materialize(Options{SkillsDir: skills}, scenario{Dir: emptyScenarioDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer plainCleanup()
	if fileExists(filepath.Join(plain, ".claude", "settings.json")) {
		t.Fatal("hooks installed without Options.Hooks")
	}
}

func TestMaterializeReportsMissingHooks(t *testing.T) {
	_, _, err := materialize(Options{SkillsDir: t.TempDir(), Hooks: true}, scenario{Dir: emptyScenarioDir(t)})
	if err == nil || !strings.Contains(err.Error(), "no .hooks/claude") {
		t.Fatalf("err = %v", err)
	}
	report := runTrial(context.Background(), Options{SkillsDir: t.TempDir(), Hooks: true}, AgentClaude, scenario{Dir: emptyScenarioDir(t)}, 1)
	if !strings.Contains(report.Error, "no .hooks/claude") {
		t.Fatalf("trial error = %q", report.Error)
	}
}

func TestRunEngineStripsTraceVariable(t *testing.T) {
	t.Setenv(traceEnv, filepath.Join(t.TempDir(), "leak.jsonl"))
	binary := filepath.Join(t.TempDir(), "fake")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho \"trace=[$RECONIFY_TRACE_FILE]\" >&2\nexit 7\n"), 0o700); err != nil { // #nosec G306 -- test executable.
		t.Fatal(err)
	}
	stderr, err := runEngine(context.Background(), binary, t.TempDir())
	if err == nil {
		t.Fatal("expected the fake exit status")
	}
	if strings.TrimSpace(string(stderr)) != "trace=[]" {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRetainWorkspaceReplacesStaleDestinationAndKeepsModes(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(filepath.Join(workspace, "sub", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(workspace, "sub", "run.sh")
	if err := os.WriteFile(script, []byte("x"), 0o700); err != nil { // #nosec G306 -- test fixture.
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "kept", "trial")
	if err := os.MkdirAll(filepath.Join(destination, "stale"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := retainWorkspace(workspace, destination); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(destination, "stale")) || !fileExists(filepath.Join(destination, "sub", "empty")) {
		t.Fatal("destination was not replaced by the workspace")
	}
	// The cross-filesystem fallback is the same copy plus removal.
	copied := filepath.Join(t.TempDir(), "copy")
	if err := copyTreeMode(destination, copied); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(copied, "sub", "run.sh"))
	if err != nil || info.Mode().Perm()&0o100 == 0 || !fileExists(filepath.Join(copied, "sub", "empty")) {
		t.Fatalf("copy lost modes or empty directories: %v %v", info, err)
	}
}

func variantOf(name string, passes ...bool) VariantReport {
	trials := make([]TrialReport, len(passes))
	for i, pass := range passes {
		trials[i] = TrialReport{Trial: i + 1, Classification: pass}
	}
	return VariantReport{Name: name, Agents: []AgentReport{{Agent: "claude", Scenarios: []ScenarioReport{{ID: "s", Trials: trials}}}}}
}

func TestExtraArmsNeverChangeTheVerdict(t *testing.T) {
	core := []VariantReport{variantOf("candidate", true, true), variantOf("released", true, false), variantOf("no-skill", false, false)}
	baseComparisons, baseVerdict := releaseVerdict(core, 2)
	if baseVerdict.Status == "inconclusive" || len(baseComparisons) != 2 {
		t.Fatalf("base = %+v %+v", baseComparisons, baseVerdict)
	}
	for _, extra := range []VariantReport{variantOf(armCandidateHooks, false, false), variantOf(armCandidateHooks, true, true), variantOf(armCandidateHooks)} {
		comparisons, verdict := releaseVerdict(append(append([]VariantReport(nil), core...), extra), 2)
		if *verdict != *baseVerdict {
			t.Fatalf("extra arm changed verdict: %+v vs %+v", verdict, baseVerdict)
		}
		if len(comparisons) != 3 || !reflect.DeepEqual(comparisons[:2], baseComparisons) {
			t.Fatalf("comparisons = %+v", comparisons)
		}
		if last := comparisons[2]; last.Variant != armCandidateHooks || last.Against != "candidate" {
			t.Fatalf("extra comparison = %+v", last)
		}
	}
	comparisons, _ := releaseVerdict(append(append([]VariantReport(nil), core...), variantOf(armCandidateHooks, true, false)), 2)
	if got := comparisons[2]; got.Passed != 1 || got.Total != 2 || got.Delta != -0.5 {
		t.Fatalf("extra comparison = %+v", got)
	}
}

func TestReleaseVerdictNeedsAllCoreArms(t *testing.T) {
	_, verdict := releaseVerdict([]VariantReport{variantOf("candidate", true, true), variantOf("released", true, true), variantOf(armCandidateHooks, true, true)}, 2)
	if verdict.Status != "inconclusive" {
		t.Fatalf("verdict = %+v", verdict)
	}
}

func TestResolveExtraArms(t *testing.T) {
	arms, err := resolveExtraArms([]string{"candidate+hooks", " candidate+hooks "})
	if err != nil || !reflect.DeepEqual(arms, []string{armCandidateHooks}) {
		t.Fatalf("arms = %v err = %v", arms, err)
	}
	if _, err := resolveExtraArms([]string{"candidate+magic"}); err == nil || !strings.Contains(err.Error(), "candidate+magic") {
		t.Fatalf("err = %v", err)
	}
	if arms, err := resolveExtraArms(nil); err != nil || arms != nil {
		t.Fatalf("arms = %v err = %v", arms, err)
	}
	_, err = Release(context.Background(), ReleaseOptions{BaselineVersion: "0.6.0", Trials: 3, Models: []string{"claude=a", "codex=b", "gemini=c", "opencode=d"}, ExtraArms: []string{"nope"}, OutDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "unknown extra arm") {
		t.Fatalf("Release err = %v", err)
	}
}

func TestScenariosOrZeroAppliesTags(t *testing.T) {
	root := t.TempDir()
	writeScenario(t, root, "010-core", v2Scenario("010-core", `,"tags":["core"]`))
	writeScenario(t, root, "011-scale", v2Scenario("011-scale", `,"tags":["scale"]`))
	if got := scenariosOrZero(root, nil, []string{"core"}); len(got) != 1 || got[0].ID != "010-core" {
		t.Fatalf("scenarios = %+v", got)
	}
	if got := scenariosOrZero(root, nil, nil); len(got) != 2 {
		t.Fatalf("scenarios = %+v", got)
	}
}
