package schemas

import _ "embed"

// EvalScenarioSchemaV1 is the stable identifier for a published Engine agent
// evaluation scenario, the machine-readable contract the cross-agent runner
// (AP-10) consumes.
const EvalScenarioSchemaV1 = "reconify.engine.eval-scenario.v1"

// EvalScenarioSchemaV2 extends the corpus contract with an expected explanation
// artifact. V1 remains supported for third-party fixtures that only grade the
// reconciliation result.
const EvalScenarioSchemaV2 = "reconify.engine.eval-scenario.v2"

// EvalScenario is one graded fixture in the Engine agent evaluation corpus. It
// pairs a natural-language prompt with the inputs an agent is given, the
// reference config a correct answer is expected to behave like, and the
// reconciliation outcome that answer must produce.
//
// Paths are relative to the scenario directory. A runner materializes a
// working directory containing Inputs and the candidate config, placing the
// config at the working-directory root, because `file_pattern` resolves
// relative to the config file rather than the process working directory.
type EvalScenario struct {
	Schema          string         `json:"schema"`
	ID              string         `json:"id"`
	Prompt          string         `json:"prompt"`
	Inputs          []string       `json:"inputs"`
	ReferenceConfig string         `json:"reference_config"`
	ExpectedResult  string         `json:"expected_result"`
	Pair            string         `json:"pair"`
	Assertions      EvalAssertions `json:"assertions"`
	CounterExamples []string       `json:"counter_examples"`
}

// EvalScenarioV2 is a full workflow fixture. Agents must discover the Engine,
// validate their configuration, reconcile deterministically, and explain the
// result they produced. ExpectedExplanation is relative to the scenario
// directory and captures the deterministic explanation answer key.
type EvalScenarioV2 struct {
	Schema string   `json:"schema"`
	ID     string   `json:"id"`
	Prompt string   `json:"prompt"`
	Inputs []string `json:"inputs"`
	// InitialFiles are files copied into the agent workspace before the task.
	// They are useful for repair scenarios, such as an invalid existing config.
	InitialFiles        []string       `json:"initial_files,omitempty"`
	ReferenceConfig     string         `json:"reference_config"`
	ExpectedResult      string         `json:"expected_result"`
	ExpectedExplanation string         `json:"expected_explanation"`
	Pair                string         `json:"pair"`
	Assertions          EvalAssertions `json:"assertions"`
	CounterExamples     []string       `json:"counter_examples"`
	// Tags select scenario tiers such as core, messy, scale, repair, and
	// ask-user. Runners may filter on them; they never change grading.
	Tags []string `json:"tags,omitempty"`
	// DecisionKeywords mark an ask-user scenario. A diagnostic grader records
	// whether the agent's final output surfaces the business decision by
	// mentioning any keyword. It is evidence, not a release gate.
	DecisionKeywords []string `json:"decision_keywords,omitempty"`
}

// EvalAssertions is the subset of reconciliation summary counters that
// characterizes a scenario. A candidate config passes the summary gate when
// every counter here equals the value the run produced.
//
// Zero values are meaningful and always emitted: asserting duplicate_count is
// zero is how a grouped scenario proves it did not raise false duplicates.
type EvalAssertions struct {
	Matched                int `json:"matched"`
	UnmatchedLeft          int `json:"unmatched_left"`
	UnmatchedRight         int `json:"unmatched_right"`
	AmountDiffCount        int `json:"amount_diff_count"`
	TimingDiffCount        int `json:"timing_diff_count"`
	DuplicateCount         int `json:"duplicate_count"`
	GroupedMatchedCount    int `json:"grouped_matched_count"`
	ManyToManyMatchedCount int `json:"many_to_many_matched_count"`
	AmbiguousGroupCount    int `json:"ambiguous_group_count"`
}

//go:generate go run ../cmd/generate-eval-scenario-schema -output reconify.engine.eval-scenario.v1.json

// evalScenarioSchemaV1 is the checked-in schema served by the CLI.
//
//go:embed reconify.engine.eval-scenario.v1.json
var evalScenarioSchemaV1 []byte

//go:embed reconify.engine.eval-scenario.v2.json
var evalScenarioSchemaV2 []byte

// EvalScenarioV1 returns a copy of the published eval scenario schema document.
func EvalScenarioV1() []byte {
	return append([]byte(nil), evalScenarioSchemaV1...)
}

// EvalScenarioV2Schema returns a copy of the published v2 scenario contract.
func EvalScenarioV2Schema() []byte {
	return append([]byte(nil), evalScenarioSchemaV2...)
}
