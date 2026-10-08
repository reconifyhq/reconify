//nolint:staticcheck // Domain aliases keep package-internal signatures readable.
package output

import (
	"github.com/reconifyhq/reconify/config"
	. "github.com/reconifyhq/reconify/engine/domain"
)

// filteredWriter wraps a ResultWriter and suppresses events based on ResultMode.
// It implements all optional writer interfaces so the caller can treat it as a
// drop-in replacement for the inner writer. Optional interface calls are forwarded
// to the inner writer only when the inner writer supports them.
type filteredWriter struct {
	inner          ResultWriter
	mode           config.ResultMode
	runID          string
	warnings       WarningObserver
	warnedNoGroup  bool
	warnedNoM2M    bool
	warnedNoSubset bool
}

func (f *filteredWriter) ObserveWarning(warning Warning) {
	if f.warnings != nil {
		f.warnings.ObserveWarning(warning)
	}
	observeWarning(f.inner, warning)
}

// WrapWithResultMode returns a ResultWriter that filters events according to mode
// and patches Summary.ResultMode and Summary.RunID before delegating to inner.
// When mode is empty it defaults to ResultModeAll (no filtering, but metadata
// is still patched into the Summary).
func WrapWithResultMode(inner ResultWriter, mode config.ResultMode, runID string) ResultWriter {
	return WrapWithResultModeAndWarnings(inner, mode, runID, nil)
}

// WrapWithResultModeAndWarnings additionally routes non-fatal engine warnings
// to an optional caller-owned observer.
func WrapWithResultModeAndWarnings(inner ResultWriter, mode config.ResultMode, runID string, warnings WarningObserver) ResultWriter {
	if mode == "" {
		mode = config.ResultModeAll
	}
	return &filteredWriter{inner: inner, mode: mode, runID: runID, warnings: warnings}
}

// --- ResultWriter (required) ---

func (f *filteredWriter) WriteMatch(p MatchedPair) error {
	if f.mode == config.ResultModeSummaryOnly || f.mode == config.ResultModeExceptionsOnly {
		return nil
	}
	return f.inner.WriteMatch(p)
}

func (f *filteredWriter) WriteAmountDiff(p AmountDiffPair) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	return f.inner.WriteAmountDiff(p)
}

func (f *filteredWriter) WriteTimingDiff(p TimingDiffPair) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	return f.inner.WriteTimingDiff(p)
}

func (f *filteredWriter) WriteUnmatched(tx Transaction, side string) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	return f.inner.WriteUnmatched(tx, side)
}

func (f *filteredWriter) WriteDuplicate(g DuplicateGroup) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	return f.inner.WriteDuplicate(g)
}

func (f *filteredWriter) WriteSummary(s Summary) error {
	if f.mode != config.ResultModeAll {
		s.ResultMode = string(f.mode)
	}
	if f.runID != "" {
		s.RunID = f.runID
	}
	return f.inner.WriteSummary(s)
}

func (f *filteredWriter) Flush() error { return f.inner.Flush() }

func (f *filteredWriter) WriteFinancialEffectMatch(v FinancialEffectFinding) error {
	if f.mode != config.ResultModeAll {
		return nil
	}
	if w, ok := f.inner.(FinancialEventWriter); ok {
		return w.WriteFinancialEffectMatch(v)
	}
	return nil
}
func (f *filteredWriter) WriteFinancialEffectDiff(v FinancialEffectFinding) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	if w, ok := f.inner.(FinancialEventWriter); ok {
		return w.WriteFinancialEffectDiff(v)
	}
	return nil
}
func (f *filteredWriter) WriteFinancialUnchecked(v FinancialEffectFinding) error {
	if f.mode != config.ResultModeAll {
		return nil
	}
	if w, ok := f.inner.(FinancialEventWriter); ok {
		return w.WriteFinancialUnchecked(v)
	}
	return nil
}

func (f *filteredWriter) WriteSettlementMatch(v SettlementFinding) error {
	if f.mode != config.ResultModeAll {
		return nil
	}
	if w, ok := f.inner.(FinancialEventWriter); ok {
		return w.WriteSettlementMatch(v)
	}
	return nil
}
func (f *filteredWriter) WriteSettlementDiff(v SettlementFinding) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	if w, ok := f.inner.(FinancialEventWriter); ok {
		return w.WriteSettlementDiff(v)
	}
	return nil
}

// --- RunInfoSetter (optional) ---

func (f *filteredWriter) SetRunInfo(info RunInfo) error {
	if setter, ok := f.inner.(RunInfoSetter); ok {
		return setter.SetRunInfo(info)
	}
	return nil
}

// --- MetaSetter / DeterministicSetter (optional) ---

// SetMeta forwards pair and source names to inner writers that record them.
func (f *filteredWriter) SetMeta(pairName, leftSource, rightSource string) {
	if setter, ok := f.inner.(interface {
		SetMeta(pairName, leftSource, rightSource string)
	}); ok {
		setter.SetMeta(pairName, leftSource, rightSource)
	}
}

// SetDeterministic forwards stable-ordering mode to inner writers that support
// it. The wrapper always satisfies the setter interface, so when the inner format
// cannot honor the request it reports that through the warning observer only; the
// warning is never written into the result stream.
func (f *filteredWriter) SetDeterministic(on bool) {
	if setter, ok := f.inner.(interface{ SetDeterministic(bool) }); ok {
		setter.SetDeterministic(on)
		return
	}
	if on && f.warnings != nil {
		f.warnings.ObserveWarning(Warning{
			Code:    WarningDeterministicUnsupported,
			Message: "--deterministic has no effect for this output format; use --format=json",
		})
	}
}

// --- IndexSelectionSetter (optional) ---

func (f *filteredWriter) SetIndexSelection(selection IndexSelection) error {
	if setter, ok := f.inner.(IndexSelectionSetter); ok {
		return setter.SetIndexSelection(selection)
	}
	return nil
}

// --- SourceBreakdownWriter (optional) ---

func (f *filteredWriter) WriteSourceSummary(sourceName string, sum Summary) error {
	if sbw, ok := f.inner.(SourceBreakdownWriter); ok {
		return sbw.WriteSourceSummary(sourceName, sum)
	}
	return nil
}

// --- GroupedEventWriter (optional) ---

func (f *filteredWriter) WriteGroupedMatch(p GroupedMatchedPair) error {
	if f.mode == config.ResultModeSummaryOnly || f.mode == config.ResultModeExceptionsOnly {
		return nil
	}
	if gw, ok := f.inner.(GroupedEventWriter); ok {
		return gw.WriteGroupedMatch(p)
	}
	if !f.warnedNoGroup {
		f.warnedNoGroup = true
		f.ObserveWarning(Warning{Code: WarningUnsupportedGroupedEvents, Message: "current output format does not support grouped or ambiguous match events; use --format=json or --format=ndjson to capture all one_to_many output"})
	}
	return nil
}

func (f *filteredWriter) WriteGroupedAmountDiff(p GroupedAmountDiffPair) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	if gw, ok := f.inner.(GroupedEventWriter); ok {
		return gw.WriteGroupedAmountDiff(p)
	}
	if !f.warnedNoGroup {
		f.warnedNoGroup = true
		f.ObserveWarning(Warning{Code: WarningUnsupportedGroupedEvents, Message: "current output format does not support grouped or ambiguous match events; use --format=json or --format=ndjson to capture all one_to_many output"})
	}
	return nil
}

func (f *filteredWriter) WriteGroupedTimingDiff(p GroupedTimingDiffPair) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	if gw, ok := f.inner.(GroupedEventWriter); ok {
		return gw.WriteGroupedTimingDiff(p)
	}
	if !f.warnedNoGroup {
		f.warnedNoGroup = true
		f.ObserveWarning(Warning{Code: WarningUnsupportedGroupedEvents, Message: "current output format does not support grouped or ambiguous match events; use --format=json or --format=ndjson to capture all one_to_many output"})
	}
	return nil
}

func (f *filteredWriter) WriteAmbiguousGroup(p AmbiguousGroupPair) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	if gw, ok := f.inner.(GroupedEventWriter); ok {
		return gw.WriteAmbiguousGroup(p)
	}
	if !f.warnedNoGroup {
		f.warnedNoGroup = true
		f.ObserveWarning(Warning{Code: WarningUnsupportedGroupedEvents, Message: "current output format does not support grouped or ambiguous match events; use --format=json or --format=ndjson to capture all one_to_many output"})
	}
	return nil
}

// --- ManyToManyEventWriter (optional) ---

func (f *filteredWriter) WriteManyToManyMatch(p ManyToManyMatchedPair) error {
	if f.mode == config.ResultModeSummaryOnly || f.mode == config.ResultModeExceptionsOnly {
		return nil
	}
	if mw, ok := f.inner.(ManyToManyEventWriter); ok {
		return mw.WriteManyToManyMatch(p)
	}
	if !f.warnedNoM2M {
		f.warnedNoM2M = true
		f.ObserveWarning(Warning{Code: WarningUnsupportedManyToManyEvents, Message: "current output format does not support many_to_many match events; use --format=json or --format=ndjson to capture all many_to_many output"})
	}
	return nil
}

func (f *filteredWriter) WriteManyToManyAmountDiff(p ManyToManyAmountDiffPair) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	if mw, ok := f.inner.(ManyToManyEventWriter); ok {
		return mw.WriteManyToManyAmountDiff(p)
	}
	if !f.warnedNoM2M {
		f.warnedNoM2M = true
		f.ObserveWarning(Warning{Code: WarningUnsupportedManyToManyEvents, Message: "current output format does not support many_to_many match events; use --format=json or --format=ndjson to capture all many_to_many output"})
	}
	return nil
}

func (f *filteredWriter) WriteManyToManyTimingDiff(p ManyToManyTimingDiffPair) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	if mw, ok := f.inner.(ManyToManyEventWriter); ok {
		return mw.WriteManyToManyTimingDiff(p)
	}
	if !f.warnedNoM2M {
		f.warnedNoM2M = true
		f.ObserveWarning(Warning{Code: WarningUnsupportedManyToManyEvents, Message: "current output format does not support many_to_many match events; use --format=json or --format=ndjson to capture all many_to_many output"})
	}
	return nil
}

// --- SubsetSumEventWriter (optional) ---

func (f *filteredWriter) WriteSubsetSumMatch(p SubsetSumMatchedPair) error {
	if f.mode == config.ResultModeSummaryOnly || f.mode == config.ResultModeExceptionsOnly {
		return nil
	}
	if sw, ok := f.inner.(SubsetSumEventWriter); ok {
		return sw.WriteSubsetSumMatch(p)
	}
	if !f.warnedNoSubset {
		f.warnedNoSubset = true
		f.ObserveWarning(Warning{Code: WarningUnsupportedSubsetSumEvents, Message: "current output format does not support subset_sum match events; use --format=json or --format=ndjson to capture all subset_sum output"})
	}
	return nil
}

func (f *filteredWriter) WriteSubsetSumAmbiguous(p SubsetSumAmbiguousPair) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	if sw, ok := f.inner.(SubsetSumEventWriter); ok {
		return sw.WriteSubsetSumAmbiguous(p)
	}
	if !f.warnedNoSubset {
		f.warnedNoSubset = true
		f.ObserveWarning(Warning{Code: WarningUnsupportedSubsetSumEvents, Message: "current output format does not support subset_sum match events; use --format=json or --format=ndjson to capture all subset_sum output"})
	}
	return nil
}

func (f *filteredWriter) WriteSubsetSumSkipped(p SubsetSumSkippedPair) error {
	if f.mode == config.ResultModeSummaryOnly {
		return nil
	}
	if sw, ok := f.inner.(SubsetSumEventWriter); ok {
		return sw.WriteSubsetSumSkipped(p)
	}
	if !f.warnedNoSubset {
		f.warnedNoSubset = true
		f.ObserveWarning(Warning{Code: WarningUnsupportedSubsetSumEvents, Message: "current output format does not support subset_sum match events; use --format=json or --format=ndjson to capture all subset_sum output"})
	}
	return nil
}
