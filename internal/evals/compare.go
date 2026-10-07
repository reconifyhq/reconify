package evals

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	diffSchemaV1    = "reconify.engine.eval-diff.v1"
	summarySchemaV1 = "reconify.engine.eval-summary.v1"

	// DefaultVariant is the release-matrix arm compared when a report holds
	// variants and the caller does not name one.
	DefaultVariant = "candidate"

	// RegressionDrop is the smallest per-(agent, scenario) pass-rate drop that
	// counts as a regression. It is 0.34 rather than 1/3 so that losing one
	// trial out of three is treated as noise, while losing two is not.
	RegressionDrop = 0.34

	rateEpsilon = 1e-9
)

// RateStat is a pass count with its Wilson confidence interval.
type RateStat struct {
	Passed          int     `json:"passed"`
	Total           int     `json:"total"`
	PassRate        float64 `json:"pass_rate"`
	ConfidenceLower float64 `json:"confidence_lower"`
	ConfidenceUpper float64 `json:"confidence_upper"`
}

func newRateStat(passed, total int) RateStat {
	lower, upper := wilson(passed, total)
	return RateStat{Passed: passed, Total: total, PassRate: rate(passed, total), ConfidenceLower: lower, ConfidenceUpper: upper}
}

// CellDiff is one (agent, scenario) pair present in both reports.
type CellDiff struct {
	Agent    string   `json:"agent"`
	Scenario string   `json:"scenario"`
	Base     RateStat `json:"base"`
	Head     RateStat `json:"head"`
	Delta    float64  `json:"delta"`
}

// OverallDiff compares classification pass rates across every shared trial.
type OverallDiff struct {
	Base  RateStat `json:"base"`
	Head  RateStat `json:"head"`
	Delta float64  `json:"delta"`
	// Significant is true when the two Wilson intervals do not overlap.
	Significant bool `json:"significant"`
}

// LabelDiff compares how many failed trials carry one root-cause label.
type LabelDiff struct {
	Label string `json:"label"`
	Base  int    `json:"base"`
	Head  int    `json:"head"`
	Delta int    `json:"delta"`
}

// MetricDiff compares one per-trial median. Nil means no trial reported it.
type MetricDiff struct {
	Name      string   `json:"name"`
	Base      *float64 `json:"base,omitempty"`
	Head      *float64 `json:"head,omitempty"`
	Delta     *float64 `json:"delta,omitempty"`
	BaseCount int      `json:"base_count"`
	HeadCount int      `json:"head_count"`
}

// RunDiff is the machine-readable result of comparing two reports.
type RunDiff struct {
	Schema      string      `json:"schema"`
	BaseVariant string      `json:"base_variant,omitempty"`
	HeadVariant string      `json:"head_variant,omitempty"`
	Overall     OverallDiff `json:"overall"`
	Cells       []CellDiff  `json:"cells"`
	// NewlyFailing lists shared pairs whose pass rate fell; NewlyPassing those
	// whose pass rate rose. Both are sorted by agent then scenario.
	NewlyFailing []CellDiff `json:"newly_failing"`
	NewlyPassing []CellDiff `json:"newly_passing"`
	// OnlyInBase and OnlyInHead name "agent/scenario" pairs excluded from every
	// aggregate because the other report lacks them.
	OnlyInBase          []string     `json:"only_in_base,omitempty"`
	OnlyInHead          []string     `json:"only_in_head,omitempty"`
	FailureLabels       []LabelDiff  `json:"failure_labels"`
	Metrics             []MetricDiff `json:"metrics"`
	Regression          bool         `json:"regression"`
	RegressionThreshold float64      `json:"regression_threshold"`
}

// Regressed reports whether the head run should fail a CI gate: at least one
// pair got worse by RegressionDrop or more.
func (d RunDiff) Regressed() bool { return len(d.regressions()) > 0 }

func (d RunDiff) regressions() []CellDiff {
	var result []CellDiff
	for _, cell := range d.NewlyFailing {
		if cell.Delta <= -RegressionDrop+rateEpsilon {
			result = append(result, cell)
		}
	}
	return result
}

// CellStat is one (agent, scenario) pass rate in a single-report summary.
type CellStat struct {
	Agent    string `json:"agent"`
	Scenario string `json:"scenario"`
	RateStat
}

// LabelCount is one failure label and how many trials carry it.
type LabelCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// MetricStat is one per-trial median. Nil means no trial reported it.
type MetricStat struct {
	Name   string   `json:"name"`
	Median *float64 `json:"median,omitempty"`
	Count  int      `json:"count"`
}

// Summary is the machine-readable digest of one report.
type Summary struct {
	Schema        string       `json:"schema"`
	Variant       string       `json:"variant,omitempty"`
	Overall       RateStat     `json:"overall"`
	Cells         []CellStat   `json:"cells"`
	FailureLabels []LabelCount `json:"failure_labels"`
	Metrics       []MetricStat `json:"metrics"`
}

type cellKey struct{ agent, scenario string }

func (k cellKey) String() string { return k.agent + "/" + k.scenario }

type cellData struct {
	stat   RateStat
	trials []TrialReport
}

// metricSpec extracts one per-trial value; ok is false when the trial did not
// report it.
type metricSpec struct {
	name    string
	extract func(TrialReport) (float64, bool)
}

var metricSpecs = []metricSpec{
	{"engine_calls", func(t TrialReport) (float64, bool) {
		if t.Efficiency == nil {
			return 0, false
		}
		return float64(t.Efficiency.EngineCalls), true
	}},
	{"failed_calls", func(t TrialReport) (float64, bool) {
		if t.Efficiency == nil {
			return 0, false
		}
		return float64(t.Efficiency.FailedCalls), true
	}},
	{"usage_errors", func(t TrialReport) (float64, bool) {
		if t.Efficiency == nil {
			return 0, false
		}
		return float64(t.Efficiency.UsageErrors), true
	}},
	{"turns", func(t TrialReport) (float64, bool) {
		if t.Usage == nil || t.Usage.Turns == nil {
			return 0, false
		}
		return float64(*t.Usage.Turns), true
	}},
	{"cost_usd", func(t TrialReport) (float64, bool) {
		if t.Usage == nil || t.Usage.CostUSD == nil {
			return 0, false
		}
		return *t.Usage.CostUSD, true
	}},
	{"tokens", func(t TrialReport) (float64, bool) {
		if t.Usage == nil || t.Usage.InputTokens == nil || t.Usage.OutputTokens == nil {
			return 0, false
		}
		return float64(*t.Usage.InputTokens + *t.Usage.OutputTokens), true
	}},
	{"wall_ms", func(t TrialReport) (float64, bool) {
		if t.Usage == nil {
			return 0, false
		}
		return float64(t.Usage.WallMS), true
	}},
}

// LoadReport reads an evaluator report from disk.
func LoadReport(path string) (Report, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- explicit CLI input path.
	if err != nil {
		return Report{}, err
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if report.Schema != reportSchemaV1 {
		return Report{}, fmt.Errorf("%s: unsupported report schema %q", path, report.Schema)
	}
	return report, nil
}

// selectAgents picks the agent results to analyze. Plain run reports ignore
// variant; release reports use the named variant, or DefaultVariant, or their
// only variant.
func selectAgents(report Report, variant string) (string, []AgentReport, error) {
	if len(report.Variants) == 0 {
		return "", report.Agents, nil
	}
	names := make([]string, 0, len(report.Variants))
	for _, candidate := range report.Variants {
		names = append(names, candidate.Name)
	}
	want := variant
	if want == "" {
		want = DefaultVariant
		if len(report.Variants) == 1 {
			want = report.Variants[0].Name
		}
	}
	for _, candidate := range report.Variants {
		if candidate.Name == want {
			return want, candidate.Agents, nil
		}
	}
	return "", nil, fmt.Errorf("report has no variant %q (available: %s)", want, strings.Join(names, ", "))
}

func collectCells(agents []AgentReport) map[cellKey]*cellData {
	cells := map[cellKey]*cellData{}
	for _, agent := range agents {
		for _, item := range agent.Scenarios {
			key := cellKey{agent.Agent, item.ID}
			cell := cells[key]
			if cell == nil {
				cell = &cellData{}
				cells[key] = cell
			}
			cell.trials = append(cell.trials, item.Trials...)
			if len(item.Trials) == 0 {
				// Reports without per-trial detail still carry the aggregate.
				cell.stat = newRateStat(cell.stat.Passed+item.Classification.Passed, cell.stat.Total+item.Classification.Total)
				continue
			}
			passed := 0
			for _, trial := range item.Trials {
				if trial.Classification {
					passed++
				}
			}
			cell.stat = newRateStat(cell.stat.Passed+passed, cell.stat.Total+len(item.Trials))
		}
	}
	return cells
}

func sortedKeys(cells map[cellKey]*cellData) []cellKey {
	keys := make([]cellKey, 0, len(cells))
	for key := range cells {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].agent != keys[j].agent {
			return keys[i].agent < keys[j].agent
		}
		return keys[i].scenario < keys[j].scenario
	})
	return keys
}

type aggregate struct {
	overall RateStat
	labels  map[string]int
	values  map[string][]float64
}

func aggregateCells(cells map[cellKey]*cellData, keys []cellKey) aggregate {
	result := aggregate{labels: map[string]int{}, values: map[string][]float64{}}
	passed, total := 0, 0
	for _, key := range keys {
		cell := cells[key]
		passed += cell.stat.Passed
		total += cell.stat.Total
		for _, trial := range cell.trials {
			if trial.Failure != nil && trial.Failure.Label != "" {
				result.labels[trial.Failure.Label]++
			}
			for _, spec := range metricSpecs {
				if value, ok := spec.extract(trial); ok {
					result.values[spec.name] = append(result.values[spec.name], value)
				}
			}
		}
	}
	result.overall = newRateStat(passed, total)
	return result
}

func median(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	result := sorted[mid]
	if len(sorted)%2 == 0 {
		result = (sorted[mid-1] + sorted[mid]) / 2
	}
	return &result
}

// Compare diffs two reports. variant selects the release arm in both reports
// that hold variants; empty means DefaultVariant. Aggregates cover only the
// (agent, scenario) pairs present in both reports.
func Compare(base, head Report, variant string) (RunDiff, error) {
	baseName, baseAgents, err := selectAgents(base, variant)
	if err != nil {
		return RunDiff{}, fmt.Errorf("base: %w", err)
	}
	headName, headAgents, err := selectAgents(head, variant)
	if err != nil {
		return RunDiff{}, fmt.Errorf("head: %w", err)
	}
	baseCells, headCells := collectCells(baseAgents), collectCells(headAgents)
	diff := RunDiff{Schema: diffSchemaV1, BaseVariant: baseName, HeadVariant: headName, RegressionThreshold: RegressionDrop,
		Cells: []CellDiff{}, NewlyFailing: []CellDiff{}, NewlyPassing: []CellDiff{}, FailureLabels: []LabelDiff{}, Metrics: []MetricDiff{}}
	var shared []cellKey
	for _, key := range sortedKeys(baseCells) {
		if _, ok := headCells[key]; !ok {
			diff.OnlyInBase = append(diff.OnlyInBase, key.String())
			continue
		}
		shared = append(shared, key)
		cell := CellDiff{Agent: key.agent, Scenario: key.scenario, Base: baseCells[key].stat, Head: headCells[key].stat}
		cell.Delta = cell.Head.PassRate - cell.Base.PassRate
		diff.Cells = append(diff.Cells, cell)
		switch {
		case cell.Delta < -rateEpsilon:
			diff.NewlyFailing = append(diff.NewlyFailing, cell)
		case cell.Delta > rateEpsilon:
			diff.NewlyPassing = append(diff.NewlyPassing, cell)
		}
	}
	for _, key := range sortedKeys(headCells) {
		if _, ok := baseCells[key]; !ok {
			diff.OnlyInHead = append(diff.OnlyInHead, key.String())
		}
	}
	baseAgg, headAgg := aggregateCells(baseCells, shared), aggregateCells(headCells, shared)
	diff.Overall = OverallDiff{Base: baseAgg.overall, Head: headAgg.overall, Delta: headAgg.overall.PassRate - baseAgg.overall.PassRate}
	diff.Overall.Significant = baseAgg.overall.Total > 0 && headAgg.overall.Total > 0 &&
		(headAgg.overall.ConfidenceUpper < baseAgg.overall.ConfidenceLower || headAgg.overall.ConfidenceLower > baseAgg.overall.ConfidenceUpper)

	labels := map[string]bool{}
	for label := range baseAgg.labels {
		labels[label] = true
	}
	for label := range headAgg.labels {
		labels[label] = true
	}
	for label := range labels {
		diff.FailureLabels = append(diff.FailureLabels, LabelDiff{Label: label, Base: baseAgg.labels[label], Head: headAgg.labels[label], Delta: headAgg.labels[label] - baseAgg.labels[label]})
	}
	sort.Slice(diff.FailureLabels, func(i, j int) bool {
		a, b := diff.FailureLabels[i], diff.FailureLabels[j]
		if a.Head != b.Head {
			return a.Head > b.Head
		}
		if a.Base != b.Base {
			return a.Base > b.Base
		}
		return a.Label < b.Label
	})

	for _, spec := range metricSpecs {
		metric := MetricDiff{Name: spec.name, Base: median(baseAgg.values[spec.name]), Head: median(headAgg.values[spec.name]),
			BaseCount: len(baseAgg.values[spec.name]), HeadCount: len(headAgg.values[spec.name])}
		if metric.Base == nil && metric.Head == nil {
			continue
		}
		if metric.Base != nil && metric.Head != nil {
			delta := *metric.Head - *metric.Base
			metric.Delta = &delta
		}
		diff.Metrics = append(diff.Metrics, metric)
	}
	// Largest drops first so regressions lead every rendering.
	sort.SliceStable(diff.NewlyFailing, func(i, j int) bool { return diff.NewlyFailing[i].Delta < diff.NewlyFailing[j].Delta })
	sort.SliceStable(diff.NewlyPassing, func(i, j int) bool { return diff.NewlyPassing[i].Delta > diff.NewlyPassing[j].Delta })
	diff.Regression = diff.Regressed()
	return diff, nil
}

// Summarize digests one report. variant selects the release arm; empty means
// DefaultVariant for release reports.
func Summarize(report Report, variant string) (Summary, error) {
	name, agents, err := selectAgents(report, variant)
	if err != nil {
		return Summary{}, err
	}
	cells := collectCells(agents)
	keys := sortedKeys(cells)
	agg := aggregateCells(cells, keys)
	summary := Summary{Schema: summarySchemaV1, Variant: name, Overall: agg.overall, Cells: []CellStat{}, FailureLabels: []LabelCount{}, Metrics: []MetricStat{}}
	for _, key := range keys {
		summary.Cells = append(summary.Cells, CellStat{Agent: key.agent, Scenario: key.scenario, RateStat: cells[key].stat})
	}
	for label, count := range agg.labels {
		summary.FailureLabels = append(summary.FailureLabels, LabelCount{Label: label, Count: count})
	}
	sort.Slice(summary.FailureLabels, func(i, j int) bool {
		a, b := summary.FailureLabels[i], summary.FailureLabels[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Label < b.Label
	})
	for _, spec := range metricSpecs {
		if values := agg.values[spec.name]; len(values) > 0 {
			summary.Metrics = append(summary.Metrics, MetricStat{Name: spec.name, Median: median(values), Count: len(values)})
		}
	}
	return summary, nil
}

// WriteJSON writes a formatted JSON document to stdout or an explicit path.
func WriteJSON(document any, output string, stdout io.Writer) error {
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return WriteText(string(data)+"\n", output, stdout)
}

// WriteText writes text to stdout or an explicit output path.
func WriteText(text, output string, stdout io.Writer) error {
	if output == "" {
		_, err := io.WriteString(stdout, text)
		return err
	}
	return os.WriteFile(output, []byte(text), 0o600) // #nosec G304 -- explicit output flag.
}

// RenderDiffMarkdown renders a compact GitHub-flavored summary, regressions first.
func RenderDiffMarkdown(diff RunDiff) string {
	var b strings.Builder
	status := "no regression"
	if diff.Regression {
		status = "**REGRESSION**"
	}
	b.WriteString("## Agent eval comparison\n\n")
	if diff.BaseVariant != "" || diff.HeadVariant != "" {
		fmt.Fprintf(&b, "Variant: base `%s`, head `%s`\n\n", diff.BaseVariant, diff.HeadVariant)
	}
	b.WriteString("| | Base | Head | Delta |\n|---|---|---|---|\n")
	fmt.Fprintf(&b, "| Classification pass rate | %s | %s | %s |\n", formatRateStat(diff.Overall.Base), formatRateStat(diff.Overall.Head), formatPoints(diff.Overall.Delta))
	fmt.Fprintf(&b, "\n%d newly failing, %d newly passing. Status: %s (a pair regresses when its pass rate drops by %.0f points or more).", len(diff.NewlyFailing), len(diff.NewlyPassing), status, RegressionDrop*100)
	if diff.Overall.Significant {
		b.WriteString(" The overall change is outside the Wilson 95% intervals.")
	}
	b.WriteString("\n")

	b.WriteString("\n### Newly failing\n\n")
	writeCellTable(&b, diff.NewlyFailing, "None.")
	b.WriteString("\n### Newly passing\n\n")
	writeCellTable(&b, diff.NewlyPassing, "None.")

	b.WriteString("\n### Failure labels\n\n")
	if len(diff.FailureLabels) == 0 {
		b.WriteString("No labeled failures.\n")
	} else {
		b.WriteString("| Label | Base | Head | Delta |\n|---|---|---|---|\n")
		for _, label := range diff.FailureLabels {
			fmt.Fprintf(&b, "| %s | %d | %d | %+d |\n", mdEscape(label.Label), label.Base, label.Head, label.Delta)
		}
	}

	if len(diff.Metrics) > 0 {
		b.WriteString("\n### Medians per trial\n\n| Metric | Base | Head | Delta |\n|---|---|---|---|\n")
		for _, metric := range diff.Metrics {
			delta := "n/a"
			if metric.Delta != nil {
				delta = formatMetric(metric.Name, *metric.Delta, true)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", metricLabel(metric.Name), formatMetricPtr(metric.Name, metric.Base), formatMetricPtr(metric.Name, metric.Head), delta)
		}
	}

	if len(diff.Cells) > 0 {
		b.WriteString("\n<details><summary>All agent and scenario pass rates</summary>\n\n")
		b.WriteString("| Agent | Scenario | Base | Head | Delta |\n|---|---|---|---|---|\n")
		for _, cell := range diff.Cells {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", mdEscape(cell.Agent), mdEscape(cell.Scenario), formatCount(cell.Base), formatCount(cell.Head), formatPoints(cell.Delta))
		}
		b.WriteString("\n</details>\n")
	}
	if len(diff.OnlyInBase)+len(diff.OnlyInHead) > 0 {
		fmt.Fprintf(&b, "\nExcluded because only one report has them: %d in base only, %d in head only.\n", len(diff.OnlyInBase), len(diff.OnlyInHead))
	}
	return b.String()
}

// RenderSummaryMarkdown renders a compact GitHub-flavored digest of one report.
func RenderSummaryMarkdown(summary Summary) string {
	var b strings.Builder
	b.WriteString("## Agent eval summary\n\n")
	if summary.Variant != "" {
		fmt.Fprintf(&b, "Variant: `%s`\n\n", summary.Variant)
	}
	fmt.Fprintf(&b, "Classification pass rate: **%s**\n\n", formatRateStat(summary.Overall))
	if len(summary.Cells) == 0 {
		b.WriteString("No agent results in this report.\n")
	} else {
		b.WriteString("| Agent | Scenario | Passed | Pass rate |\n|---|---|---|---|\n")
		for _, cell := range summary.Cells {
			fmt.Fprintf(&b, "| %s | %s | %d/%d | %s |\n", mdEscape(cell.Agent), mdEscape(cell.Scenario), cell.Passed, cell.Total, formatPercent(cell.PassRate))
		}
	}
	b.WriteString("\n### Failure labels\n\n")
	if len(summary.FailureLabels) == 0 {
		b.WriteString("No labeled failures.\n")
	} else {
		b.WriteString("| Label | Trials |\n|---|---|\n")
		for _, label := range summary.FailureLabels {
			fmt.Fprintf(&b, "| %s | %d |\n", mdEscape(label.Label), label.Count)
		}
	}
	if len(summary.Metrics) > 0 {
		b.WriteString("\n### Medians per trial\n\n| Metric | Median | Trials reporting |\n|---|---|---|\n")
		for _, metric := range summary.Metrics {
			fmt.Fprintf(&b, "| %s | %s | %d |\n", metricLabel(metric.Name), formatMetricPtr(metric.Name, metric.Median), metric.Count)
		}
	}
	return b.String()
}

func writeCellTable(b *strings.Builder, cells []CellDiff, empty string) {
	if len(cells) == 0 {
		b.WriteString(empty + "\n")
		return
	}
	b.WriteString("| Agent | Scenario | Base | Head | Delta |\n|---|---|---|---|---|\n")
	for _, cell := range cells {
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n", mdEscape(cell.Agent), mdEscape(cell.Scenario), formatCount(cell.Base), formatCount(cell.Head), formatPoints(cell.Delta))
	}
}

func mdEscape(value string) string { return strings.ReplaceAll(value, "|", `\|`) }

func formatPercent(value float64) string { return fmt.Sprintf("%.1f%%", value*100) }

func formatPoints(delta float64) string { return fmt.Sprintf("%+.1f pts", delta*100) }

func formatCount(stat RateStat) string {
	return fmt.Sprintf("%d/%d (%s)", stat.Passed, stat.Total, formatPercent(stat.PassRate))
}

func formatRateStat(stat RateStat) string {
	if stat.Total == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%s (%d/%d, 95%% CI %.0f-%.0f%%)", formatPercent(stat.PassRate), stat.Passed, stat.Total, stat.ConfidenceLower*100, stat.ConfidenceUpper*100)
}

func metricLabel(name string) string {
	switch name {
	case "engine_calls":
		return "Engine calls"
	case "failed_calls":
		return "Failed Engine calls"
	case "usage_errors":
		return "Usage errors"
	case "turns":
		return "Agent turns"
	case "cost_usd":
		return "Cost (USD)"
	case "tokens":
		return "Tokens (input+output)"
	case "wall_ms":
		return "Wall time"
	default:
		return name
	}
}

func formatMetricPtr(name string, value *float64) string {
	if value == nil {
		return "n/a"
	}
	return formatMetric(name, *value, false)
}

func formatMetric(name string, value float64, signed bool) string {
	sign := ""
	if value < 0 {
		sign, value = "-", -value
	} else if signed && value > 0 {
		sign = "+"
	}
	switch name {
	case "cost_usd":
		return fmt.Sprintf("%s$%.4f", sign, value)
	case "wall_ms":
		return fmt.Sprintf("%s%.1fs", sign, value/1000)
	default:
		return sign + strconv.FormatFloat(math.Round(value*100)/100, 'f', -1, 64)
	}
}
