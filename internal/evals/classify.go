package evals

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reconifyhq/reconify/config"
	"github.com/reconifyhq/reconify/engine/matching"
)

// Failure labels, checked in this order; the first match wins.
const (
	labelTimeout                = "timeout"
	labelAgentError             = "agent_error"
	labelMissingConfig          = "missing_config"
	labelConfigInvalid          = "config_invalid"
	labelFilePatternUnresolved  = "file_pattern_unresolved"
	labelWrongAmountMapping     = "wrong_amount_mapping"
	labelWrongDateLayout        = "wrong_date_layout"
	labelWrongTolerance         = "wrong_tolerance"
	labelWrongDateWindow        = "wrong_date_window"
	labelWrongMatchingStrategy  = "wrong_matching_strategy"
	labelMissingResultArtifact  = "missing_result_artifact"
	labelWrongClassification    = "wrong_classification"
	maxDiagnosticEvidenceLength = 300
)

// classifyInput is the evidence the failure classifier inspects.
type classifyInput struct {
	ctxErr    error // the trial context's error after the agent finished
	agentErr  error
	item      scenario
	workspace string

	configPresent bool
	configValid   bool
	configDetail  string

	verified     bool
	verifyDiag   diagnosticInfo
	verifyDetail string
	artifact     bool
}

// diagnosticInfo is the subset of a reconify.engine.diagnostic.v1 envelope the
// evaluator reads from `--error-format json` stderr.
type diagnosticInfo struct {
	Code    string
	Message string
	Errors  []string // "path: message" entries from details.errors
}

// parseDiagnostic extracts the diagnostic envelope from stderr, tolerating
// surrounding text. A zero value means no envelope was found.
func parseDiagnostic(stderr []byte) diagnosticInfo {
	for _, value := range jsonValues(stderr) {
		diagnostic := asMap(value["diagnostic"])
		if diagnostic == nil {
			continue
		}
		info := diagnosticInfo{Code: asString(diagnostic["code"]), Message: asString(diagnostic["message"])}
		if list, ok := asMap(diagnostic["details"])["errors"].([]any); ok {
			for _, item := range list {
				entry := asMap(item)
				if message := asString(entry["message"]); message != "" {
					info.Errors = append(info.Errors, strings.TrimPrefix(asString(entry["path"])+": "+message, ": "))
				}
			}
		}
		return info
	}
	return diagnosticInfo{}
}

// detail is a one-line description of the diagnostic for evidence strings.
func (d diagnosticInfo) detail(fallback string) string {
	switch {
	case len(d.Errors) > 0:
		return d.Errors[0]
	case d.Message != "":
		return truncate(d.Message, maxDiagnosticEvidenceLength)
	}
	return truncate(fallback, maxDiagnosticEvidenceLength)
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

// classifyFailure labels why a trial failed classification. Callers invoke it
// only after the classification grade failed.
func classifyFailure(in classifyInput) *FailureReport {
	switch {
	case errors.Is(in.ctxErr, context.DeadlineExceeded):
		return &FailureReport{Label: labelTimeout, Evidence: "agent run exceeded the trial timeout"}
	case in.agentErr != nil && !in.configPresent:
		return &FailureReport{Label: labelAgentError, Evidence: truncate(in.agentErr.Error(), maxDiagnosticEvidenceLength)}
	case !in.configPresent:
		return &FailureReport{Label: labelMissingConfig, Evidence: "agent finished without creating reconify.yaml"}
	case !in.configValid:
		return &FailureReport{Label: labelConfigInvalid, Evidence: in.configDetail}
	case !in.verified && unresolvedInput(in.verifyDiag, in.verifyDetail):
		return &FailureReport{Label: labelFilePatternUnresolved, Evidence: in.verifyDiag.detail(in.verifyDetail)}
	}
	if finding := compareToReference(in); finding != nil {
		return finding
	}
	if !in.artifact {
		return &FailureReport{Label: labelMissingResultArtifact, Evidence: "no result.json or agent-result.json in the workspace"}
	}
	evidence := "verified result differs from the answer key although the config matches the reference on every compared field"
	if !in.verified {
		evidence = "verification reconcile failed: " + in.verifyDiag.detail(in.verifyDetail)
	}
	return &FailureReport{Label: labelWrongClassification, Evidence: evidence}
}

// unresolvedInput reports whether a failed verification run could not find or
// read the source files a config's file_pattern names.
func unresolvedInput(diag diagnosticInfo, detail string) bool {
	if diag.Code == "INPUT_UNREADABLE" {
		return true
	}
	text := strings.ToLower(diag.Message + " " + detail)
	for _, marker := range []string{"no files match pattern", "resolves outside config directory", "no file specified and no file_pattern"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// compareToReference explains a failed classification by diffing the agent's
// config with the scenario's reference config for the scenario pair. It never
// grades; behavior grading already decided the trial failed.
func compareToReference(in classifyInput) *FailureReport {
	if in.item.ReferenceConfig == "" {
		return nil
	}
	agent, err := config.Load(filepath.Join(in.workspace, "reconify.yaml"))
	if err != nil {
		return nil
	}
	reference, err := config.Load(filepath.Join(in.item.Dir, in.item.ReferenceConfig))
	if err != nil {
		return nil
	}
	findings := configFindings(agent, reference, in.item.Pair)
	for _, label := range []string{labelWrongAmountMapping, labelWrongDateLayout, labelWrongTolerance, labelWrongDateWindow, labelWrongMatchingStrategy} {
		if evidence, ok := findings[label]; ok {
			return &FailureReport{Label: label, Evidence: evidence}
		}
	}
	return nil
}

// configFindings returns the first concrete difference per failure label. Sources
// are compared by role through each config's own pair, so source names may differ.
func configFindings(agent, reference *config.Config, pairName string) map[string]string {
	findings := map[string]string{}
	agentPair, agentOK := agent.Pairs[pairName]
	refPair, refOK := reference.Pairs[pairName]
	if !agentOK || !refOK {
		return findings
	}
	note := func(label, evidence string) {
		if _, seen := findings[label]; !seen {
			findings[label] = evidence
		}
	}
	type side struct {
		role         string
		agent, ref   config.Source
		agentPresent bool
	}
	var sides []side
	add := func(role, agentName, refName string) {
		agentSource, present := agent.Sources[agentName]
		refSource, refPresent := reference.Sources[refName]
		if refPresent {
			sides = append(sides, side{role: role, agent: agentSource, ref: refSource, agentPresent: present})
		}
	}
	add("left", agentPair.Left, refPair.Left)
	agentRights, refRights := agentPair.Counterparts(), refPair.Counterparts()
	if len(agentRights) != len(refRights) {
		note(labelWrongMatchingStrategy, fmt.Sprintf("pair has %d counterpart source(s) vs reference %d", len(agentRights), len(refRights)))
	}
	for i := range refRights {
		role := "right"
		if len(refRights) > 1 {
			role = fmt.Sprintf("right[%d]", i)
		}
		if i < len(agentRights) {
			add(role, agentRights[i], refRights[i])
		}
	}
	for _, s := range sides {
		if !s.agentPresent {
			continue
		}
		a, r := s.agent.Parser, s.ref.Parser
		for _, field := range []struct {
			name, got, want string
		}{
			{"amount_col", a.AmountCol, r.AmountCol},
			{"multiplier", fmt.Sprint(a.Multiplier), fmt.Sprint(r.Multiplier)},
			{"decimal", defaultString(a.Decimal, "."), defaultString(r.Decimal, ".")},
			{"thousands", a.Thousands, r.Thousands},
		} {
			if field.got != field.want {
				note(labelWrongAmountMapping, fmt.Sprintf("%s.%s %q vs reference %q", s.role, field.name, field.got, field.want))
			}
		}
		if a.DateLayout != r.DateLayout {
			note(labelWrongDateLayout, fmt.Sprintf("%s.date_layout %q vs reference %q", s.role, a.DateLayout, r.DateLayout))
		}
		if groupKey(a) != groupKey(r) {
			note(labelWrongMatchingStrategy, fmt.Sprintf("%s.group_col %q vs reference %q", s.role, groupKey(a), groupKey(r)))
		}
		if a.ResolvedDuplicatePolicy() != r.ResolvedDuplicatePolicy() {
			note(labelWrongMatchingStrategy, fmt.Sprintf("%s.duplicate_policy %q vs reference %q", s.role, a.ResolvedDuplicatePolicy(), r.ResolvedDuplicatePolicy()))
		}
	}
	if agentPair.AmountToleranceMinor != refPair.AmountToleranceMinor {
		note(labelWrongTolerance, fmt.Sprintf("pair.amount_tolerance_minor %d vs reference %d", agentPair.AmountToleranceMinor, refPair.AmountToleranceMinor))
	}
	if windowDays(agentPair.DateWindow) != windowDays(refPair.DateWindow) {
		note(labelWrongDateWindow, fmt.Sprintf("pair.date_window %q vs reference %q", agentPair.DateWindow, refPair.DateWindow))
	}
	if got, want := passSignature(agentPair), passSignature(refPair); got != want {
		note(labelWrongMatchingStrategy, fmt.Sprintf("pair.passes [%s] vs reference [%s]", got, want))
	}
	return findings
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// groupKey is the effective duplicate/grouping column: group_col, else ref_col.
func groupKey(parser config.ParserCfg) string { return defaultString(parser.GroupCol, parser.RefCol) }

// windowDays normalizes a date_window to days; unparsable windows compare as -1.
func windowDays(window string) int {
	days, err := matching.ParseDateWindow(window)
	if err != nil {
		return -1
	}
	return days
}

// passSignature lists the effective passes, making the implicit legacy pipeline
// (reference matching plus optional name tokens) comparable with explicit passes.
func passSignature(pair config.Pair) string {
	var passes []string
	for _, pass := range pair.Passes {
		passes = append(passes, pass.Type+":"+pass.ResolvedGroupBy())
	}
	if len(pair.Passes) == 0 {
		passes = []string{config.PassTypeReferenceOneToOne + ":" + config.GroupByReference}
		if pair.NameMode == "tokens" {
			passes = append(passes, config.PassTypeNameTokensOneToOne+":"+config.GroupByReference)
		}
	}
	return strings.Join(passes, " ")
}
