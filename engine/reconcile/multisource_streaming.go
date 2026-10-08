//nolint:revive // This package preserves stable compatibility names.
//nolint:staticcheck // Domain aliases keep package-internal signatures readable.
package reconcile

import (
	"context"
	"fmt"

	//nolint:staticcheck // Domain types are deliberately imported into the implementation namespace.
	. "github.com/reconifyhq/reconify/engine/domain"
	"github.com/reconifyhq/reconify/engine/matching"
	"github.com/reconifyhq/reconify/engine/parser"

	"math"
	"strings"

	"github.com/reconifyhq/reconify/config"
	engineTelemetry "github.com/reconifyhq/reconify/engine/telemetry"
)

// CounterpartStream describes one streaming counterpart pass and its caller-owned
// right-side index.
type CounterpartStream struct {
	SourceName string
	RightPath  string
	RightCfg   config.CSVParserCfg
	Index      RightIndex
}

// ReconcileStreamingMultiSource is the streaming equivalent of ReconcileMultiSource.
// The left file is streamed from disk exactly once, during the first counterpart's
// pass — matched/amount-diff/timing-diff/right-unmatched/duplicate events are
// written to w immediately, the same as ReconcileStreaming. Left transactions that
// remain unmatched after that pass are buffered in memory (bounded by
// maxLeftBuffer, advisory like ReconcileStreaming's token-mode buffer) and
// replayed against each subsequent counterpart's index in turn, with no second
// read of the left file.
//
// name_mode=tokens is not supported here yet — token-mode buffering across
// multiple counterpart passes is unimplemented; pass NameMode="none" or "".
//
// Single-counterpart callers should use ReconcileStreaming directly — it is
// unchanged and remains the documented large-file path. This function is for
// len(counterparts) > 1 only.
func ReconcileStreamingMultiSource(
	ctx context.Context,
	pairName string,
	leftSource string,
	leftPath string,
	leftCfg config.CSVParserCfg,
	counterparts []CounterpartStream,
	pair config.Pair,
	w ResultWriter,
	maxLeftBuffer int,
) error {
	return reconcileStreamingMultiSource(ctx, pairName, leftSource, leftPath, leftCfg, counterparts, pair, w, maxLeftBuffer, nil)
}

// ReconcileStreamingMultiSourceWithTelemetry is the streaming multi-source
// entry point with typed, best-effort engineTelemetry.
func ReconcileStreamingMultiSourceWithTelemetry(
	ctx context.Context,
	pairName string,
	leftSource string,
	leftPath string,
	leftCfg config.CSVParserCfg,
	counterparts []CounterpartStream,
	pair config.Pair,
	w ResultWriter,
	maxLeftBuffer int,
	telemetry TelemetryOptions,
) error {
	reporter := engineTelemetry.NewReporter(telemetry)
	if reporter != nil {
		defer reporter.Close()
	}
	err := reconcileStreamingMultiSource(ctx, pairName, leftSource, leftPath, leftCfg, counterparts, pair, w, maxLeftBuffer, reporter)
	if err != nil {
		reporter.Fail(0)
	}
	return err
}

func reconcileStreamingMultiSource(
	ctx context.Context,
	pairName string,
	leftSource string,
	leftPath string,
	leftCfg config.CSVParserCfg,
	counterparts []CounterpartStream,
	pair config.Pair,
	w ResultWriter,
	maxLeftBuffer int,
	reporter *engineTelemetry.Reporter,
) error {
	if err := validateCollaborator("result writer", w); err != nil {
		return err
	}
	if len(counterparts) == 0 {
		return fmt.Errorf("at least one counterpart source is required")
	}
	if err := validateCounterpartStreams(counterparts); err != nil {
		return err
	}
	if pair.NameMode == "tokens" {
		return fmt.Errorf("name_mode=tokens is not yet supported for multi-source (rights) reconciliation")
	}
	if containsPass(pair.Passes, config.PassTypeOneToMany) || containsPass(pair.Passes, config.PassTypeManyToMany) {
		return fmt.Errorf("grouped passes are not supported in the streaming " +
			"multi-source path — the CLI routes to batch automatically when " +
			"one_to_many or many_to_many is configured")
	}

	cc := &matching.CurrencyTracker{}
	bySource := make(map[string]Summary, len(counterparts))
	names := make([]string, 0, len(counterparts))

	var (
		totalLeft, totalRight                                               int
		matchedCount, amountDiffCount, timingDiffCount, unmatchedRightCount int
		financialMatchCount, financialDiffCount, financialUncheckedCount    int
		settlementMatchCount, settlementDiffCount                           int
		matchedAmtLeft, matchedAmtRight, unmatchedAmtRight, amountDiffTotal int64
		dupTxnCount                                                         int
		leftover                                                            []Transaction
	)

	for i, cp := range counterparts {
		fromFile := i == 0
		var rows []Transaction
		if !fromFile {
			rows = leftover
		}

		passLeftover, passSummary, leftDups, err := runStreamingPass(
			ctx, leftSource, cp.SourceName, fromFile, leftPath, leftCfg, rows,
			cp.RightPath, cp.RightCfg, cp.Index, pair, cc, w,
			reporter,
		)
		if err != nil {
			return fmt.Errorf("counterpart %q: %w", cp.SourceName, err)
		}

		names = append(names, cp.SourceName)
		bySource[cp.SourceName] = passSummary

		if fromFile {
			totalLeft = passSummary.TotalLeft
		}
		totalRight += passSummary.TotalRight
		matchedCount += passSummary.MatchedCount
		amountDiffCount += passSummary.AmountDiffCount
		timingDiffCount += passSummary.TimingDiffCount
		unmatchedRightCount += passSummary.UnmatchedRight
		matchedAmtLeft += passSummary.MatchedAmountLeft
		matchedAmtRight += passSummary.MatchedAmountRight
		unmatchedAmtRight += passSummary.UnmatchedAmountRight
		amountDiffTotal += passSummary.AmountDiffTotal
		financialMatchCount += passSummary.FinancialEffectMatchCount
		financialDiffCount += passSummary.FinancialEffectDiffCount
		financialUncheckedCount += passSummary.FinancialUncheckedCount
		settlementMatchCount += passSummary.SettlementMatchCount
		settlementDiffCount += passSummary.SettlementDiffCount
		dupTxnCount += passSummary.DuplicateCount // right-side dups for this counterpart

		for _, g := range leftDups {
			dupTxnCount += len(g.Transactions)
			if err := w.WriteDuplicate(g); err != nil {
				return err
			}
		}

		leftover = passLeftover
		if maxLeftBuffer > 0 && len(leftover) > maxLeftBuffer {
			observeWarning(w, reporter, Warning{Code: WarningCarryBufferPressure, Message: fmt.Sprintf("multi-source carry-forward buffer is %d rows (limit %d) after counterpart %q; memory usage may be high", len(leftover), maxLeftBuffer, cp.SourceName)})
		}
	}

	// Whatever remains after the final pass is the true final unmatched_left.
	var unmatchedAmtLeft int64
	for _, tx := range leftover {
		unmatchedAmtLeft += tx.Amount
		if err := w.WriteUnmatched(tx, "left"); err != nil {
			return err
		}
	}

	for _, warning := range cc.Warnings() {
		observeWarning(w, reporter, Warning{Code: WarningEmptyCurrency, Message: warning})
	}

	total := totalLeft
	if totalRight > total {
		total = totalRight
	}
	matchRate := 0.0
	if total > 0 {
		matchRate = math.Round(float64(matchedCount)/float64(total)*10000) / 100
	}
	reconciledRate := 0.0
	if total > 0 {
		reconciledRate = math.Round(float64(matchedCount+amountDiffCount+timingDiffCount)/float64(total)*10000) / 100
	}

	aggregate := Summary{
		Currency:                  cc.Currency(),
		TotalLeft:                 totalLeft,
		TotalRight:                totalRight,
		MatchedCount:              matchedCount,
		UnmatchedLeft:             len(leftover),
		UnmatchedRight:            unmatchedRightCount,
		AmountDiffCount:           amountDiffCount,
		TimingDiffCount:           timingDiffCount,
		DuplicateCount:            dupTxnCount,
		MatchRatePct:              matchRate,
		ReconciledRatePct:         reconciledRate,
		MatchedAmountLeft:         matchedAmtLeft,
		MatchedAmountRight:        matchedAmtRight,
		UnmatchedAmountLeft:       unmatchedAmtLeft,
		UnmatchedAmountRight:      unmatchedAmtRight,
		AmountDiffTotal:           amountDiffTotal,
		FinancialEffectMatchCount: financialMatchCount,
		FinancialEffectDiffCount:  financialDiffCount,
		FinancialUncheckedCount:   financialUncheckedCount,
		SettlementMatchCount:      settlementMatchCount,
		SettlementDiffCount:       settlementDiffCount,
		TotalDiscrepancy:          unmatchedAmtLeft + unmatchedAmtRight + amountDiffTotal,
	}

	if sbw, ok := w.(SourceBreakdownWriter); ok {
		for _, name := range names {
			if err := sbw.WriteSourceSummary(name, bySource[name]); err != nil {
				return err
			}
		}
	}

	reporter.Start("finalization", leftSource, strings.Join(names, ","), nil)
	reporter.Progress(totalLeft + totalRight)
	if err := w.WriteSummary(aggregate); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	reporter.Complete(totalLeft + totalRight)
	return nil
}

// runStreamingPass executes one counterpart pass: indexes the counterpart's right
// file, tracks right-side duplicates, then matches left rows — either streamed
// from leftPath (fromFile=true, used only for the first pass) or replayed from
// leftRows (every subsequent pass) — against that index using the same
// decideMatch best-candidate selection as ReconcileStreaming. Matched/amount-diff/
// timing-diff/right-duplicate/right-unmatched events are written to w immediately,
// since they are final for this counterpart. Left-unmatched rows are returned
// instead of written, so the driver can decide whether to carry them into the
// next pass or write them out as the final unmatched_left.
//
// Left-side duplicate detection only runs when fromFile=true: that is the one
// pass that sees every original left row exactly once, so it's the only place
// duplicate annotation can run without re-reporting the same group once per pass
// it survives into.
func runStreamingPass(
	ctx context.Context,
	leftSource, rightSource string,
	fromFile bool,
	leftPath string,
	leftCfg config.CSVParserCfg,
	leftRows []Transaction,
	rightPath string,
	rightCfg config.CSVParserCfg,
	idx RightIndex,
	pair config.Pair,
	cc *matching.CurrencyTracker,
	w ResultWriter,
	reporter *engineTelemetry.Reporter,
) (leftover []Transaction, summary Summary, leftDups []DuplicateGroup, err error) {
	dateWindowDays, err := matching.ParseDateWindow(pair.DateWindow)
	if err != nil {
		return nil, Summary{}, nil, fmt.Errorf("invalid date_window: %w", err)
	}
	tolerance := pair.AmountToleranceMinor

	rightCfgNoRaw := rightCfg
	rightCfgNoRaw.SkipRaw = true

	rightPolicy := rightCfg.ResolvedDuplicatePolicy()
	rightSeen := make(map[string]uint8)
	rightDupKeys := make(map[string]bool)
	rightMergeSeen := make(map[string]bool)
	var rightLatestBuf *latestBuffer
	if rightPolicy == config.DuplicatePolicyLatest {
		rightLatestBuf = newLatestBuffer()
	}
	var totalRight int
	reporter.Start("right_index", rightSource, rightSource, nil)

	if perr := parser.ParseCSVEach(ctx, rightSource, rightPath, rightCfgNoRaw, func(tx Transaction, _ int) error {
		totalRight++
		reporter.Progress(totalRight)
		if oerr := cc.Observe(rightSource, tx); oerr != nil {
			return oerr
		}
		switch rightPolicy {
		case config.DuplicatePolicyFlag:
			if tx.GroupKey != "" {
				if rightSeen[tx.GroupKey] < 2 {
					rightSeen[tx.GroupKey]++
				}
				if rightSeen[tx.GroupKey] == 2 {
					rightDupKeys[tx.GroupKey] = true
				}
			}
			return idx.Add(tx)
		case config.DuplicatePolicyKeep:
			return idx.Add(tx)
		case config.DuplicatePolicyMerge:
			if tx.GroupKey != "" && rightMergeSeen[tx.GroupKey] {
				return nil
			}
			if tx.GroupKey != "" {
				rightMergeSeen[tx.GroupKey] = true
			}
			return idx.Add(tx)
		case config.DuplicatePolicyLatest:
			if tx.GroupKey != "" {
				rightLatestBuf.put(tx.GroupKey, tx)
				return nil
			}
			return idx.Add(tx)
		}
		return idx.Add(tx)
	}); perr != nil {
		return nil, Summary{}, nil, fmt.Errorf("parse right source %q: %w", rightSource, perr)
	}
	if rightPolicy == config.DuplicatePolicyLatest {
		if aerr := rightLatestBuf.each(idx.Add); aerr != nil {
			return nil, Summary{}, nil, aerr
		}
	}
	reporter.Complete(totalRight)

	dupTxnCount := 0
	if rightPolicy == config.DuplicatePolicyFlag && len(rightDupKeys) > 0 {
		reporter.Start("right_duplicate_scan", rightSource, rightSource, nil)
		groups, derr := matching.CollectDuplicates(ctx, rightSource, rightPath, rightCfg, rightDupKeys)
		if derr != nil {
			return nil, Summary{}, nil, fmt.Errorf("collect right duplicates: %w", derr)
		}
		for _, g := range groups {
			dupTxnCount += len(g.Transactions)
			reporter.Progress(dupTxnCount)
			if werr := w.WriteDuplicate(g); werr != nil {
				return nil, Summary{}, nil, werr
			}
		}
		reporter.Complete(dupTxnCount)
	}

	leftPolicy := leftCfg.ResolvedDuplicatePolicy()
	leftSeen := make(map[string]uint8)
	leftDupKeys := make(map[string]bool)
	leftMergeSeen := make(map[string]bool)
	var leftLatestBuf *latestBuffer
	if fromFile && leftPolicy == config.DuplicatePolicyLatest {
		leftLatestBuf = newLatestBuffer()
	}

	var (
		matchedCount, amountDiffCount, timingDiffCount, unmatchedRightCount int
		matchedAmtLeft, matchedAmtRight                                     int64
		unmatchedAmtLeft, unmatchedAmtRight, amountDiffTotal                int64
		totalLeft                                                           int
	)

	// matchLeft contains only matching and outcome accounting. The first pass
	// calls it for ordinary rows, while latest representatives call it directly
	// after the scan so replay cannot re-enter the deduplication buffer.
	matchLeft := func(ltx Transaction) error {
		decision, derr := decideMatch(ltx, idx, tolerance, dateWindowDays)
		if derr != nil {
			return derr
		}
		switch decision.outcome {
		case outcomeExact:
			if merr := idx.MarkUsed(decision.right); merr != nil {
				return fmt.Errorf("mark used: %w", merr)
			}
			matchedCount++
			matchedAmtLeft += ltx.Amount
			matchedAmtRight += decision.rightTx.Amount
			return w.WriteMatch(MatchedPair{Left: ltx, Right: decision.rightTx})
		case outcomeAmountDiff:
			if merr := idx.MarkUsed(decision.right); merr != nil {
				return fmt.Errorf("mark used: %w", merr)
			}
			amountDiffCount++
			if decision.amountDiffMinor < 0 {
				amountDiffTotal += -decision.amountDiffMinor
			} else {
				amountDiffTotal += decision.amountDiffMinor
			}
			return w.WriteAmountDiff(AmountDiffPair{
				Left:      ltx,
				Right:     decision.rightTx,
				DiffMinor: decision.amountDiffMinor,
			})
		case outcomeTimingDiff:
			if merr := idx.MarkUsed(decision.right); merr != nil {
				return fmt.Errorf("mark used: %w", merr)
			}
			timingDiffCount++
			return w.WriteTimingDiff(TimingDiffPair{
				Left:     ltx,
				Right:    decision.rightTx,
				DaysDiff: decision.daysDiff,
			})
		default:
			unmatchedAmtLeft += ltx.Amount
			leftover = append(leftover, ltx)
			return nil
		}
	}

	processLeft := func(ltx Transaction) error {
		totalLeft++
		reporter.Progress(totalLeft)
		if fromFile {
			if oerr := cc.Observe(leftSource, ltx); oerr != nil {
				return oerr
			}
			if leftPolicy == config.DuplicatePolicyFlag && ltx.GroupKey != "" {
				if leftSeen[ltx.GroupKey] < 2 {
					leftSeen[ltx.GroupKey]++
				}
				if leftSeen[ltx.GroupKey] == 2 {
					leftDupKeys[ltx.GroupKey] = true
				}
			}
			// Dedup for merge: skip if GroupKey already seen (first-seen wins).
			if leftPolicy == config.DuplicatePolicyMerge && ltx.GroupKey != "" {
				if leftMergeSeen[ltx.GroupKey] {
					return nil
				}
				leftMergeSeen[ltx.GroupKey] = true
			}
			// Dedup for latest: buffer and defer processing until after scan.
			if leftPolicy == config.DuplicatePolicyLatest && ltx.GroupKey != "" {
				leftLatestBuf.put(ltx.GroupKey, ltx)
				return nil
			}
		}
		return matchLeft(ltx)
	}

	reporter.Start("left_match", leftSource, rightSource, nil)
	if fromFile {
		if perr := parser.ParseCSVEach(ctx, leftSource, leftPath, leftCfg, func(tx Transaction, _ int) error {
			return processLeft(tx)
		}); perr != nil {
			return nil, Summary{}, nil, fmt.Errorf("parse left source: %w", perr)
		}
	} else {
		for _, tx := range leftRows {
			if ctx.Err() != nil {
				return nil, Summary{}, nil, ctx.Err()
			}
			if perr := processLeft(tx); perr != nil {
				return nil, Summary{}, nil, perr
			}
		}
	}
	// For "latest" (first pass only): process buffered left rows now that the scan
	// is complete and we know which row is last for each GroupKey.
	if fromFile && leftPolicy == config.DuplicatePolicyLatest {
		// These rows were already counted and currency-validated during the file
		// scan; replay only the final representative for each non-empty group key.
		if perr := leftLatestBuf.each(func(ltx Transaction) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return matchLeft(ltx)
		}); perr != nil {
			return nil, Summary{}, nil, perr
		}
	}
	reporter.Complete(totalLeft)

	if fromFile && leftPolicy == config.DuplicatePolicyFlag && len(leftDupKeys) > 0 {
		reporter.Start("left_duplicate_scan", leftSource, rightSource, nil)
		groups, derr := matching.CollectDuplicates(ctx, leftSource, leftPath, leftCfg, leftDupKeys)
		if derr != nil {
			return nil, Summary{}, nil, fmt.Errorf("collect left duplicates: %w", derr)
		}
		leftDups = groups
		reporter.Complete(len(groups))
	}

	if ierr := idx.IterateUnused(func(tx Transaction) error {
		unmatchedRightCount++
		unmatchedAmtRight += tx.Amount
		return w.WriteUnmatched(tx, "right")
	}); ierr != nil {
		return nil, Summary{}, nil, ierr
	}

	total := totalLeft
	if totalRight > total {
		total = totalRight
	}
	matchRate := 0.0
	if total > 0 {
		matchRate = math.Round(float64(matchedCount)/float64(total)*10000) / 100
	}
	reconciledRate := 0.0
	if total > 0 {
		reconciledRate = math.Round(float64(matchedCount+amountDiffCount+timingDiffCount)/float64(total)*10000) / 100
	}

	summary = Summary{
		TotalLeft:            totalLeft,
		TotalRight:           totalRight,
		MatchedCount:         matchedCount,
		UnmatchedLeft:        len(leftover),
		UnmatchedRight:       unmatchedRightCount,
		AmountDiffCount:      amountDiffCount,
		TimingDiffCount:      timingDiffCount,
		DuplicateCount:       dupTxnCount,
		MatchRatePct:         matchRate,
		ReconciledRatePct:    reconciledRate,
		MatchedAmountLeft:    matchedAmtLeft,
		MatchedAmountRight:   matchedAmtRight,
		UnmatchedAmountLeft:  unmatchedAmtLeft,
		UnmatchedAmountRight: unmatchedAmtRight,
		AmountDiffTotal:      amountDiffTotal,
		TotalDiscrepancy:     unmatchedAmtLeft + unmatchedAmtRight + amountDiffTotal,
	}

	return leftover, summary, leftDups, nil
}
