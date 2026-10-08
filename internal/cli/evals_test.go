package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reconifyhq/reconify/schemas"
)

// evalsDir is the agent evaluation corpus, relative to this package.
const evalsDir = "../../evals"

// The corpus under evals/ is graded reference data: an agent's config is scored
// by how closely its reconciliation outcome matches expected/result.json. These
// tests are what keep that answer key honest. Without them the corpus is inert
// data that nothing exercises, and a wrong reference config fails silently by
// grading every agent against the wrong answer.

func loadScenarios(t *testing.T) map[string]schemas.EvalScenarioV2 {
	t.Helper()
	entries, err := os.ReadDir(evalsDir)
	if err != nil {
		t.Fatalf("read evals dir: %v", err)
	}
	scenarios := make(map[string]schemas.EvalScenarioV2)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(evalsDir, entry.Name(), "scenario.json")
		data, err := os.ReadFile(path) // #nosec G304 -- checked-in corpus fixture.
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var scenario schemas.EvalScenarioV2
		if err := json.Unmarshal(data, &scenario); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		scenarios[entry.Name()] = scenario
	}
	if len(scenarios) == 0 {
		t.Fatal("no scenarios found under evals/")
	}
	return scenarios
}

// materialize builds the working directory an evaluation runner constructs for
// a candidate config: the scenario inputs plus the config at the directory
// root. The config must sit at the root because file_pattern resolves relative
// to the config file, not the process working directory.
func materialize(t *testing.T, scenarioDir, configPath string) string {
	t.Helper()
	workDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workDir, "inputs"), 0o750); err != nil {
		t.Fatal(err)
	}
	inputs, err := os.ReadDir(filepath.Join(scenarioDir, "inputs"))
	if err != nil {
		t.Fatalf("read inputs: %v", err)
	}
	for _, input := range inputs {
		copyFile(t,
			filepath.Join(scenarioDir, "inputs", input.Name()),
			filepath.Join(workDir, "inputs", input.Name()))
	}
	copyFile(t, configPath, filepath.Join(workDir, "reconify.yaml"))
	return workDir
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src) // #nosec G304 -- checked-in corpus fixture.
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil { // #nosec G703 -- dst is always under t.TempDir().
		t.Fatalf("write %s: %v", dst, err)
	}
}

// runScenario drives the same CLI surface an external agent is graded through:
// config validate, then reconcile. It returns the result document, or the gate
// the config failed at.
func runScenario(t *testing.T, workDir, pair string) (result []byte, gate string) {
	t.Helper()
	configPath := filepath.Join(workDir, "reconify.yaml")

	validate := newRootCmd("test", "test")
	validate.SetOut(io.Discard)
	validate.SetErr(io.Discard)
	validate.SetArgs([]string{"config", "validate", "-c", configPath})
	if err := validate.Execute(); err != nil {
		return nil, "valid"
	}

	outPath := filepath.Join(workDir, "result.json")
	reconcile := newRootCmd("test", "test")
	reconcile.SetOut(io.Discard)
	reconcile.SetErr(io.Discard)
	reconcile.SetArgs([]string{
		"reconcile", "-c", configPath, "--pair", pair,
		"--format", "json", "--deterministic", "--out", outPath,
	})
	if err := reconcile.Execute(); err != nil {
		return nil, "runs"
	}

	data, err := os.ReadFile(outPath) // #nosec G304 -- test-owned temp file.
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	return data, ""
}

func assertionsOf(t *testing.T, result []byte) schemas.EvalAssertions {
	t.Helper()
	var document struct {
		Summary schemas.EvalAssertions `json:"summary"`
	}
	if err := json.Unmarshal(result, &document); err != nil {
		t.Fatalf("unmarshal result summary: %v", err)
	}
	return document.Summary
}

// TestEvalReferenceConfigsProduceExpectedResults is the answer-key check: every
// reference config must still reconcile to the committed expected result. It
// fails if a fixture, a reference config, or engine behavior drifts apart.
func TestEvalReferenceConfigsProduceExpectedResults(t *testing.T) {
	for name, scenario := range loadScenarios(t) {
		t.Run(name, func(t *testing.T) {
			if scenario.Schema != schemas.EvalScenarioSchemaV2 {
				t.Fatalf("schema = %q, want %q", scenario.Schema, schemas.EvalScenarioSchemaV2)
			}
			if scenario.ID != name {
				t.Fatalf("id = %q, want directory name %q", scenario.ID, name)
			}

			scenarioDir := filepath.Join(evalsDir, name)
			workDir := materialize(t, scenarioDir, filepath.Join(scenarioDir, scenario.ReferenceConfig))
			result, gate := runScenario(t, workDir, scenario.Pair)
			if gate != "" {
				t.Fatalf("reference config failed the %q gate", gate)
			}

			expected, err := os.ReadFile(filepath.Join(scenarioDir, scenario.ExpectedResult)) // #nosec G304 -- checked-in corpus fixture.
			if err != nil {
				t.Fatalf("read expected result: %v", err)
			}
			if !bytes.Equal(bytes.TrimSpace(result), bytes.TrimSpace(expected)) {
				t.Errorf("reference config no longer reproduces %s", scenario.ExpectedResult)
			}
			if got := assertionsOf(t, result); got != scenario.Assertions {
				t.Errorf("summary assertions drifted\n got: %+v\nwant: %+v", got, scenario.Assertions)
			}
			explainCmd := newRootCmd("test", "test")
			var explanation bytes.Buffer
			explainCmd.SetOut(&explanation)
			explainCmd.SetErr(io.Discard)
			explainCmd.SetArgs([]string{"explain", filepath.Join(workDir, "result.json")})
			if err := explainCmd.Execute(); err != nil {
				t.Fatalf("explain reference result: %v", err)
			}
			expectedExplanation, err := os.ReadFile(filepath.Join(scenarioDir, scenario.ExpectedExplanation)) // #nosec G304 -- checked-in corpus fixture.
			if err != nil {
				t.Fatalf("read %s: %v", scenario.ExpectedExplanation, err)
			}
			if !bytes.Equal(bytes.TrimSpace(explanation.Bytes()), bytes.TrimSpace(expectedExplanation)) {
				t.Errorf("reference config no longer reproduces %s", scenario.ExpectedExplanation)
			}
		})
	}
}

// TestEvalCounterExamplesDoNotReproduceExpectedResults is the discrimination
// check. A scenario that any plausible-but-wrong config also satisfies measures
// nothing, so each counter-example must fail a gate or produce different
// summary counters. This is what forces the fixtures to carry rows that punish
// an over-wide tolerance or window.
func TestEvalCounterExamplesDoNotReproduceExpectedResults(t *testing.T) {
	for name, scenario := range loadScenarios(t) {
		t.Run(name, func(t *testing.T) {
			if len(scenario.CounterExamples) == 0 {
				t.Fatal("scenario has no counter examples, so nothing proves it discriminates")
			}
			scenarioDir := filepath.Join(evalsDir, name)
			for _, rel := range scenario.CounterExamples {
				t.Run(filepath.Base(rel), func(t *testing.T) {
					workDir := materialize(t, scenarioDir, filepath.Join(scenarioDir, rel))
					result, gate := runScenario(t, workDir, scenario.Pair)
					if gate != "" {
						return // rejected before it could be scored, which is a pass
					}
					if got := assertionsOf(t, result); got == scenario.Assertions {
						t.Errorf("counter example reproduces the expected summary %+v, so the scenario does not discriminate", got)
					}
				})
			}
		})
	}
}

var validEvalTags = map[string]bool{"core": true, "messy": true, "scale": true, "repair": true, "ask-user": true}

// configKeywords are config-surface terms a prompt must never contain. A prompt
// describes the business situation; naming the key under test turns a scenario
// into a typing exercise. The list covers keys whose names are not ordinary
// business words.
var configKeywords = []string{
	"multiplier", "date_layout", "date_col", "amount_col", "ref_col", "name_col", "currency_col",
	"group_col", "file_pattern", "date_window", "amount_tolerance", "name_mode", "name_match_threshold",
	"duplicate_policy", "one_to_many", "many_to_many", "subset_sum", "reference_one_to_one",
	"name_tokens_one_to_one", "skip_raw", "result_mode", "thousands:", "decimal:",
}

// TestEvalScenarioMetadata keeps the corpus contract honest beyond the answer
// key: tiers are known, ask-user scenarios carry decision keywords, listed
// inputs are exactly the files on disk, and prompts do not leak config keys.
func TestEvalScenarioMetadata(t *testing.T) {
	for name, scenario := range loadScenarios(t) {
		t.Run(name, func(t *testing.T) {
			scenarioDir := filepath.Join(evalsDir, name)

			if len(scenario.Tags) == 0 {
				t.Fatal("scenario has no tags; every scenario belongs to a tier")
			}
			tags := map[string]bool{}
			for _, tag := range scenario.Tags {
				if !validEvalTags[tag] {
					t.Errorf("unknown tag %q", tag)
				}
				tags[tag] = true
			}
			if tags["ask-user"] != (len(scenario.DecisionKeywords) > 0) {
				t.Errorf("decision_keywords must be set exactly on ask-user scenarios (tags=%v, keywords=%v)",
					scenario.Tags, scenario.DecisionKeywords)
			}
			if tags["repair"] && len(scenario.InitialFiles) == 0 {
				t.Error("repair scenario has no initial_files to repair")
			}

			entries, err := os.ReadDir(filepath.Join(scenarioDir, "inputs"))
			if err != nil {
				t.Fatalf("read inputs: %v", err)
			}
			onDisk := map[string]bool{}
			for _, entry := range entries {
				onDisk["inputs/"+entry.Name()] = true
			}
			listed := map[string]bool{}
			for _, input := range scenario.Inputs {
				listed[input] = true
				if !onDisk[input] {
					t.Errorf("listed input %s does not exist", input)
				}
			}
			for input := range onDisk {
				if !listed[input] {
					t.Errorf("input %s exists on disk but is not listed in scenario.json", input)
				}
			}
			for _, initial := range scenario.InitialFiles {
				if _, err := os.Stat(filepath.Join(scenarioDir, initial)); err != nil {
					t.Errorf("initial file %s: %v", initial, err)
				}
			}

			prompt := strings.ToLower(scenario.Prompt)
			for _, keyword := range configKeywords {
				if strings.Contains(prompt, keyword) {
					t.Errorf("prompt leaks config key %q; describe the business situation instead", keyword)
				}
			}
		})
	}
}

// TestEvalRepairStartingConfigsAreBroken proves a repair scenario is a real
// repair: the config the agent starts with must fail a gate or produce a
// different outcome than the answer key. Otherwise an agent that does nothing
// would pass.
func TestEvalRepairStartingConfigsAreBroken(t *testing.T) {
	repairs := 0
	for name, scenario := range loadScenarios(t) {
		isRepair := false
		for _, tag := range scenario.Tags {
			isRepair = isRepair || tag == "repair"
		}
		if !isRepair {
			continue
		}
		repairs++
		t.Run(name, func(t *testing.T) {
			scenarioDir := filepath.Join(evalsDir, name)
			// A runner copies initial_files to the same relative path inside the
			// workspace, so reconify.yaml lands at the workspace root.
			var starting string
			for _, initial := range scenario.InitialFiles {
				if filepath.Clean(initial) == "reconify.yaml" {
					starting = filepath.Join(scenarioDir, initial)
				}
			}
			if starting == "" {
				t.Fatal("repair scenario must ship a starting reconify.yaml among initial_files")
			}
			workDir := materialize(t, scenarioDir, starting)
			result, gate := runScenario(t, workDir, scenario.Pair)
			if gate != "" {
				return
			}
			if got := assertionsOf(t, result); got == scenario.Assertions {
				t.Errorf("the starting config already reproduces the expected summary %+v, so nothing needs repairing", got)
			}
		})
	}
	if repairs == 0 {
		t.Fatal("no repair scenarios found")
	}
}
