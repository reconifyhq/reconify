package evals

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// claim is one summary counter value asserted in the agent's final answer.
type claim struct {
	counter string
	value   int
}

// claimAliases maps summary fields to the phrases agents use for them. Phrases
// are matched after lowercasing and replacing underscores with spaces.
var claimAliases = []struct {
	counter string
	phrase  string
}{
	{"unmatched_left", `unmatched left`},
	{"unmatched_right", `unmatched right`},
	{"amount_diff_count", `amount (?:diff|differences?|mismatch(?:es)?)(?: count)?`},
	{"timing_diff_count", `timing (?:diff|differences?)(?: count)?`},
	{"duplicate_count", `duplicates?(?: count)?`},
	{"matched", `matched`},
}

var claimPatterns = func() []struct {
	counter string
	label   *regexp.Regexp
	count   *regexp.Regexp
} {
	patterns := make([]struct {
		counter string
		label   *regexp.Regexp
		count   *regexp.Regexp
	}, 0, len(claimAliases))
	for _, alias := range claimAliases {
		patterns = append(patterns, struct {
			counter string
			label   *regexp.Regexp
			count   *regexp.Regexp
		}{
			alias.counter,
			// "matched: 3", "**matched**: 3", "| matched | 3 |".
			regexp.MustCompile(`\b` + alias.phrase + `\b[\s*` + "`" + `|:=]{1,6}(\d+)\b`),
			// "3 matched"; the number must stand alone, not end an id like PAY-003.
			regexp.MustCompile(`(?:^|[^\w.\-])(\d+)[\s*` + "`" + `]+` + alias.phrase + `\b`),
		})
	}
	return patterns
}()

// parseClaims extracts counter claims such as `matched: 3` or `1 unmatched left`.
// Text that makes no recognizable claim yields none.
func parseClaims(text string) []claim {
	text = strings.ToLower(strings.ReplaceAll(text, "_", " "))
	var claims []claim
	for _, pattern := range claimPatterns {
		for _, expression := range []*regexp.Regexp{pattern.label, pattern.count} {
			for _, match := range expression.FindAllStringSubmatchIndex(text, -1) {
				if pattern.counter == "matched" && qualifiedMatch(text[:match[0]]) {
					continue
				}
				if value, err := strconv.Atoi(text[match[2]:match[3]]); err == nil {
					claims = append(claims, claim{counter: pattern.counter, value: value})
				}
			}
		}
	}
	return claims
}

var precedingWord = regexp.MustCompile(`(\w+)\W*$`)

// qualifiedMatch reports whether "matched" is the tail of a different counter
// such as "grouped matched" or "many to many matched", which are not summary.matched.
func qualifiedMatch(before string) bool {
	word := precedingWord.FindStringSubmatch(before)
	if word == nil {
		return false
	}
	switch word[1] {
	case "grouped", "many", "fuzzy", "tokens", "token", "name":
		return true
	}
	return false
}

// summaryCounters reads the integer counters of a result document's summary.
func summaryCounters(result []byte) (map[string]int, bool) {
	var document struct {
		Summary map[string]any `json:"summary"`
	}
	if json.Unmarshal(result, &document) != nil || document.Summary == nil {
		return nil, false
	}
	counters := map[string]int{}
	for _, claimAlias := range claimAliases {
		if value, ok := document.Summary[claimAlias.counter].(float64); ok {
			counters[claimAlias.counter] = int(value)
		}
	}
	return counters, true
}
