//nolint:staticcheck // Domain aliases keep package-internal signatures readable.
package output

import (
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"

	. "github.com/reconifyhq/reconify/engine/domain"
)

// ---------------------------------------------------------------------------
// JSONWriter — buffers full Result struct, writes indented JSON on Flush().
// Supports SetRunInfo (embeds RunInfo in Result.RunInfo) and SetDeterministic
// (sorts all output sections before encoding for stable diff-based audit trails).
// ---------------------------------------------------------------------------

type jsonWriter struct {
	w             io.Writer
	result        Result
	deterministic bool
}

// JSONWriter is the structured JSON writer implementation. Most callers should
// use NewResultWriter; this constructor is available for integrations that need
// metadata setters before emitting events.
type JSONWriter = jsonWriter

// NewJSONWriter creates a JSON writer with metadata setters.
func NewJSONWriter(w io.Writer) *JSONWriter { return newJSONWriter(w) }

func newJSONWriter(w io.Writer) *jsonWriter {
	return &jsonWriter{w: w, result: Result{Schema: ResultSchemaV1}}
}

func (j *jsonWriter) WriteMatch(pair MatchedPair) error {
	j.result.Matched = append(j.result.Matched, pair)
	return nil
}
func (j *jsonWriter) WriteAmountDiff(pair AmountDiffPair) error {
	j.result.AmountDiff = append(j.result.AmountDiff, pair)
	return nil
}
func (j *jsonWriter) WriteTimingDiff(pair TimingDiffPair) error {
	j.result.TimingDiff = append(j.result.TimingDiff, pair)
	return nil
}
func (j *jsonWriter) WriteUnmatched(tx Transaction, side string) error {
	if side == "left" {
		j.result.UnmatchedLeft = append(j.result.UnmatchedLeft, tx)
	} else {
		j.result.UnmatchedRight = append(j.result.UnmatchedRight, tx)
	}
	return nil
}
func (j *jsonWriter) WriteDuplicate(group DuplicateGroup) error {
	j.result.Duplicates = append(j.result.Duplicates, group)
	return nil
}
func (j *jsonWriter) WriteSummary(s Summary) error {
	j.result.Summary = s
	return nil
}

// WriteSourceSummary records a per-counterpart breakdown for 1-N source runs.
// Implements SourceBreakdownWriter.
func (j *jsonWriter) WriteSourceSummary(sourceName string, s Summary) error {
	if j.result.BySource == nil {
		j.result.BySource = make(map[string]Summary)
	}
	j.result.BySource[sourceName] = s
	return nil
}

// GroupedEventWriter implementation — appends to the result struct fields flushed by Flush().
func (j *jsonWriter) WriteGroupedMatch(pair GroupedMatchedPair) error {
	j.result.GroupedMatched = append(j.result.GroupedMatched, pair)
	return nil
}
func (j *jsonWriter) WriteGroupedAmountDiff(pair GroupedAmountDiffPair) error {
	j.result.GroupedAmountDiff = append(j.result.GroupedAmountDiff, pair)
	return nil
}
func (j *jsonWriter) WriteGroupedTimingDiff(pair GroupedTimingDiffPair) error {
	j.result.GroupedTimingDiff = append(j.result.GroupedTimingDiff, pair)
	return nil
}
func (j *jsonWriter) WriteAmbiguousGroup(pair AmbiguousGroupPair) error {
	j.result.AmbiguousGroups = append(j.result.AmbiguousGroups, pair)
	return nil
}

// ManyToManyEventWriter implementation — appends to the result struct fields flushed by Flush().
func (j *jsonWriter) WriteManyToManyMatch(pair ManyToManyMatchedPair) error {
	j.result.ManyToManyMatched = append(j.result.ManyToManyMatched, pair)
	return nil
}
func (j *jsonWriter) WriteManyToManyAmountDiff(pair ManyToManyAmountDiffPair) error {
	j.result.ManyToManyAmountDiff = append(j.result.ManyToManyAmountDiff, pair)
	return nil
}
func (j *jsonWriter) WriteManyToManyTimingDiff(pair ManyToManyTimingDiffPair) error {
	j.result.ManyToManyTimingDiff = append(j.result.ManyToManyTimingDiff, pair)
	return nil
}

// SubsetSumEventWriter implementation — appends to the result struct fields flushed by Flush().
func (j *jsonWriter) WriteSubsetSumMatch(pair SubsetSumMatchedPair) error {
	j.result.SubsetSumMatched = append(j.result.SubsetSumMatched, pair)
	return nil
}
func (j *jsonWriter) WriteSubsetSumAmbiguous(pair SubsetSumAmbiguousPair) error {
	j.result.SubsetSumAmbiguous = append(j.result.SubsetSumAmbiguous, pair)
	return nil
}
func (j *jsonWriter) WriteSubsetSumSkipped(pair SubsetSumSkippedPair) error {
	j.result.SubsetSumSkipped = append(j.result.SubsetSumSkipped, pair)
	return nil
}

func (j *jsonWriter) WriteFinancialEffectMatch(f FinancialEffectFinding) error {
	j.result.FinancialEffectMatches = append(j.result.FinancialEffectMatches, f)
	return nil
}
func (j *jsonWriter) WriteFinancialEffectDiff(f FinancialEffectFinding) error {
	j.result.FinancialEffectDiffs = append(j.result.FinancialEffectDiffs, f)
	return nil
}
func (j *jsonWriter) WriteFinancialUnchecked(f FinancialEffectFinding) error {
	j.result.FinancialUnchecked = append(j.result.FinancialUnchecked, f)
	return nil
}
func (j *jsonWriter) WriteSettlementMatch(f SettlementFinding) error {
	j.result.SettlementMatches = append(j.result.SettlementMatches, f)
	return nil
}
func (j *jsonWriter) WriteSettlementDiff(f SettlementFinding) error {
	j.result.SettlementDiffs = append(j.result.SettlementDiffs, f)
	return nil
}

// SetMeta sets pair and source names on the result. Fixes the pre-existing bug
// where PairName/LeftSource/RightSource were never populated in the JSON output.
func (j *jsonWriter) SetMeta(pairName, leftSource, rightSource string) {
	j.result.PairName = pairName
	j.result.LeftSource = leftSource
	j.result.RightSource = rightSource
}

// SetRunInfo stores the audit envelope for inclusion in the JSON output.
// Implements RunInfoSetter.
func (j *jsonWriter) SetRunInfo(info RunInfo) error {
	j.result.RunInfo = &info
	return nil
}

func (j *jsonWriter) SetIndexSelection(selection IndexSelection) error {
	j.result.IndexSelection = &selection
	return nil
}

// SetDeterministic enables stable output ordering. When true, Flush() sorts all
// result sections by a stable key before encoding. This adds O(n log n) sort time
// on the result set — typically 4-8 seconds at 17M matched rows.
func (j *jsonWriter) SetDeterministic(on bool) { j.deterministic = on }

// sortResult sorts all result sections in place for deterministic output.
func (j *jsonWriter) sortResult() {
	sort.SliceStable(j.result.Matched, func(i, k int) bool {
		return idLess(j.result.Matched[i].Left.ID, j.result.Matched[k].Left.ID)
	})
	sort.SliceStable(j.result.UnmatchedLeft, func(i, k int) bool {
		return idLess(j.result.UnmatchedLeft[i].ID, j.result.UnmatchedLeft[k].ID)
	})
	sort.SliceStable(j.result.UnmatchedRight, func(i, k int) bool {
		return idLess(j.result.UnmatchedRight[i].ID, j.result.UnmatchedRight[k].ID)
	})
	sort.SliceStable(j.result.AmountDiff, func(i, k int) bool {
		return idLess(j.result.AmountDiff[i].Left.ID, j.result.AmountDiff[k].Left.ID)
	})
	sort.SliceStable(j.result.TimingDiff, func(i, k int) bool {
		return idLess(j.result.TimingDiff[i].Left.ID, j.result.TimingDiff[k].Left.ID)
	})
	sort.SliceStable(j.result.Duplicates, func(i, k int) bool {
		if j.result.Duplicates[i].Reference != j.result.Duplicates[k].Reference {
			return j.result.Duplicates[i].Reference < j.result.Duplicates[k].Reference
		}
		return j.result.Duplicates[i].Source < j.result.Duplicates[k].Source
	})
	sort.SliceStable(j.result.GroupedMatched, func(i, k int) bool {
		return idLess(j.result.GroupedMatched[i].Left.ID, j.result.GroupedMatched[k].Left.ID)
	})
	sort.SliceStable(j.result.GroupedAmountDiff, func(i, k int) bool {
		return idLess(j.result.GroupedAmountDiff[i].Left.ID, j.result.GroupedAmountDiff[k].Left.ID)
	})
	sort.SliceStable(j.result.GroupedTimingDiff, func(i, k int) bool {
		return idLess(j.result.GroupedTimingDiff[i].Left.ID, j.result.GroupedTimingDiff[k].Left.ID)
	})
	sort.SliceStable(j.result.AmbiguousGroups, func(i, k int) bool {
		return j.result.AmbiguousGroups[i].Reference < j.result.AmbiguousGroups[k].Reference
	})
	sort.SliceStable(j.result.ManyToManyMatched, func(i, k int) bool {
		return idLess(firstTransactionID(j.result.ManyToManyMatched[i].Lefts), firstTransactionID(j.result.ManyToManyMatched[k].Lefts))
	})
	sort.SliceStable(j.result.ManyToManyAmountDiff, func(i, k int) bool {
		return idLess(firstTransactionID(j.result.ManyToManyAmountDiff[i].Lefts), firstTransactionID(j.result.ManyToManyAmountDiff[k].Lefts))
	})
	sort.SliceStable(j.result.ManyToManyTimingDiff, func(i, k int) bool {
		return idLess(firstTransactionID(j.result.ManyToManyTimingDiff[i].Lefts), firstTransactionID(j.result.ManyToManyTimingDiff[k].Lefts))
	})
	sort.SliceStable(j.result.SubsetSumMatched, func(i, k int) bool {
		return idLess(j.result.SubsetSumMatched[i].Left.ID, j.result.SubsetSumMatched[k].Left.ID)
	})
	sort.SliceStable(j.result.SubsetSumAmbiguous, func(i, k int) bool {
		return idLess(j.result.SubsetSumAmbiguous[i].Left.ID, j.result.SubsetSumAmbiguous[k].Left.ID)
	})
	sort.SliceStable(j.result.SubsetSumSkipped, func(i, k int) bool {
		return idLess(j.result.SubsetSumSkipped[i].Left.ID, j.result.SubsetSumSkipped[k].Left.ID)
	})
}

func (j *jsonWriter) Flush() error {
	if j.deterministic {
		j.sortResult()
	}
	enc := json.NewEncoder(j.w)
	enc.SetIndent("", "  ")
	return enc.Encode(j.result)
}

// GetResult returns the accumulated Result. Used by the batch Reconcile() wrapper.
func (j *jsonWriter) GetResult() *Result { return &j.result }

// idLess orders transaction IDs of the form "{source}-{row}" by source name and
// then by numeric row, so "left-2" sorts before "left-10" and deterministic output
// follows input row order. IDs that do not end in a numeric row fall back to plain
// string comparison.
func idLess(a, b string) bool {
	aPrefix, aRow, aOK := splitRowID(a)
	bPrefix, bRow, bOK := splitRowID(b)
	if !aOK || !bOK {
		return a < b
	}
	if aPrefix != bPrefix {
		return aPrefix < bPrefix
	}
	return aRow < bRow
}

func splitRowID(id string) (prefix string, row int, ok bool) {
	cut := strings.LastIndexByte(id, '-')
	if cut < 0 {
		return "", 0, false
	}
	row, err := strconv.Atoi(id[cut+1:])
	if err != nil {
		return "", 0, false
	}
	return id[:cut], row, true
}
