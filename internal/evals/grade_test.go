package evals

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/reconifyhq/reconify/schemas"
)

const answerKey = `{"summary":{"matched":3,"unmatched_left":1,"unmatched_right":0,"amount_diff_count":1,"timing_diff_count":0,"duplicate_count":0},"matched":[{"left":{"amount":1},"right":{"amount":1}}],"unmatched_left":[],"unmatched_right":[],"amount_diff":null,"timing_diff":null,"duplicates":null}`

func goodEvidence() trialEvidence {
	return trialEvidence{
		trace: []TraceEntry{
			call(0, "", "capabilities"),
			call(0, "", "config", "validate"),
			call(0, "", "reconcile"),
			call(0, "", "explain", "result.json"),
		},
		configPresent: true, configValidated: true,
		verified: true, artifact: true,
		actual: []byte(answerKey), expected: []byte(answerKey),
		assertions:        schemas.EvalAssertions{Matched: 3, UnmatchedLeft: 1, AmountDiffCount: 1},
		hasExplanationKey: true, explanationFound: true, explanationEqual: true,
		messages: []string{"intermediate", "matched: 3, unmatched left: 1, amount differences: 1"},
	}
}

func grade(t *testing.T, grades []Grade, name string) Grade {
	t.Helper()
	for _, g := range grades {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("grade %q missing from %+v", name, grades)
	return Grade{}
}

func TestGradeTrialAllPass(t *testing.T) {
	grades := gradeTrial(goodEvidence())
	var names []string
	for _, g := range grades {
		names = append(names, g.Name)
		if !g.Pass {
			t.Errorf("grade %s failed: %s", g.Name, g.Evidence)
		}
		if g.Evidence == "" {
			t.Errorf("grade %s has no evidence", g.Name)
		}
		if g.Gating != (g.Name == gradeClassification) {
			t.Errorf("grade %s gating = %v", g.Name, g.Gating)
		}
	}
	want := []string{"discovery", "configuration", "execution", "classification", "exact_result", "assertions_match", "explanation", "protocol", "claims_consistent"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("grades = %v, want %v", names, want)
	}
}

func TestGradeTrialSingleFailures(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*trialEvidence)
		grade  string
		want   string
	}{
		{"no capabilities", func(e *trialEvidence) { e.trace = e.trace[1:] }, gradeDiscovery, "never ran"},
		{"no validate call", func(e *trialEvidence) { e.trace = []TraceEntry{call(0, "", "reconcile")} }, gradeConfiguration, "never ran config validate"},
		{"final config invalid", func(e *trialEvidence) { e.configValidated, e.configDetail = false, "sources.left: bad" }, gradeConfiguration, "sources.left: bad"},
		{"no reconcile call", func(e *trialEvidence) { e.trace = e.trace[:2] }, gradeExecution, "never ran reconcile"},
		{"no artifact", func(e *trialEvidence) { e.artifact = false }, gradeExecution, "no result.json"},
		{"wrong events", func(e *trialEvidence) {
			e.actual = []byte(strings.Replace(answerKey, `"amount":1}`, `"amount":2}`, 1))
		}, gradeClassification, "differs"},
		{"not byte equal", func(e *trialEvidence) {
			e.actual = append([]byte(" "), e.actual...)
			e.actual = append(e.actual, []byte(" x")...)
		}, gradeExactResult, "not byte-equal"},
		{"assertions differ", func(e *trialEvidence) { e.assertions.Matched = 9 }, gradeAssertionsMatch, "differs"},
		{"explanation missing", func(e *trialEvidence) { e.explanationFound = false }, gradeExplanation, "no explanation"},
		{"explain not run", func(e *trialEvidence) { e.trace = e.trace[:3] }, gradeExplanation, "never ran explain"},
		{"explanation wrong", func(e *trialEvidence) { e.explanationEqual = false }, gradeExplanation, "differs"},
		{"reconcile before validate", func(e *trialEvidence) {
			e.trace = []TraceEntry{call(0, "", "reconcile"), call(0, "", "config", "validate")}
		}, gradeProtocol, "preceded"},
		{"validate never ran", func(e *trialEvidence) { e.trace = []TraceEntry{call(0, "", "reconcile")} }, gradeProtocol, "never ran config validate"},
		{"false claim", func(e *trialEvidence) { e.messages = []string{"matched: 4"} }, gradeClaimsConsistent, "claimed matched=4, verified 3"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			evidence := goodEvidence()
			testCase.mutate(&evidence)
			g := grade(t, gradeTrial(evidence), testCase.grade)
			if g.Pass || !strings.Contains(g.Evidence, testCase.want) {
				t.Fatalf("grade = %+v, want failure mentioning %q", g, testCase.want)
			}
		})
	}
}

func TestGradeTrialFallsBackToCommandsLog(t *testing.T) {
	evidence := goodEvidence()
	evidence.trace = nil
	evidence.commands = []string{"capabilities", "config validate --config reconify.yaml", "reconcile --pair p", "explain result.json"}
	for _, g := range gradeTrial(evidence) {
		if !g.Pass {
			t.Errorf("grade %s failed on commands log: %s", g.Name, g.Evidence)
		}
	}
	evidence.commands = []string{"reconcile --pair p", "config validate"}
	if grade(t, gradeTrial(evidence), gradeProtocol).Pass {
		t.Fatal("protocol passed with reconcile first in the commands log")
	}
}

func TestGradeTrialUnverifiedRunFailsDependentGraders(t *testing.T) {
	evidence := goodEvidence()
	evidence.verified, evidence.verifyDetail = false, "no files match pattern"
	grades := gradeTrial(evidence)
	for _, name := range []string{gradeExecution, gradeClassification, gradeExactResult, gradeAssertionsMatch, gradeExplanation} {
		if g := grade(t, grades, name); g.Pass || !strings.Contains(g.Evidence, "no files match") {
			t.Errorf("grade %s = %+v", name, g)
		}
	}
	if g := grade(t, grades, gradeClaimsConsistent); g.Pass || !strings.Contains(g.Evidence, "no verified summary") {
		t.Errorf("claims grade = %+v", g)
	}
	if !grade(t, grades, gradeDiscovery).Pass {
		t.Fatal("discovery depends only on the trace")
	}
}

func TestGradeTrialMissingConfig(t *testing.T) {
	grades := gradeTrial(trialEvidence{trace: []TraceEntry{call(0, "", "capabilities")}})
	if !grade(t, grades, gradeDiscovery).Pass {
		t.Fatal("discovery should pass")
	}
	for _, g := range grades {
		if g.Name != gradeDiscovery && g.Name != gradeClaimsConsistent && g.Name != gradeProtocol && g.Pass {
			t.Errorf("grade %s passed without a config", g.Name)
		}
	}
}

func TestExplanationGradeOnlyWhenScenarioHasAnswerKey(t *testing.T) {
	evidence := goodEvidence()
	evidence.hasExplanationKey = false
	for _, g := range gradeTrial(evidence) {
		if g.Name == gradeExplanation {
			t.Fatal("explanation graded without an expected explanation")
		}
	}
}

func TestDecisionSurfacedOnlyWithKeywords(t *testing.T) {
	evidence := goodEvidence()
	for _, g := range gradeTrial(evidence) {
		if g.Name == gradeDecisionSurfaced {
			t.Fatal("decision_surfaced graded without decision keywords")
		}
	}
	evidence.decisionKeywords = []string{"Which currency", "fx rate"}
	evidence.messages = []string{"Before I continue: which CURRENCY should be used?", "done"}
	if g := grade(t, gradeTrial(evidence), gradeDecisionSurfaced); !g.Pass || g.Gating {
		t.Fatalf("grade = %+v", g)
	}
	evidence.messages = []string{"all done"}
	if grade(t, gradeTrial(evidence), gradeDecisionSurfaced).Pass {
		t.Fatal("decision surfaced without a keyword")
	}
}

func TestParseClaims(t *testing.T) {
	for _, testCase := range []struct {
		name string
		text string
		want []string
	}{
		{"none", "I configured everything and it works.", nil},
		{"label colon", "matched: 3", []string{"matched=3"}},
		{"number first", "3 matched, 1 unmatched left", []string{"matched=3", "unmatched_left=1"}},
		{"markdown", "- **Matched**: 3\n- **Unmatched left**: 1\n| amount differences | 2 |", []string{"matched=3", "unmatched_left=1", "amount_diff_count=2"}},
		{"field names", "summary.unmatched_right = 2, duplicate_count: 0, timing_diff_count: 1", []string{"unmatched_right=2", "duplicate_count=0", "timing_diff_count=1"}},
		{"plural words", "2 duplicates and 1 amount difference and 4 timing differences", []string{"duplicate_count=2", "amount_diff_count=1", "timing_diff_count=4"}},
		{"unmatched alone is ambiguous", "unmatched: 5 rows", nil},
		{"reference ids are not counts", "PAY-003 matched the bank row", nil},
		{"grouped counters are different", "grouped matched: 2 and many-to-many matched: 1", nil},
		{"prose without numbers", "everything matched on reference", nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var got []string
			for _, c := range parseClaims(testCase.text) {
				got = append(got, c.counter+"="+strconv.Itoa(c.value))
			}
			sort.Strings(got)
			want := append([]string(nil), testCase.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("claims = %v, want %v", got, want)
			}
		})
	}
}

func TestClaimsGradeEvidence(t *testing.T) {
	evidence := goodEvidence()
	evidence.messages = []string{"I think it is fine."}
	if g := grade(t, gradeTrial(evidence), gradeClaimsConsistent); !g.Pass || g.Evidence != "no claims found" {
		t.Fatalf("grade = %+v", g)
	}
	evidence.messages = []string{"unmatched left: 2"}
	if g := grade(t, gradeTrial(evidence), gradeClaimsConsistent); g.Pass || !strings.Contains(g.Evidence, "unmatched_left=2, verified 1") {
		t.Fatalf("grade = %+v", g)
	}
	// Only the final message is checked: earlier wrong guesses are not claims.
	evidence.messages = []string{"matched: 99", "matched: 3"}
	if !grade(t, gradeTrial(evidence), gradeClaimsConsistent).Pass {
		t.Fatal("earlier message affected claims")
	}
}
