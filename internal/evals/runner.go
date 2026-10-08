// Package evals runs the checked-in Engine agent corpus against local coding
// agent CLIs. It only starts external agents when reconify-eval is explicitly run.
package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reconifyhq/reconify/schemas"
)

const reportSchemaV1 = "reconify.engine.eval-report.v1"

// Options configures one explicit evaluator run.
type Options struct {
	CorpusDir, SkillsDir, ReconifyPath string
	Agents, ScenarioIDs                []string
	Trials                             int
	Timeout                            time.Duration
	LookPath                           func(string) (string, error)
	ArtifactDir                        string
	MaxParallel                        int
	ModelArguments                     map[string]string
	// Tags restricts the run to scenarios carrying at least one tag.
	Tags []string
	// Hooks installs the packaged agent hooks (<skills>/.hooks) into each
	// trial workspace, so a hooks arm can be measured against a skills arm.
	Hooks bool
}

// Report is the aggregate machine-readable evaluator output.
type Report struct {
	Schema      string          `json:"schema"`
	Agents      []AgentReport   `json:"agents"`
	Skipped     []SkippedAgent  `json:"skipped,omitempty"`
	Experiment  *Experiment     `json:"experiment,omitempty"`
	Variants    []VariantReport `json:"variants,omitempty"`
	Comparisons []Comparison    `json:"comparisons,omitempty"`
	Verdict     *Verdict        `json:"verdict,omitempty"`
}

// Experiment and related fields are additive report-v1 provenance for release runs.
type Experiment struct {
	Seed            int64    `json:"seed"`
	PromptVersion   string   `json:"prompt_version"`
	CorpusDigest    string   `json:"corpus_digest,omitempty"`
	CandidateDigest string   `json:"candidate_digest,omitempty"`
	BaselineVersion string   `json:"baseline_version,omitempty"`
	MaxParallel     int      `json:"max_parallel,omitempty"`
	ModelArguments  []string `json:"model_arguments,omitempty"`
	OS              string   `json:"os,omitempty"`
	Arch            string   `json:"arch,omitempty"`
}

// VariantReport groups one arm's per-agent results in a release matrix.
type VariantReport struct {
	Name   string        `json:"name"`
	Agents []AgentReport `json:"agents"`
}

// Comparison records one arm's task-success rate relative to another arm.
type Comparison struct {
	Variant  string  `json:"variant"`
	Against  string  `json:"against"`
	Passed   int     `json:"passed"`
	Total    int     `json:"total"`
	PassRate float64 `json:"pass_rate"`
	Delta    float64 `json:"delta"`
}

// Verdict is the release gate's overall outcome for a matrix.
type Verdict struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// SkippedAgent records an unavailable agent from an `--agent all` run.
type SkippedAgent struct {
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
}

// AgentReport groups results from one adapter.
type AgentReport struct {
	Agent     string           `json:"agent"`
	Scenarios []ScenarioReport `json:"scenarios"`
}

// ScenarioReport contains every trial for one corpus scenario.
type ScenarioReport struct {
	ID             string        `json:"id"`
	Trials         []TrialReport `json:"trials"`
	Classification Metrics       `json:"classification"`
	ExactResult    Metrics       `json:"exact_result"`
}

// Metrics reports capability and reliability across a scenario's trials.
type Metrics struct {
	PassAt1         bool    `json:"pass_at_1"`
	PassAll         bool    `json:"pass_all"`
	Passed          int     `json:"passed"`
	Total           int     `json:"total"`
	PassRate        float64 `json:"pass_rate"`
	PassAny         bool    `json:"pass_any"`
	ConfidenceLower float64 `json:"confidence_lower"`
	ConfidenceUpper float64 `json:"confidence_upper"`
}

// TrialReport records observable workflow evidence for one agent attempt.
type TrialReport struct {
	Trial           int      `json:"trial"`
	Discovery       bool     `json:"discovery"`
	Configuration   bool     `json:"configuration"`
	Execution       bool     `json:"execution"`
	Classification  bool     `json:"classification"`
	Explanation     bool     `json:"explanation"`
	ExactResult     bool     `json:"exact_result"`
	AssertionsMatch bool     `json:"assertions_match"`
	Commands        []string `json:"commands"`
	AgentOutput     string   `json:"agent_output,omitempty"`
	Error           string   `json:"error,omitempty"`
	ArtifactPath    string   `json:"artifact_path,omitempty"`
	// Additive harness evidence. Absent values mean the evidence was not
	// observable for this agent or run, never zero.
	Trace      []TraceEntry   `json:"trace,omitempty"`
	Usage      *Usage         `json:"usage,omitempty"`
	Efficiency *Efficiency    `json:"efficiency,omitempty"`
	Grades     []Grade        `json:"grades,omitempty"`
	Failure    *FailureReport `json:"failure,omitempty"`
}

// TraceEntry is one Engine invocation recorded by the workspace wrapper.
type TraceEntry struct {
	Argv           []string `json:"argv"`
	ExitCode       int      `json:"exit_code"`
	DurationMS     int64    `json:"duration_ms"`
	DiagnosticCode string   `json:"diagnostic_code,omitempty"`
}

// Usage is agent-reported resource consumption. Each pointer is nil when the
// agent CLI does not expose that value.
type Usage struct {
	Turns        *int     `json:"turns,omitempty"`
	InputTokens  *int     `json:"input_tokens,omitempty"`
	OutputTokens *int     `json:"output_tokens,omitempty"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
	WallMS       int64    `json:"wall_ms"`
}

// Efficiency summarizes the trace into workflow-cost signals.
type Efficiency struct {
	EngineCalls            int  `json:"engine_calls"`
	FailedCalls            int  `json:"failed_calls"`
	UsageErrors            int  `json:"usage_errors"`
	CallsToFirstValidation *int `json:"calls_to_first_valid_config,omitempty"`
	Recovered              bool `json:"recovered"`
}

// Grade is one grader's verdict with the evidence that produced it.
type Grade struct {
	Name     string `json:"name"`
	Pass     bool   `json:"pass"`
	Gating   bool   `json:"gating"`
	Evidence string `json:"evidence,omitempty"`
}

// FailureReport is the deterministic root-cause label for a failed trial.
type FailureReport struct {
	Label    string `json:"label"`
	Evidence string `json:"evidence"`
}

type scenario struct {
	ID, Prompt, ExpectedResult, ExpectedExplanation, Pair, Dir string
	ReferenceConfig                                            string
	Inputs                                                     []string
	InitialFiles                                               []string
	Assertions                                                 schemas.EvalAssertions
	Tags, DecisionKeywords                                     []string
}

// Run evaluates selected local agents without making ordinary CI tests invoke them.
func Run(ctx context.Context, options Options) (Report, error) {
	if options.ReconifyPath == "" {
		return Report{}, errors.New("--reconify is required")
	}
	if info, err := os.Stat(options.ReconifyPath); err != nil || info.IsDir() {
		return Report{}, fmt.Errorf("reconify binary %q is not an executable file", options.ReconifyPath)
	}
	if options.Trials <= 0 {
		return Report{}, errors.New("--trials must be positive")
	}
	if len(options.Agents) == 0 {
		return Report{}, errors.New("--agent is required")
	}
	if options.Timeout <= 0 {
		options.Timeout = 10 * time.Minute
	}
	if options.LookPath == nil {
		options.LookPath = exec.LookPath
	}
	agents, skipped, err := resolveAgents(options.Agents, options.LookPath)
	if err != nil {
		return Report{}, err
	}
	scenarios, err := loadScenarios(options.CorpusDir, options.ScenarioIDs)
	if err != nil {
		return Report{}, err
	}
	if scenarios = filterByTags(scenarios, options.Tags); len(scenarios) == 0 {
		return Report{}, fmt.Errorf("no scenarios carry any of the tags %q", options.Tags)
	}
	report := Report{Schema: reportSchemaV1, Skipped: skipped}
	for _, agent := range agents {
		current := AgentReport{Agent: string(agent)}
		for _, item := range scenarios {
			entry := ScenarioReport{ID: item.ID, Trials: make([]TrialReport, options.Trials)}
			var group sync.WaitGroup
			var limit chan struct{}
			if options.MaxParallel > 0 {
				limit = make(chan struct{}, options.MaxParallel)
			}
			for trial := 1; trial <= options.Trials; trial++ {
				group.Add(1)
				go func(index int) {
					defer group.Done()
					if limit != nil {
						limit <- struct{}{}
						defer func() { <-limit }()
					}
					trialCtx, cancel := context.WithTimeout(ctx, options.Timeout)
					entry.Trials[index-1] = runTrial(trialCtx, options, agent, item, index)
					cancel()
				}(trial)
			}
			group.Wait()
			entry.Classification = metrics(entry.Trials, func(result TrialReport) bool { return result.Classification })
			entry.ExactResult = metrics(entry.Trials, func(result TrialReport) bool { return result.ExactResult })
			current.Scenarios = append(current.Scenarios, entry)
		}
		report.Agents = append(report.Agents, current)
	}
	return report, nil
}

func metrics(trials []TrialReport, passed func(TrialReport) bool) Metrics {
	result := Metrics{PassAll: len(trials) > 0, Total: len(trials)}
	if len(trials) > 0 {
		result.PassAt1 = passed(trials[0])
	}
	for _, trial := range trials {
		if passed(trial) {
			result.Passed++
		} else {
			result.PassAll = false
		}
	}
	result.PassRate = rate(result.Passed, result.Total)
	result.PassAny = result.Passed > 0
	result.ConfidenceLower, result.ConfidenceUpper = wilson(result.Passed, result.Total)
	return result
}

func rate(passed, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(passed) / float64(total)
}

func wilson(passed, total int) (float64, float64) {
	if total == 0 {
		return 0, 0
	}
	p := float64(passed) / float64(total)
	z := 1.959963984540054
	denom := 1 + z*z/float64(total)
	centre := (p + z*z/(2*float64(total))) / denom
	half := z * math.Sqrt((p*(1-p)+z*z/(4*float64(total)))/float64(total)) / denom
	return math.Max(0, centre-half), math.Min(1, centre+half)
}

func resolveAgents(values []string, lookPath func(string) (string, error)) ([]Agent, []SkippedAgent, error) {
	all, requested := false, map[Agent]bool{}
	for _, value := range values {
		if value == "all" {
			all = true
			continue
		}
		agent := Agent(value)
		if !containsAgent(supportedAgents(), agent) {
			return nil, nil, fmt.Errorf("unsupported agent %q", value)
		}
		requested[agent] = true
	}
	if all {
		for _, agent := range supportedAgents() {
			requested[agent] = true
		}
	}
	if len(requested) == 0 {
		return nil, nil, errors.New("--agent must name an agent or all")
	}
	var available []Agent
	var skipped []SkippedAgent
	for _, agent := range supportedAgents() {
		if !requested[agent] {
			continue
		}
		if _, err := lookPath(string(agent)); err != nil {
			if all {
				skipped = append(skipped, SkippedAgent{Agent: string(agent), Reason: "not installed or not on PATH"})
				continue
			}
			return nil, nil, fmt.Errorf("selected agent %q is unavailable: %w", agent, err)
		}
		available = append(available, agent)
	}
	return available, skipped, nil
}

func containsAgent(agents []Agent, target Agent) bool {
	for _, agent := range agents {
		if agent == target {
			return true
		}
	}
	return false
}

// filterByTags keeps scenarios carrying at least one requested tag; no tags keeps all.
func filterByTags(scenarios []scenario, tags []string) []scenario {
	if len(tags) == 0 {
		return scenarios
	}
	var kept []scenario
	for _, item := range scenarios {
		for _, tag := range tags {
			if slices.Contains(item.Tags, tag) {
				kept = append(kept, item)
				break
			}
		}
	}
	return kept
}

func loadScenarios(corpusDir string, only []string) ([]scenario, error) {
	if corpusDir == "" {
		corpusDir = "evals"
	}
	wanted := map[string]bool{}
	for _, id := range only {
		wanted[id] = true
	}
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		return nil, fmt.Errorf("read corpus: %w", err)
	}
	var found []scenario
	for _, entry := range entries {
		if !entry.IsDir() || (len(wanted) > 0 && !wanted[entry.Name()]) {
			continue
		}
		path := filepath.Join(corpusDir, entry.Name(), "scenario.json")
		data, err := os.ReadFile(path) // #nosec G304 -- checked-in corpus path.
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var header struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if header.Schema == schemas.EvalScenarioSchemaV1 {
			var document schemas.EvalScenario
			if err := json.Unmarshal(data, &document); err != nil {
				return nil, fmt.Errorf("parse %s: %w", path, err)
			}
			found = append(found, scenario{ID: document.ID, Prompt: document.Prompt, Inputs: document.Inputs, ExpectedResult: document.ExpectedResult, ReferenceConfig: document.ReferenceConfig, Pair: document.Pair, Assertions: document.Assertions, Dir: filepath.Join(corpusDir, entry.Name())})
			continue
		}
		if header.Schema != schemas.EvalScenarioSchemaV2 {
			return nil, fmt.Errorf("scenario %s has unsupported schema %q", entry.Name(), header.Schema)
		}
		var document schemas.EvalScenarioV2
		if err := json.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		found = append(found, scenario{ID: document.ID, Prompt: document.Prompt, Inputs: document.Inputs, InitialFiles: document.InitialFiles, ExpectedResult: document.ExpectedResult, ExpectedExplanation: document.ExpectedExplanation, ReferenceConfig: document.ReferenceConfig, Pair: document.Pair, Assertions: document.Assertions, Tags: document.Tags, DecisionKeywords: document.DecisionKeywords, Dir: filepath.Join(corpusDir, entry.Name())})
	}
	if len(found) == 0 {
		return nil, errors.New("no requested scenarios found")
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	return found, nil
}

func runTrial(ctx context.Context, options Options, agent Agent, item scenario, trial int) (report TrialReport) {
	report.Trial = trial
	workspace, cleanup, err := materialize(options, item)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	defer func() {
		if options.ArtifactDir == "" {
			cleanup()
			return
		}
		destination := filepath.Join(options.ArtifactDir, fmt.Sprintf("%s-%s-%d", agent, item.ID, trial))
		if err := retainWorkspace(workspace, destination); err != nil {
			if report.Error == "" {
				report.Error = fmt.Sprintf("retain artifacts: %v", err)
			}
			cleanup()
			return
		}
		report.ArtifactPath = destination
	}()
	started := time.Now()
	output, agentErr := runAgent(ctx, agent, workspace, taskPrompt(item), options.ReconifyPath, options.ModelArguments[string(agent)])
	wall := time.Since(started)
	report.AgentOutput = boundAgentOutput(output)
	report.Usage = parseUsage(agent, output, wall.Milliseconds())
	if agentErr != nil {
		report.Error = agentErr.Error()
	}
	commands, readErr := readCommands(filepath.Join(workspace, ".reconify-eval-commands.log"))
	if readErr != nil && report.Error == "" {
		report.Error = readErr.Error()
	}
	report.Commands = commands
	report.Trace = readTrace(filepath.Join(workspace, traceFileName))
	report.Efficiency = computeEfficiency(report.Trace)

	evidence := trialEvidence{commands: commands, trace: report.Trace, messages: agentMessages(agent, output), decisionKeywords: item.DecisionKeywords, hasExplanationKey: item.ExpectedExplanation != "", assertions: item.Assertions}
	input := classifyInput{agentErr: agentErr, item: item, workspace: workspace}
	config := filepath.Join(workspace, "reconify.yaml")
	evidence.configPresent = fileExists(config)
	input.configPresent = evidence.configPresent
	if !evidence.configPresent {
		if report.Error == "" {
			report.Error = "agent did not create reconify.yaml"
		}
	} else {
		verifyEngine(ctx, options.ReconifyPath, workspace, item, &evidence, &input, &report)
	}
	report.Grades = gradeTrial(evidence)
	report.Discovery = gradePass(report.Grades, gradeDiscovery)
	report.Configuration = gradePass(report.Grades, gradeConfiguration)
	report.Execution = gradePass(report.Grades, gradeExecution)
	report.Classification = gradePass(report.Grades, gradeClassification)
	report.ExactResult = gradePass(report.Grades, gradeExactResult)
	report.AssertionsMatch = gradePass(report.Grades, gradeAssertionsMatch)
	report.Explanation = gradePass(report.Grades, gradeExplanation)
	if !report.Classification {
		input.ctxErr = ctx.Err()
		report.Failure = classifyFailure(input)
	}
	return report
}

// verifyEngine re-runs the agent's config through the evaluator's own Engine
// calls, which never carry the trace variable, and records what graders and the
// failure classifier need.
func verifyEngine(ctx context.Context, binary, workspace string, item scenario, evidence *trialEvidence, input *classifyInput, report *TrialReport) {
	config := filepath.Join(workspace, "reconify.yaml")
	stderr, err := runEngine(ctx, binary, workspace, "--error-format", "json", "config", "validate", "--config", config)
	evidence.configValidated, input.configValid = err == nil, err == nil
	if err != nil {
		diag := parseDiagnostic(stderr)
		evidence.configDetail = diag.detail(err.Error())
		input.configDetail = evidence.configDetail
	}
	verified := filepath.Join(workspace, "verified-result.json")
	stderr, err = runEngine(ctx, binary, workspace, "--error-format", "json", "reconcile", "--config", config, "--pair", item.Pair, "--format", "json", "--deterministic", "--out", verified)
	if err != nil {
		input.verifyDiag = parseDiagnostic(stderr)
		evidence.verifyDetail = input.verifyDiag.detail(err.Error())
		input.verifyDetail = evidence.verifyDetail
		if report.Error == "" {
			report.Error = fmt.Sprintf("verify reconciliation: %v", err)
		}
		return
	}
	evidence.verified, input.verified = true, true
	evidence.artifact = resolveArtifact(workspace, resultArtifactNames) != ""
	input.artifact = evidence.artifact
	evidence.actual, _ = os.ReadFile(verified)                                       // #nosec G304 -- evaluator workspace.
	evidence.expected, _ = os.ReadFile(filepath.Join(item.Dir, item.ExpectedResult)) // #nosec G304 -- checked-in fixture.
	if item.ExpectedExplanation == "" {
		return
	}
	explanationPath := resolveArtifact(workspace, explanationArtifactNames)
	if explanationPath == "" {
		return
	}
	agentExplanation, err := os.ReadFile(explanationPath) // #nosec G304 -- evaluator workspace.
	if err != nil {
		return
	}
	evidence.explanationFound = true
	expectedExplanation, readErr := os.ReadFile(filepath.Join(item.Dir, item.ExpectedExplanation)) // #nosec G304 -- checked-in fixture.
	evidence.explanationEqual = readErr == nil && semanticExplanationEqual(agentExplanation, expectedExplanation)
}

const maxAgentOutputBytes = 40000

func boundAgentOutput(output []byte) string {
	if len(output) <= maxAgentOutputBytes {
		return string(output)
	}
	return string(output[:maxAgentOutputBytes]) + "\n[agent output truncated]"
}

func assertionsMatch(data []byte, expected schemas.EvalAssertions) bool {
	var document struct {
		Summary schemas.EvalAssertions `json:"summary"`
	}
	return json.Unmarshal(data, &document) == nil && document.Summary == expected
}

// semanticResultEqual compares outcome events while deliberately ignoring
// generated IDs, source labels, raw parser fields, ordering, and index metadata.
func semanticResultEqual(actual, expected []byte) bool {
	var left, right map[string]any
	if json.Unmarshal(actual, &left) != nil || json.Unmarshal(expected, &right) != nil {
		return false
	}
	return canonicalSemantic(left) == canonicalSemantic(right)
}

// semanticExplanationEqual compares explanations with the same rules as
// results: agent-chosen source names and descriptive fields are ignored, and
// lists compare as multisets.
func semanticExplanationEqual(actual, expected []byte) bool {
	var left, right any
	if json.Unmarshal(actual, &left) != nil || json.Unmarshal(expected, &right) != nil {
		return false
	}
	a, _ := json.Marshal(normalizeSemantic(left))
	b, _ := json.Marshal(normalizeSemantic(right))
	return bytes.Equal(a, b)
}

// resultMetadataKeys describe how a run was produced rather than what it
// found, so semantic grading ignores them. Every other top-level key is an
// outcome section and is compared, which keeps new event kinds graded.
var resultMetadataKeys = map[string]bool{
	"schema": true, "summary": true, "index_selection": true, "run_info": true,
	"pair": true, "left_source": true, "right_source": true,
}

// descriptiveKeys never change which rows reconcile: generated ids,
// agent-chosen source names, raw input echoes, and optional descriptive
// mappings such as name.
var descriptiveKeys = map[string]bool{
	"id": true, "source": true, "raw": true, "index_selection": true, "run_id": true,
	"name": true, "group_key": true,
}

func canonicalSemantic(document map[string]any) string {
	selected := map[string]any{}
	for key, value := range document {
		if resultMetadataKeys[key] || isEmptySection(value) {
			continue
		}
		if key == "by_source" {
			// Per-counterpart counters are keyed by agent-chosen source names;
			// compare them as an unordered set of counter objects.
			if sources, ok := value.(map[string]any); ok {
				values := make([]any, 0, len(sources))
				for _, counters := range sources {
					values = append(values, counters)
				}
				value = values
			}
		}
		selected[key] = normalizeSemantic(value)
	}
	data, _ := json.Marshal(selected)
	return string(data)
}

// isEmptySection treats null, empty arrays, and empty objects as an absent
// section, so formats that omit empty sections compare equal.
func isEmptySection(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

func normalizeSemantic(value any) any {
	switch typed := value.(type) {
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			data, _ := json.Marshal(normalizeSemantic(item))
			items = append(items, string(data))
		}
		sort.Strings(items)
		result := make([]any, 0, len(items))
		for _, item := range items {
			var decoded any
			_ = json.Unmarshal([]byte(item), &decoded)
			result = append(result, decoded)
		}
		return result
	case map[string]any:
		result := map[string]any{}
		for key, item := range typed {
			if descriptiveKeys[key] {
				continue
			}
			result[key] = normalizeSemantic(item)
		}
		return result
	default:
		return value
	}
}

func materialize(options Options, item scenario) (string, func(), error) {
	workspace, err := os.MkdirTemp("", "reconify-eval-")
	if err != nil {
		return "", nil, fmt.Errorf("create workspace: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(workspace) }
	for _, input := range item.Inputs {
		if err := copyFile(filepath.Join(item.Dir, input), filepath.Join(workspace, input)); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	for _, initial := range item.InitialFiles {
		if filepath.IsAbs(initial) || strings.HasPrefix(filepath.Clean(initial), ".."+string(os.PathSeparator)) || filepath.Clean(initial) == ".." {
			cleanup()
			return "", nil, fmt.Errorf("initial file path escapes scenario: %q", initial)
		}
		if err := copyFile(filepath.Join(item.Dir, initial), filepath.Join(workspace, filepath.Clean(initial))); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	for _, dir := range []string{".agents", ".claude", ".codex"} {
		if err := copyTree(filepath.Join(options.SkillsDir, dir), filepath.Join(workspace, dir, "skills")); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanup()
			return "", nil, err
		}
	}
	if options.Hooks {
		hooks := filepath.Join(options.SkillsDir, ".hooks", "claude")
		if err := copyTreeMode(hooks, filepath.Join(workspace, ".claude")); err != nil {
			cleanup()
			if errors.Is(err, os.ErrNotExist) {
				return "", nil, errors.New("hooks arm requested but package has no .hooks/claude")
			}
			return "", nil, err
		}
	}
	if err := writeWrapper(workspace); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := exec.Command("git", "init", "--quiet", workspace).Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("initialize temporary git workspace: %w", err)
	} // #nosec G204 -- fixed command.
	return workspace, cleanup, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src) // #nosec G304 -- checked-in fixture path.
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600) // #nosec G703 -- evaluator workspace.
}

func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from, to := filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyTree(from, to); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(from, to); err != nil {
			return err
		}
	}
	return nil
}

// retainWorkspace moves a trial workspace into the artifact directory. A plain
// rename fails across filesystems (for example /tmp versus the repository), so
// it falls back to a mode-preserving copy followed by removing the source.
func retainWorkspace(workspace, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return err
	}
	_ = os.RemoveAll(destination) // a stale trial from an earlier run
	if os.Rename(workspace, destination) == nil {
		return nil
	}
	if err := copyTreeMode(workspace, destination); err != nil {
		_ = os.RemoveAll(destination)
		return err
	}
	return os.RemoveAll(workspace)
}

// copyTreeMode copies a tree like copyTree but keeps each file's permission
// bits, so hook scripts stay executable.
func copyTreeMode(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from, to := filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := os.MkdirAll(to, 0o750); err != nil {
				return err
			}
			if err := copyTreeMode(from, to); err != nil {
				return err
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(from) // #nosec G304 -- packaged hook or retained workspace path.
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(to, data, info.Mode().Perm()|0o600); err != nil { // #nosec G306,G703 -- hook scripts must keep exec bits in the evaluator workspace.
			return err
		}
	}
	return nil
}

func writeWrapper(workspace string) error {
	bin := filepath.Join(workspace, ".bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		return err
	}
	data := []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RECONIFY_EVAL_LOG\"\nexec \"$RECONIFY_EVAL_BINARY\" \"$@\"\n")
	return os.WriteFile(filepath.Join(bin, "reconify"), data, 0o700) // #nosec G306,G703 -- executable wrapper in evaluator workspace.
}

func taskPrompt(item scenario) string {
	return fmt.Sprintf("%s\n\nWork only in this temporary workspace. Solve the business reconciliation problem described above for pair %q. Leave a valid configuration, the resulting reconciliation artifact, and a concise explanation of that result at the workspace root. You may inspect the available files and installed documentation or tools as needed. Verify the configuration and result before finishing; recover from errors and do not stop at a partial artifact.", item.Prompt, item.Pair)
}

// runEngine runs the Engine for the evaluator's own verification and returns
// stderr. The trace variable is stripped so these calls never enter the agent trace.
func runEngine(ctx context.Context, binary, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- binary is explicit CLI input.
	cmd.Dir = dir
	cmd.Env = envWithout(os.Environ(), traceEnv)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.Bytes(), err
}

func readCommands(path string) ([]string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- evaluator workspace.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' }), nil
}

func containsCommand(commands []string, fragment string) bool {
	for _, command := range commands {
		if strings.Contains(command, fragment) {
			return true
		}
	}
	return false
}
func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

// Skills from version 0.6.0 onward name the retained artifacts result.json and
// explanation.json. Published baseline packages still instruct agents to write
// the evaluator-specific agent-*.json names, so both are accepted here and the
// release gate keeps comparing arms on equal terms.
var (
	resultArtifactNames      = []string{"result.json", "agent-result.json"}
	explanationArtifactNames = []string{"explanation.json", "agent-explanation.json"}
)

// resolveArtifact returns the first candidate that exists in workspace, or "".
func resolveArtifact(workspace string, candidates []string) string {
	for _, name := range candidates {
		path := filepath.Join(workspace, name)
		if fileExists(path) {
			return path
		}
	}
	return ""
}

// WriteReport writes formatted JSON to stdout or an explicit output path.
func WriteReport(report Report, output string, stdout io.Writer) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if output == "" {
		_, err = stdout.Write(data)
		return err
	}
	return os.WriteFile(output, data, 0o600) // #nosec G304 -- explicit output flag.
}
