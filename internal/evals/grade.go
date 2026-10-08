package evals

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/reconifyhq/reconify/schemas"
)

// Grader names. They are the stable identifiers in TrialReport.Grades.
const (
	gradeDiscovery        = "discovery"
	gradeConfiguration    = "configuration"
	gradeExecution        = "execution"
	gradeClassification   = "classification"
	gradeExactResult      = "exact_result"
	gradeAssertionsMatch  = "assertions_match"
	gradeExplanation      = "explanation"
	gradeProtocol         = "protocol"
	gradeClaimsConsistent = "claims_consistent"
	gradeDecisionSurfaced = "decision_surfaced"
)

// trialEvidence is everything the graders observe about one finished trial.
type trialEvidence struct {
	commands []string
	trace    []TraceEntry

	configPresent   bool
	configValidated bool   // the evaluator's own `config validate` passed
	configDetail    string // first validation problem when it did not

	verified     bool   // the evaluator's deterministic reconcile succeeded
	verifyDetail string // why it did not
	artifact     bool   // the agent left a result artifact
	actual       []byte // evaluator-verified result
	expected     []byte // answer key
	assertions   schemas.EvalAssertions

	hasExplanationKey bool
	explanationFound  bool
	explanationEqual  bool

	messages         []string // agent assistant messages, final answer last
	decisionKeywords []string
}

// ran reports whether the agent invoked the named subcommand. The trace is
// authoritative when present; older binaries fall back to the wrapper log.
func (e trialEvidence) ran(name string) bool {
	if len(e.trace) > 0 {
		return traceIndex(e.trace, name) >= 0
	}
	return containsCommand(e.commands, name)
}

// firstIndex returns the position of the first call to name in the trace or log.
func (e trialEvidence) firstIndex(name string) int {
	if len(e.trace) > 0 {
		return traceIndex(e.trace, name)
	}
	for i, command := range e.commands {
		if strings.Contains(command, name) {
			return i
		}
	}
	return -1
}

func (e trialEvidence) finalMessage() string {
	if len(e.messages) == 0 {
		return ""
	}
	return e.messages[len(e.messages)-1]
}

// gradeTrial runs every grader. The list order follows the contract table.
func gradeTrial(e trialEvidence) []Grade {
	grades := []Grade{
		gradeOf(gradeDiscovery, false, e.ran("capabilities"), "ran reconify capabilities", "never ran reconify capabilities"),
		gradeConfigurationOf(e),
		gradeExecutionOf(e),
		gradeClassificationOf(e),
		gradeExactResultOf(e),
		gradeAssertionsOf(e),
	}
	if e.hasExplanationKey {
		grades = append(grades, gradeExplanationOf(e))
	}
	grades = append(grades, gradeProtocolOf(e), gradeClaimsOf(e))
	if len(e.decisionKeywords) > 0 {
		grades = append(grades, gradeDecisionOf(e))
	}
	return grades
}

func gradeOf(name string, gating, pass bool, passEvidence, failEvidence string) Grade {
	evidence := failEvidence
	if pass {
		evidence = passEvidence
	}
	return Grade{Name: name, Pass: pass, Gating: gating, Evidence: evidence}
}

func gradeConfigurationOf(e trialEvidence) Grade {
	switch {
	case !e.configPresent:
		return Grade{Name: gradeConfiguration, Evidence: "reconify.yaml was not created"}
	case !e.ran("config validate"):
		return Grade{Name: gradeConfiguration, Evidence: "agent never ran config validate"}
	case !e.configValidated:
		return Grade{Name: gradeConfiguration, Evidence: "final config is invalid: " + e.configDetail}
	}
	return Grade{Name: gradeConfiguration, Pass: true, Evidence: "agent ran config validate and the final config validates"}
}

func gradeExecutionOf(e trialEvidence) Grade {
	switch {
	case !e.configPresent:
		return Grade{Name: gradeExecution, Evidence: "reconify.yaml was not created"}
	case !e.verified:
		return Grade{Name: gradeExecution, Evidence: "verification reconcile failed: " + e.verifyDetail}
	case !e.ran("reconcile"):
		return Grade{Name: gradeExecution, Evidence: "agent never ran reconcile"}
	case !e.artifact:
		return Grade{Name: gradeExecution, Evidence: "no result.json or agent-result.json left in the workspace"}
	}
	return Grade{Name: gradeExecution, Pass: true, Evidence: "agent ran reconcile and left a result artifact"}
}

// notVerified is the shared evidence for graders that need the verified run.
func notVerified(e trialEvidence) string {
	if !e.configPresent {
		return "reconify.yaml was not created"
	}
	return "verification reconcile failed: " + e.verifyDetail
}

func gradeClassificationOf(e trialEvidence) Grade {
	if !e.verified {
		return Grade{Name: gradeClassification, Gating: true, Evidence: notVerified(e)}
	}
	return gradeOf(gradeClassification, true, semanticResultEqual(e.actual, e.expected),
		"verified event multiset equals the answer key", "verified event multiset differs from the answer key")
}

func gradeExactResultOf(e trialEvidence) Grade {
	if !e.verified {
		return Grade{Name: gradeExactResult, Evidence: notVerified(e)}
	}
	return gradeOf(gradeExactResult, false, bytes.Equal(bytes.TrimSpace(e.actual), bytes.TrimSpace(e.expected)),
		"verified result is byte-equal to the answer key", "verified result is not byte-equal to the answer key")
}

func gradeAssertionsOf(e trialEvidence) Grade {
	if !e.verified {
		return Grade{Name: gradeAssertionsMatch, Evidence: notVerified(e)}
	}
	return gradeOf(gradeAssertionsMatch, false, assertionsMatch(e.actual, e.assertions),
		"verified summary equals the scenario assertions", "verified summary differs from the scenario assertions")
}

func gradeExplanationOf(e trialEvidence) Grade {
	switch {
	case !e.verified:
		return Grade{Name: gradeExplanation, Evidence: notVerified(e)}
	case !e.explanationFound:
		return Grade{Name: gradeExplanation, Evidence: "no explanation.json or agent-explanation.json left in the workspace"}
	case !e.ran("explain"):
		return Grade{Name: gradeExplanation, Evidence: "agent never ran explain"}
	case !e.explanationEqual:
		return Grade{Name: gradeExplanation, Evidence: "explanation differs from the answer key"}
	}
	return Grade{Name: gradeExplanation, Pass: true, Evidence: "agent ran explain and the explanation equals the answer key"}
}

func gradeProtocolOf(e trialEvidence) Grade {
	validate, reconcile := e.firstIndex("config validate"), e.firstIndex("reconcile")
	switch {
	case reconcile < 0:
		return Grade{Name: gradeProtocol, Evidence: "agent never ran reconcile"}
	case validate < 0:
		return Grade{Name: gradeProtocol, Evidence: "agent never ran config validate"}
	case validate > reconcile:
		return Grade{Name: gradeProtocol, Evidence: fmt.Sprintf("first reconcile (call %d) preceded first config validate (call %d)", reconcile+1, validate+1)}
	}
	return Grade{Name: gradeProtocol, Pass: true, Evidence: fmt.Sprintf("config validate (call %d) preceded first reconcile (call %d)", validate+1, reconcile+1)}
}

func gradeClaimsOf(e trialEvidence) Grade {
	claims := parseClaims(e.finalMessage())
	if len(claims) == 0 {
		return Grade{Name: gradeClaimsConsistent, Pass: true, Evidence: "no claims found"}
	}
	summary, ok := summaryCounters(e.actual)
	if !e.verified || !ok {
		return Grade{Name: gradeClaimsConsistent, Evidence: fmt.Sprintf("%d claim(s) found but no verified summary to check them against", len(claims))}
	}
	// Agents legitimately quote other values for a counter (an earlier
	// attempt, a per-counterpart breakdown), so a counter is consistent when
	// any of its claims equals the verified value.
	consistent := map[string]bool{}
	claimed := map[string][]int{}
	var order []string
	for _, claim := range claims {
		if _, seen := claimed[claim.counter]; !seen {
			order = append(order, claim.counter)
		}
		claimed[claim.counter] = append(claimed[claim.counter], claim.value)
		if summary[claim.counter] == claim.value {
			consistent[claim.counter] = true
		}
	}
	var wrong []string
	for _, counter := range order {
		if !consistent[counter] {
			values := make([]string, 0, len(claimed[counter]))
			for _, value := range claimed[counter] {
				values = append(values, strconv.Itoa(value))
			}
			wrong = append(wrong, fmt.Sprintf("claimed %s=%s, verified %d", counter, strings.Join(values, "/"), summary[counter]))
		}
	}
	if len(wrong) > 0 {
		return Grade{Name: gradeClaimsConsistent, Evidence: strings.Join(wrong, "; ")}
	}
	return Grade{Name: gradeClaimsConsistent, Pass: true, Evidence: fmt.Sprintf("%d claim(s) match the verified summary", len(claims))}
}

func gradeDecisionOf(e trialEvidence) Grade {
	text := strings.ToLower(strings.Join(e.messages, "\n"))
	for _, keyword := range e.decisionKeywords {
		if keyword != "" && strings.Contains(text, strings.ToLower(keyword)) {
			return Grade{Name: gradeDecisionSurfaced, Pass: true, Evidence: fmt.Sprintf("output mentions %q", keyword)}
		}
	}
	return Grade{Name: gradeDecisionSurfaced, Evidence: fmt.Sprintf("output mentions none of %q", e.decisionKeywords)}
}

// gradePass reports whether the named grade exists and passed.
func gradePass(grades []Grade, name string) bool {
	for _, grade := range grades {
		if grade.Name == name {
			return grade.Pass
		}
	}
	return false
}
