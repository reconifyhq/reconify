package evals

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAgentsSkipsUnavailableOnlyForAll(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "codex" {
			return "", errors.New("missing")
		}
		return "/bin/" + name, nil
	}
	agents, skipped, err := resolveAgents([]string{"all"}, lookPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 3 || len(skipped) != 1 || skipped[0].Agent != "codex" {
		t.Fatalf("agents=%v skipped=%+v", agents, skipped)
	}
	if _, _, err := resolveAgents([]string{"codex"}, lookPath); err == nil {
		t.Fatal("selected unavailable agent succeeded")
	}
}

func TestRunValidatesRequiredOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reconify")
	if err := os.WriteFile(path, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{ReconifyPath: path, Agents: []string{"codex"}}); err == nil {
		t.Fatal("missing trials succeeded")
	}
	if _, err := Run(context.Background(), Options{ReconifyPath: path, Trials: 1}); err == nil {
		t.Fatal("missing agent succeeded")
	}
}

func TestMetrics(t *testing.T) {
	got := metrics([]TrialReport{{Classification: false}, {Classification: true}}, func(report TrialReport) bool { return report.Classification })
	if got.PassAt1 || got.PassAll {
		t.Fatalf("metrics=%+v", got)
	}
}

func TestSemanticResultEqualIgnoresGeneratedAndOrderingFields(t *testing.T) {
	expected := []byte(`{"matched":[{"left":{"id":"a","source":"left","amount":10,"reference":"R"},"right":{"id":"b","source":"right","amount":10,"reference":"R"}}],"unmatched_left":[],"unmatched_right":[],"amount_diff":null,"timing_diff":null,"duplicates":null}`)
	actual := []byte(`{"matched":[{"left":{"id":"new-a","source":"other","amount":10,"reference":"R"},"right":{"id":"new-b","source":"other","amount":10,"reference":"R"}}],"unmatched_left":[],"unmatched_right":[],"amount_diff":null,"timing_diff":null,"duplicates":null,"index_selection":{"backend":"memory"}}`)
	if !semanticResultEqual(actual, expected) {
		t.Fatal("semantically equal results differ")
	}
	wrong := []byte(`{"matched":[{"left":{"amount":11,"reference":"R"},"right":{"amount":11,"reference":"R"}}],"unmatched_left":[],"unmatched_right":[],"amount_diff":null,"timing_diff":null,"duplicates":null}`)
	if semanticResultEqual(wrong, expected) {
		t.Fatal("different transaction amounts compared equal")
	}
}

func TestMaterializeInstallsSkillsAtPublishedPaths(t *testing.T) {
	root := t.TempDir()
	skills := filepath.Join(root, "skills")
	for _, dir := range []string{".agents", ".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(skills, dir, "reconify-engine-reconcile"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skills, dir, "reconify-engine-reconcile", "SKILL.md"), []byte("skill"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scenarioDir := filepath.Join(root, "scenario")
	if err := os.MkdirAll(filepath.Join(scenarioDir, "inputs"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scenarioDir, "inputs", "left.csv"), []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, cleanup, err := materialize(Options{SkillsDir: skills}, scenario{Dir: scenarioDir, Inputs: []string{"inputs/left.csv"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, dir := range []string{".agents", ".claude", ".codex"} {
		path := filepath.Join(workspace, dir, "skills", "reconify-engine-reconcile", "SKILL.md")
		if !fileExists(path) {
			t.Fatalf("skill missing at %s", path)
		}
	}
}

func TestPublishedAdaptersPointToWorkspaceCanonicalSkills(t *testing.T) {
	for _, adapter := range []string{
		"../../skills/.claude/reconify-engine-reconcile/SKILL.md",
		"../../skills/.codex/reconify-engine-reconcile/SKILL.md",
	} {
		data, err := os.ReadFile(adapter) // #nosec G304 -- fixed repository test paths.
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "../../.agents/skills") {
			t.Fatalf("adapter %s contains a broken relative canonical path", adapter)
		}
		if !strings.Contains(text, ".agents/skills/reconify-engine-reconcile/SKILL.md") {
			t.Fatalf("adapter %s does not point to the workspace canonical skill", adapter)
		}
	}
}

func TestTaskPromptIsNeutralAndRequiresArtifacts(t *testing.T) {
	prompt := taskPrompt(scenario{Prompt: "p", Pair: "left_vs_right"})
	for _, required := range []string{"valid configuration", "reconciliation artifact", "explanation"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q", required)
		}
	}
	for _, leaked := range []string{"reconify capabilities", "reconify inspect", "config schema", "config validate", "check-source"} {
		if strings.Contains(prompt, leaked) {
			t.Fatalf("prompt leaks workflow command %q", leaked)
		}
	}
}

func TestLoadScenariosSupportsPublishedV1(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "legacy")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	fixture := `{"schema":"reconify.engine.eval-scenario.v1","id":"legacy","prompt":"p","inputs":[],"reference_config":"reference/reconify.yaml","expected_result":"expected/result.json","pair":"pair","assertions":{"matched":0,"unmatched_left":0,"unmatched_right":0,"amount_diff_count":0,"timing_diff_count":0,"duplicate_count":0,"grouped_matched_count":0,"many_to_many_matched_count":0,"ambiguous_group_count":0},"counter_examples":[]}`
	if err := os.WriteFile(filepath.Join(dir, "scenario.json"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	scenarios, err := loadScenarios(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 1 || scenarios[0].ExpectedExplanation != "" {
		t.Fatalf("scenarios=%+v", scenarios)
	}
}

func TestAgentCommandsUseIsolatedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	for _, agent := range supportedAgents() {
		cmd := agentCommand(context.Background(), agent, workspace, "test")
		if cmd.Path == "" {
			t.Fatalf("%s has no command", agent)
		}
		if agent != AgentClaude && agent != AgentGemini && !containsString(cmd.Args, workspace) {
			t.Fatalf("%s does not receive workspace: %v", agent, cmd.Args)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestResolveArtifactAcceptsCurrentAndPublishedNames(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		present   []string
		candidate []string
		want      string
	}{
		{name: "current result", present: []string{"result.json"}, candidate: resultArtifactNames, want: "result.json"},
		{name: "published result", present: []string{"agent-result.json"}, candidate: resultArtifactNames, want: "agent-result.json"},
		{name: "current wins", present: []string{"result.json", "agent-result.json"}, candidate: resultArtifactNames, want: "result.json"},
		{name: "no result", present: []string{"verified-result.json"}, candidate: resultArtifactNames, want: ""},
		{name: "current explanation", present: []string{"explanation.json"}, candidate: explanationArtifactNames, want: "explanation.json"},
		{name: "published explanation", present: []string{"agent-explanation.json"}, candidate: explanationArtifactNames, want: "agent-explanation.json"},
		{name: "no explanation", present: nil, candidate: explanationArtifactNames, want: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			workspace := t.TempDir()
			for _, name := range testCase.present {
				if err := os.WriteFile(filepath.Join(workspace, name), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			resolved := resolveArtifact(workspace, testCase.candidate)
			want := ""
			if testCase.want != "" {
				want = filepath.Join(workspace, testCase.want)
			}
			if resolved != want {
				t.Fatalf("resolveArtifact = %q, want %q", resolved, want)
			}
		})
	}
}

func TestCanonicalSkillsUsePublicArtifactNames(t *testing.T) {
	skill, err := os.ReadFile(filepath.Join("..", "..", "skills", ".agents", "reconify-engine-reconcile", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"agent-result.json", "agent-explanation.json"} {
		if strings.Contains(string(skill), leaked) {
			t.Fatalf("evaluator-specific name %q leaked into the shipped skill", leaked)
		}
	}
	for _, required := range []string{"--out result.json", "explain result.json > explanation.json"} {
		if !strings.Contains(string(skill), required) {
			t.Fatalf("shipped skill missing %q", required)
		}
	}
}

func TestSemanticResultEqualIgnoresDescriptiveMappings(t *testing.T) {
	expected := []byte(`{"matched":[{"left":{"amount":10,"reference":"R","name":"Sale","group_key":"R"},"right":{"amount":10,"reference":"R","name":"Settlement"}}],"amount_diff":[]}`)
	actual := []byte(`{"matched":[{"left":{"amount":10,"reference":"R","name":""},"right":{"amount":10,"reference":"R","name":""}}],"amount_diff":null}`)
	if !semanticResultEqual(actual, expected) {
		t.Fatal("an unmapped description column must not change the classification")
	}
}

func TestSemanticResultEqualComparesGroupedSections(t *testing.T) {
	expected := []byte(`{"matched":[],"grouped_matched":[{"left":{"amount":30,"reference":"G"},"rights":[{"amount":10,"reference":"G"},{"amount":20,"reference":"G"}]}]}`)
	split := []byte(`{"matched":[],"grouped_matched":[{"left":{"amount":30,"reference":"G"},"rights":[{"amount":15,"reference":"G"},{"amount":15,"reference":"G"}]}]}`)
	if semanticResultEqual(split, expected) {
		t.Fatal("different group compositions compared equal")
	}
	missing := []byte(`{"matched":[]}`)
	if semanticResultEqual(missing, expected) {
		t.Fatal("a missing grouped section compared equal")
	}
}

func TestSemanticResultEqualComparesBySourceWithoutNames(t *testing.T) {
	expected := []byte(`{"by_source":{"stripe":{"matched":2,"unmatched_right":0},"paypal":{"matched":1,"unmatched_right":1}}}`)
	renamed := []byte(`{"by_source":{"psp_a":{"matched":1,"unmatched_right":1},"psp_b":{"matched":2,"unmatched_right":0}}}`)
	if !semanticResultEqual(renamed, expected) {
		t.Fatal("renamed counterpart sources must compare equal")
	}
	wrong := []byte(`{"by_source":{"stripe":{"matched":3,"unmatched_right":0},"paypal":{"matched":0,"unmatched_right":1}}}`)
	if semanticResultEqual(wrong, expected) {
		t.Fatal("different per-source counters compared equal")
	}
}
