//nolint:staticcheck // Domain aliases keep package-internal signatures readable.
package output

import (
	"sort"
	"strings"

	. "github.com/reconifyhq/reconify/engine/domain"
)

// WriteResultEvents writes all match events from res to w without calling
// WriteSummary or Flush.
func WriteResultEvents(w ResultWriter, res *Result, suppressWarnings bool) error {
	for _, mp := range res.Matched {
		if err := w.WriteMatch(mp); err != nil {
			return err
		}
	}
	for _, ap := range res.AmountDiff {
		if err := w.WriteAmountDiff(ap); err != nil {
			return err
		}
	}
	for _, tp := range res.TimingDiff {
		if err := w.WriteTimingDiff(tp); err != nil {
			return err
		}
	}
	for _, tx := range res.UnmatchedLeft {
		if err := w.WriteUnmatched(tx, "left"); err != nil {
			return err
		}
	}
	for _, tx := range res.UnmatchedRight {
		if err := w.WriteUnmatched(tx, "right"); err != nil {
			return err
		}
	}
	for _, dg := range res.Duplicates {
		if err := w.WriteDuplicate(dg); err != nil {
			return err
		}
	}
	hasGrouped := len(res.GroupedMatched)+len(res.GroupedAmountDiff)+len(res.GroupedTimingDiff)+len(res.AmbiguousGroups) > 0
	if gw, ok := w.(GroupedEventWriter); ok {
		for _, gm := range res.GroupedMatched {
			if err := gw.WriteGroupedMatch(gm); err != nil {
				return err
			}
		}
		for _, gd := range res.GroupedAmountDiff {
			if err := gw.WriteGroupedAmountDiff(gd); err != nil {
				return err
			}
		}
		for _, gt := range res.GroupedTimingDiff {
			if err := gw.WriteGroupedTimingDiff(gt); err != nil {
				return err
			}
		}
		for _, ag := range res.AmbiguousGroups {
			if err := gw.WriteAmbiguousGroup(ag); err != nil {
				return err
			}
		}
	} else if hasGrouped && !suppressWarnings {
		observeWarning(w, Warning{Code: WarningUnsupportedGroupedEvents, Message: "current output format does not support grouped or ambiguous match events; use --format=json or --format=ndjson to capture all one_to_many output"})
	}
	hasManyToMany := len(res.ManyToManyMatched)+len(res.ManyToManyAmountDiff)+len(res.ManyToManyTimingDiff) > 0
	if mw, ok := w.(ManyToManyEventWriter); ok {
		for _, mm := range res.ManyToManyMatched {
			if err := mw.WriteManyToManyMatch(mm); err != nil {
				return err
			}
		}
		for _, md := range res.ManyToManyAmountDiff {
			if err := mw.WriteManyToManyAmountDiff(md); err != nil {
				return err
			}
		}
		for _, mt := range res.ManyToManyTimingDiff {
			if err := mw.WriteManyToManyTimingDiff(mt); err != nil {
				return err
			}
		}
	} else if hasManyToMany && !suppressWarnings {
		observeWarning(w, Warning{Code: WarningUnsupportedManyToManyEvents, Message: "current output format does not support many_to_many match events; use --format=json or --format=ndjson to capture all many_to_many output"})
	}
	hasSubsetSum := len(res.SubsetSumMatched)+len(res.SubsetSumAmbiguous)+len(res.SubsetSumSkipped) > 0
	if sw, ok := w.(SubsetSumEventWriter); ok {
		for _, sm := range res.SubsetSumMatched {
			if err := sw.WriteSubsetSumMatch(sm); err != nil {
				return err
			}
		}
		for _, sa := range res.SubsetSumAmbiguous {
			if err := sw.WriteSubsetSumAmbiguous(sa); err != nil {
				return err
			}
		}
		for _, sk := range res.SubsetSumSkipped {
			if err := sw.WriteSubsetSumSkipped(sk); err != nil {
				return err
			}
		}
	} else if hasSubsetSum && !suppressWarnings {
		observeWarning(w, Warning{Code: WarningUnsupportedSubsetSumEvents, Message: "current output format does not support subset_sum match events; use --format=json or --format=ndjson to capture all subset_sum output"})
	}
	if fw, ok := w.(FinancialEventWriter); ok {
		for _, finding := range res.FinancialEffectMatches {
			if err := fw.WriteFinancialEffectMatch(finding); err != nil {
				return err
			}
		}
		for _, finding := range res.FinancialEffectDiffs {
			if err := fw.WriteFinancialEffectDiff(finding); err != nil {
				return err
			}
		}
		for _, finding := range res.FinancialUnchecked {
			if err := fw.WriteFinancialUnchecked(finding); err != nil {
				return err
			}
		}
		for _, finding := range res.SettlementMatches {
			if err := fw.WriteSettlementMatch(finding); err != nil {
				return err
			}
		}
		for _, finding := range res.SettlementDiffs {
			if err := fw.WriteSettlementDiff(finding); err != nil {
				return err
			}
		}
	}
	for _, warning := range res.Warnings {
		observeWarning(w, Warning{Code: WarningEmptyCurrency, Message: warning})
	}
	if sbw, ok := w.(SourceBreakdownWriter); ok {
		for _, name := range sourceSummaryOrder(res) {
			if err := sbw.WriteSourceSummary(name, res.BySource[name]); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteResult writes all events, the summary, and flushes w.
func WriteResult(w ResultWriter, res *Result) error {
	if err := WriteResultEvents(w, res, false); err != nil {
		return err
	}
	if err := w.WriteSummary(res.Summary); err != nil {
		return err
	}
	return w.Flush()
}

func observeWarning(target any, warning Warning) {
	if observer, ok := target.(WarningObserver); ok {
		observer.ObserveWarning(warning)
	}
}

// sourceSummaryOrder returns the BySource keys in a stable order: counterpart
// order as recorded in RightSource (the comma-joined configured order), then any
// remaining keys alphabetically. Ranging over the map directly would reorder
// source_summary events on every run.
func sourceSummaryOrder(res *Result) []string {
	names := make([]string, 0, len(res.BySource))
	listed := make(map[string]bool, len(res.BySource))
	for _, name := range strings.Split(res.RightSource, ",") {
		if _, ok := res.BySource[name]; ok && !listed[name] {
			listed[name] = true
			names = append(names, name)
		}
	}
	rest := make([]string, 0, len(res.BySource)-len(names))
	for name := range res.BySource {
		if !listed[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(names, rest...)
}
