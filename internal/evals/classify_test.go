package evals

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cfgOptions parameterizes a one-pair config so tests can vary one field at a time.
type cfgOptions struct {
	amountCol, layout, window, passes, groupCol, dupPolicy, decimal string
	multiplier, tolerance                                           int
}

func baseConfig() cfgOptions {
	return cfgOptions{amountCol: "amount", layout: "2006-01-02", window: "1d", multiplier: 100, tolerance: 200}
}

func (o cfgOptions) yaml() string {
	parser := func(file string) string {
		text := fmt.Sprintf(`    file_pattern: "inputs/%s.csv"
    parser:
      type: csv
      date_col: date
      date_layout: %q
      amount_col: %s
      ref_col: reference
      multiplier: %d
`, file, o.layout, o.amountCol, o.multiplier)
		if o.groupCol != "" {
			text += "      group_col: " + o.groupCol + "\n"
		}
		if o.dupPolicy != "" {
			text += "      duplicate_policy: " + o.dupPolicy + "\n"
		}
		if o.decimal != "" {
			text += fmt.Sprintf("      decimal: %q\n", o.decimal)
		}
		return text
	}
	text := "version: 1\ntimezone: UTC\nsources:\n  left:\n" + parser("left") + "  right:\n" + parser("right")
	text += fmt.Sprintf("pairs:\n  left_vs_right:\n    left: left\n    right: right\n    date_window: %q\n    amount_tolerance_minor: %d\n", o.window, o.tolerance)
	if o.passes != "" {
		text += "    passes:\n"
		for _, pass := range strings.Fields(o.passes) {
			text += "      - type: " + pass + "\n"
		}
	}
	return text
}

// classifyFixture lays out a scenario directory with a reference config and a workspace.
func classifyFixture(t *testing.T, agent *cfgOptions) classifyInput {
	t.Helper()
	root := t.TempDir()
	scenarioDir, workspace := filepath.Join(root, "scenario"), filepath.Join(root, "workspace")
	for _, dir := range []string{filepath.Join(scenarioDir, "reference"), workspace} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	reference := baseConfig()
	write := func(path, text string) {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(scenarioDir, "reference", "reconify.yaml"), reference.yaml())
	input := classifyInput{
		item:      scenario{Dir: scenarioDir, ReferenceConfig: "reference/reconify.yaml", Pair: "left_vs_right"},
		workspace: workspace, verified: true, artifact: true,
	}
	if agent != nil {
		write(filepath.Join(workspace, "reconify.yaml"), agent.yaml())
		input.configPresent, input.configValid = true, true
	}
	return input
}

func TestClassifyFailureConfigDifferences(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		mutate   func(*cfgOptions)
		label    string
		evidence string
	}{
		{"amount column", func(o *cfgOptions) { o.amountCol = "gross" }, labelWrongAmountMapping, `left.amount_col "gross" vs reference "amount"`},
		{"multiplier", func(o *cfgOptions) { o.multiplier = 1 }, labelWrongAmountMapping, `left.multiplier "1" vs reference "100"`},
		{"decimal separator", func(o *cfgOptions) { o.decimal = "," }, labelWrongAmountMapping, `left.decimal "," vs reference "."`},
		{"date layout", func(o *cfgOptions) { o.layout = "02/01/2006" }, labelWrongDateLayout, `left.date_layout "02/01/2006" vs reference "2006-01-02"`},
		{"tolerance", func(o *cfgOptions) { o.tolerance = 100000 }, labelWrongTolerance, "pair.amount_tolerance_minor 100000 vs reference 200"},
		{"date window", func(o *cfgOptions) { o.window = "10d" }, labelWrongDateWindow, `pair.date_window "10d" vs reference "1d"`},
		{"group column", func(o *cfgOptions) { o.groupCol = "invoice" }, labelWrongMatchingStrategy, `left.group_col "invoice" vs reference "reference"`},
		{"duplicate policy", func(o *cfgOptions) { o.dupPolicy = "merge" }, labelWrongMatchingStrategy, `left.duplicate_policy "merge" vs reference "flag"`},
		{"passes", func(o *cfgOptions) { o.passes = "reference_one_to_one one_to_many" }, labelWrongMatchingStrategy, "pair.passes [reference_one_to_one:reference one_to_many:reference] vs reference [reference_one_to_one:reference]"},
		{"first label in order wins", func(o *cfgOptions) { o.layout, o.tolerance, o.passes = "x", 0, "many_to_many" }, labelWrongDateLayout, `left.date_layout "x"`},
		{"equivalent windows and explicit passes", func(o *cfgOptions) { o.passes = "reference_one_to_one" }, labelWrongClassification, "matches the reference"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			agent := baseConfig()
			testCase.mutate(&agent)
			failure := classifyFailure(classifyFixture(t, &agent))
			if failure.Label != testCase.label || !strings.Contains(failure.Evidence, testCase.evidence) {
				t.Fatalf("failure = %+v, want %s containing %q", failure, testCase.label, testCase.evidence)
			}
		})
	}
}

func TestClassifyFailureEarlyLabels(t *testing.T) {
	agent := baseConfig()
	for _, testCase := range []struct {
		name   string
		build  func() classifyInput
		label  string
		detail string
	}{
		{"timeout beats everything", func() classifyInput {
			in := classifyFixture(t, nil)
			in.ctxErr, in.agentErr = context.DeadlineExceeded, errors.New("claude: killed")
			return in
		}, labelTimeout, "timeout"},
		{"agent error without config", func() classifyInput {
			in := classifyFixture(t, nil)
			in.agentErr = errors.New("claude: exit status 1")
			return in
		}, labelAgentError, "exit status 1"},
		{"missing config", func() classifyInput { return classifyFixture(t, nil) }, labelMissingConfig, "reconify.yaml"},
		{"config invalid", func() classifyInput {
			in := classifyFixture(t, &agent)
			in.configValid, in.configDetail = false, "sources.left.parser.multiplier: required field is missing"
			return in
		}, labelConfigInvalid, "multiplier"},
		{"file pattern by diagnostic code", func() classifyInput {
			in := classifyFixture(t, &agent)
			in.verified = false
			in.verifyDiag = diagnosticInfo{Code: "INPUT_UNREADABLE", Message: "cannot read left"}
			return in
		}, labelFilePatternUnresolved, "cannot read left"},
		{"file pattern by message", func() classifyInput {
			in := classifyFixture(t, &agent)
			in.verified = false
			in.verifyDiag = diagnosticInfo{Code: "INTERNAL_ERROR", Message: `no files match pattern "/w/inputs/left.csv"`}
			return in
		}, labelFilePatternUnresolved, "no files match"},
		{"agent error with config continues", func() classifyInput {
			in := classifyFixture(t, &agent)
			in.agentErr = errors.New("claude: exit status 1")
			return in
		}, labelWrongClassification, ""},
		{"missing artifact when config matches", func() classifyInput {
			in := classifyFixture(t, &agent)
			in.artifact = false
			return in
		}, labelMissingResultArtifact, "result.json"},
		{"run failure with matching config", func() classifyInput {
			in := classifyFixture(t, &agent)
			in.verified = false
			in.verifyDiag = diagnosticInfo{Message: "column \"amt\" not found"}
			return in
		}, labelWrongClassification, `column "amt"`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			failure := classifyFailure(testCase.build())
			if failure.Label != testCase.label || !strings.Contains(failure.Evidence, testCase.detail) {
				t.Fatalf("failure = %+v, want %s containing %q", failure, testCase.label, testCase.detail)
			}
		})
	}
}

func TestClassifyFailureComparesSourcesByPairRole(t *testing.T) {
	input := classifyFixture(t, nil)
	renamed := strings.NewReplacer("\n  left:\n", "\n  bank:\n", "\n  right:\n", "\n  ledger:\n", "    left: left\n    right: right\n", "    left: bank\n    right: ledger\n").Replace(baseConfig().yaml())
	renamed = strings.Replace(renamed, "multiplier: 100", "multiplier: 1", 1)
	if err := os.WriteFile(filepath.Join(input.workspace, "reconify.yaml"), []byte(renamed), 0o600); err != nil {
		t.Fatal(err)
	}
	input.configPresent, input.configValid = true, true
	if failure := classifyFailure(input); failure.Label != labelWrongAmountMapping {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestParseDiagnostic(t *testing.T) {
	stderr := []byte("noise before\n" + `{"error":"invalid","code":"config_error","ok":false,"schema":"reconify.engine.diagnostic.v1","diagnostic":{"code":"CONFIG_INVALID","category":"config","message":"config is invalid","details":{"errors":[{"path":"sources.left.parser.date_col","message":"required field is missing"},{"message":"no path"}]}}}` + "\n")
	got := parseDiagnostic(stderr)
	if got.Code != "CONFIG_INVALID" || len(got.Errors) != 2 || got.Errors[0] != "sources.left.parser.date_col: required field is missing" || got.Errors[1] != "no path" {
		t.Fatalf("diagnostic = %+v", got)
	}
	if got.detail("x") != got.Errors[0] {
		t.Fatalf("detail = %q", got.detail("x"))
	}
	if empty := parseDiagnostic([]byte("plain failure text")); empty.Code != "" || empty.detail("fallback") != "fallback" {
		t.Fatalf("empty = %+v", empty)
	}
}
