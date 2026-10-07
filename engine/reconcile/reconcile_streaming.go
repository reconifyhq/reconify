//nolint:revive // This package preserves stable compatibility names.
//nolint:staticcheck // Domain aliases keep package-internal signatures readable.
package reconcile

import (
	"context"
	"fmt"

	//nolint:staticcheck // Domain types are deliberately imported into the implementation namespace.
	. "github.com/reconifyhq/reconify/engine/domain"
	"github.com/reconifyhq/reconify/engine/matching"

	"math"
	"time"

	"github.com/reconifyhq/reconify/config"
	engineTelemetry "github.com/reconifyhq/reconify/engine/telemetry"
)

// ReconcileStreaming reconciles two configured input files through a caller-owned
// right-side index while emitting result events to w.
func ReconcileStreaming(
	ctx context.Context,
	pairName string,
	leftSource string,
	rightSource string,
	leftPath string,
	rightPath string,
	leftCfg config.ParserCfg,
	rightCfg config.ParserCfg,
	pair config.Pair,
	idx RightIndex,
	w ResultWriter,
	maxTokenBuffer int,
) error {
	return reconcileStreaming(ctx, pairName, leftSource, rightSource, leftPath, rightPath, leftCfg, rightCfg, pair, idx, w, maxTokenBuffer, nil)
}

// ReconcileStreamingWithProgress is identical to ReconcileStreaming, but emits progress
// updates every progressEvery rows when progress != nil. If progressEvery <= 0,
// it defaults to 1,000,000 rows.
func ReconcileStreamingWithProgress(
	ctx context.Context,
	pairName string,
	leftSource string,
	rightSource string,
	leftPath string,
	rightPath string,
	leftCfg config.ParserCfg,
	rightCfg config.ParserCfg,
	pair config.Pair,
	idx RightIndex,
	w ResultWriter,
	maxTokenBuffer int,
	progress ProgressFunc,
	progressEvery int,
) error {
	reporter := engineTelemetry.NewReporter(TelemetryOptions{
		ProgressEvery: progressEvery,
		Sink:          legacyProgressSink(progress),
	})
	defer reporter.Close()
	return reconcileStreaming(ctx, pairName, leftSource, rightSource, leftPath, rightPath, leftCfg, rightCfg, pair, idx, w, maxTokenBuffer, reporter)
}

// ReconcileStreamingWithTelemetry emits typed lifecycle telemetry while preserving
// the streaming reconciliation and ResultWriter behavior of ReconcileStreaming.
func ReconcileStreamingWithTelemetry(
	ctx context.Context,
	pairName string,
	leftSource string,
	rightSource string,
	leftPath string,
	rightPath string,
	leftCfg config.ParserCfg,
	rightCfg config.ParserCfg,
	pair config.Pair,
	idx RightIndex,
	w ResultWriter,
	maxTokenBuffer int,
	telemetry TelemetryOptions,
) error {
	reporter := engineTelemetry.NewReporter(telemetry)
	if reporter != nil {
		defer reporter.Close()
	}
	err := reconcileStreaming(ctx, pairName, leftSource, rightSource, leftPath, rightPath, leftCfg, rightCfg, pair, idx, w, maxTokenBuffer, reporter)
	if err != nil {
		reporter.Fail(0)
	}
	return err
}

func legacyProgressSink(progress ProgressFunc) TelemetrySink {
	if progress == nil {
		return nil
	}
	return func(event TelemetryEvent) error {
		if event.Type != "progress" || (event.Status != "running" && event.Status != "completed") {
			return nil
		}
		if event.Stage != "right_index" && event.Stage != "left_match" {
			return nil
		}
		progress(ProgressEvent{
			Phase:   event.Stage,
			Rows:    event.Rows,
			Elapsed: time.Duration(event.Elapsed * float64(time.Second)),
			Done:    event.Status == "completed",
		})
		return nil
	}
}

type streamingDuplicateOptions struct {
	rightRepresentativeRows    map[int]struct{}
	leftRepresentativeRows     map[int]struct{}
	rightPartitionOriginalRows []int
	leftPartitionOriginalRows  []int
}

func partitionOriginalRow(originalRows []int, rowNum int) int {
	if rowNum > 0 && rowNum <= len(originalRows) {
		return originalRows[rowNum-1]
	}
	return rowNum
}

func reconcileStreaming(
	ctx context.Context,
	pairName string,
	leftSource string,
	rightSource string,
	leftPath string,
	rightPath string,
	leftCfg config.ParserCfg,
	rightCfg config.ParserCfg,
	pair config.Pair,
	idx RightIndex,
	w ResultWriter,
	maxTokenBuffer int,
	reporter *engineTelemetry.Reporter,
) error {
	return reconcileStreamingWithOptions(ctx, pairName, leftSource, rightSource, leftPath, rightPath, leftCfg, rightCfg, pair, idx, w, maxTokenBuffer, reporter, streamingDuplicateOptions{})
}

func reconcileStreamingWithOptions(
	ctx context.Context,
	pairName string,
	leftSource string,
	rightSource string,
	leftPath string,
	rightPath string,
	leftCfg config.ParserCfg,
	rightCfg config.ParserCfg,
	pair config.Pair,
	idx RightIndex,
	w ResultWriter,
	maxTokenBuffer int,
	reporter *engineTelemetry.Reporter,
	duplicateOptions streamingDuplicateOptions,
) error {
	if err := validateStreamingCollaborators(idx, w); err != nil {
		return err
	}
	dateWindowDays, err := matching.ParseDateWindow(pair.DateWindow)
	if err != nil {
		return fmt.Errorf("invalid date_window: %w", err)
	}

	tolerance := pair.AmountToleranceMinor
	cc := matching.CurrencyTracker{}

	// Derive whether the name-token pass and/or subset-sum pass should run.
	// Passes takes precedence; fall back to the legacy NameMode field when no
	// explicit passes are set.
	effectiveTokenMode := pair.NameMode == "tokens"
	effectiveSubsetSumMode := false
	var subsetSumPassCfg config.PassConfig
	if len(pair.Passes) > 0 {
		effectiveTokenMode = containsPass(pair.Passes, config.PassTypeNameTokensOneToOne)
		effectiveSubsetSumMode = containsPass(pair.Passes, config.PassTypeSubsetSum)
		if effectiveSubsetSumMode {
			subsetSumPassCfg = findPassConfig(pair.Passes, config.PassTypeSubsetSum)
		}
		if err := validateStreamingPassOrder(pair.Passes); err != nil {
			return err
		}
	}
	// -----------------------------------------------------------------------
	// Pass 1: stream right CSV into index, track right duplicates
	// -----------------------------------------------------------------------
	// Force SkipRaw for the right side — Raw data in the index wastes memory.
	rightCfgNoRaw := rightCfg
	rightCfgNoRaw.SkipRaw = true

	rightPolicy := rightCfg.ResolvedDuplicatePolicy()
	financialMatchCount, financialDiffCount, financialUncheckedCount := 0, 0, 0
	settlementMatchCount, settlementDiffCount := 0, 0
	emitFinancial := func(tx Transaction) error {
		fw, ok := w.(FinancialEventWriter)
		if !ok {
			return nil
		}
		for _, check := range tx.FinancialChecks {
			finding := FinancialEffectFinding{Transaction: tx, Check: check}
			var err error
			if check.Field == "settlement" {
				switch check.Status {
				case "match":
					settlementMatchCount++
					err = fw.WriteSettlementMatch(finding)
				case "diff":
					settlementDiffCount++
					err = fw.WriteSettlementDiff(finding)
				}
				if err != nil {
					return err
				}
				continue
			}
			switch check.Status {
			case "match":
				financialMatchCount++
				err = fw.WriteFinancialEffectMatch(finding)
			case "diff":
				financialDiffCount++
				err = fw.WriteFinancialEffectDiff(finding)
			case "unchecked":
				financialUncheckedCount++
				err = fw.WriteFinancialUnchecked(finding)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}

	// rightSeen/rightDupKeys are only used for the "flag" policy.
	rightSeen := make(map[string]uint8)
	rightDupKeys := make(map[string]bool)
	// rightMergeSeen deduplicates for "merge" (first-seen wins).
	rightMergeSeen := make(map[string]bool)
	// rightLatestBuf accumulates last-seen rows per GroupKey for "latest".
	var rightLatestBuf *latestBuffer
	if rightPolicy == config.DuplicatePolicyLatest {
		rightLatestBuf = newLatestBuffer()
	}
	var totalRight int
	reporter.Start("right_index", rightSource, rightSource, nil)

	if err := ParseEach(ctx, rightSource, rightPath, rightCfgNoRaw, func(tx Transaction, rowNum int) error {
		totalRight++
		restorePartitionTransactionID(&tx, rightSource, duplicateOptions.rightPartitionOriginalRows, rowNum)
		if err := cc.Observe(rightSource, tx); err != nil {
			return err
		}
		reporter.Progress(totalRight)
		if duplicateOptions.rightRepresentativeRows != nil && tx.GroupKey != "" {
			if _, ok := duplicateOptions.rightRepresentativeRows[partitionOriginalRow(duplicateOptions.rightPartitionOriginalRows, rowNum)]; !ok {
				return nil
			}
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
			if err := emitFinancial(tx); err != nil {
				return err
			}
			return idx.Add(tx)
		case config.DuplicatePolicyKeep:
			if err := emitFinancial(tx); err != nil {
				return err
			}
			return idx.Add(tx)
		case config.DuplicatePolicyMerge:
			if tx.GroupKey != "" && rightMergeSeen[tx.GroupKey] {
				return nil // skip duplicate; first-seen already in index
			}
			if tx.GroupKey != "" {
				rightMergeSeen[tx.GroupKey] = true
			}
			if err := emitFinancial(tx); err != nil {
				return err
			}
			return idx.Add(tx)
		case config.DuplicatePolicyLatest:
			if tx.GroupKey != "" {
				rightLatestBuf.put(tx.GroupKey, tx) // overwrite with latest row
				return nil
			}
			if err := emitFinancial(tx); err != nil {
				return err
			}
			return idx.Add(tx)
		}
		if err := emitFinancial(tx); err != nil {
			return err
		}
		return idx.Add(tx)
	}); err != nil {
		return fmt.Errorf("parse right source: %w", err)
	}
	// For "latest": bulk-add the last-seen row per GroupKey after the full scan.
	if rightPolicy == config.DuplicatePolicyLatest {
		if err := rightLatestBuf.each(func(tx Transaction) error {
			if err := emitFinancial(tx); err != nil {
				return err
			}
			return idx.Add(tx)
		}); err != nil {
			return err
		}
	}
	reporter.Complete(totalRight)

	// Emit right duplicate groups via targeted re-scan (flag policy only).
	// matching.CollectDuplicates is only called when duplicates actually exist.
	dupTxnCount := 0
	if rightPolicy == config.DuplicatePolicyFlag && len(rightDupKeys) > 0 {
		reporter.Start("right_duplicate_scan", rightSource, rightSource, nil)
		groups, err := matching.CollectDuplicates(ctx, rightSource, rightPath, rightCfg, rightDupKeys)
		if err != nil {
			return fmt.Errorf("collect right duplicates: %w", err)
		}
		for _, g := range groups {
			dupTxnCount += len(g.Transactions)
			reporter.Progress(dupTxnCount)
			if err := w.WriteDuplicate(g); err != nil {
				return err
			}
		}
		reporter.Complete(dupTxnCount)
	}

	// -----------------------------------------------------------------------
	// Pass 2: stream left CSV, match against index, emit events immediately
	// -----------------------------------------------------------------------
	reporter.Start("left_match", leftSource, rightSource, nil)
	leftPolicy := leftCfg.ResolvedDuplicatePolicy()
	// leftSeen/leftDupKeys are only used for the "flag" policy.
	leftSeen := make(map[string]uint8)
	leftDupKeys := make(map[string]bool)
	// leftMergeSeen deduplicates for "merge" (first-seen wins).
	leftMergeSeen := make(map[string]bool)
	// leftLatestBuf accumulates last-seen rows per GroupKey for "latest".
	var leftLatestBuf *latestBuffer
	if leftPolicy == config.DuplicatePolicyLatest {
		leftLatestBuf = newLatestBuffer()
	}
	threshold := matching.ResolveNameMatchThreshold(pair.NameMatchThreshold)

	var (
		matchedCount       int
		amountDiffCount    int
		timingDiffCount    int
		unmatchedLeftCount int
		tokenUnmatchedLeft []Transaction

		// Monetary accumulators (minor units). Always populated; negligible cost.
		matchedAmtLeft    int64
		matchedAmtRight   int64
		unmatchedAmtLeft  int64
		unmatchedAmtRight int64
		amountDiffTotal   int64
		ambiguousAmtRight int64
	)
	var totalLeft int

	// doMatchLeft executes the core match logic for one left transaction.
	// Extracted so it can be called both from the ParseEach callback and from
	// the post-scan loop used by the "latest" policy.
	doMatchLeft := func(ltx Transaction) error {
		if ltx.Reference == "" {
			unmatchedLeftCount++
			unmatchedAmtLeft += ltx.Amount
			if effectiveTokenMode || effectiveSubsetSumMode {
				tokenUnmatchedLeft = append(tokenUnmatchedLeft, ltx)
				return nil
			}
			return w.WriteUnmatched(ltx, "left")
		}
		decision, err := decideMatch(ltx, idx, tolerance, dateWindowDays)
		if err != nil {
			return err
		}
		switch decision.outcome {
		case outcomeExact:
			if err := idx.MarkUsed(decision.right); err != nil {
				return fmt.Errorf("mark used: %w", err)
			}
			matchedCount++
			matchedAmtLeft += ltx.Amount
			matchedAmtRight += decision.rightTx.Amount
			return w.WriteMatch(MatchedPair{Left: ltx, Right: decision.rightTx})
		case outcomeAmountDiff:
			if err := idx.MarkUsed(decision.right); err != nil {
				return fmt.Errorf("mark used: %w", err)
			}
			amountDiffCount++
			diff := decision.amountDiffMinor
			if diff < 0 {
				amountDiffTotal += -diff
			} else {
				amountDiffTotal += diff
			}
			return w.WriteAmountDiff(AmountDiffPair{
				Left:      ltx,
				Right:     decision.rightTx,
				DiffMinor: decision.amountDiffMinor,
			})
		case outcomeTimingDiff:
			if err := idx.MarkUsed(decision.right); err != nil {
				return fmt.Errorf("mark used: %w", err)
			}
			timingDiffCount++
			return w.WriteTimingDiff(TimingDiffPair{
				Left:     ltx,
				Right:    decision.rightTx,
				DaysDiff: decision.daysDiff,
			})
		}
		unmatchedLeftCount++
		unmatchedAmtLeft += ltx.Amount
		if effectiveTokenMode || effectiveSubsetSumMode {
			tokenUnmatchedLeft = append(tokenUnmatchedLeft, ltx)
			return nil
		}
		return w.WriteUnmatched(ltx, "left")
	}

	if err := ParseEach(ctx, leftSource, leftPath, leftCfg, func(ltx Transaction, rowNum int) error {
		totalLeft++
		restorePartitionTransactionID(&ltx, leftSource, duplicateOptions.leftPartitionOriginalRows, rowNum)
		if err := cc.Observe(leftSource, ltx); err != nil {
			return err
		}
		reporter.Progress(totalLeft)
		if duplicateOptions.leftRepresentativeRows != nil && ltx.GroupKey != "" {
			if _, ok := duplicateOptions.leftRepresentativeRows[partitionOriginalRow(duplicateOptions.leftPartitionOriginalRows, rowNum)]; !ok {
				return nil
			}
		}
		switch leftPolicy {
		case config.DuplicatePolicyFlag:
			if ltx.GroupKey != "" {
				if leftSeen[ltx.GroupKey] < 2 {
					leftSeen[ltx.GroupKey]++
				}
				if leftSeen[ltx.GroupKey] == 2 {
					leftDupKeys[ltx.GroupKey] = true
				}
			}
			if err := emitFinancial(ltx); err != nil {
				return err
			}
			return doMatchLeft(ltx)
		case config.DuplicatePolicyKeep:
			if err := emitFinancial(ltx); err != nil {
				return err
			}
			return doMatchLeft(ltx)
		case config.DuplicatePolicyMerge:
			if ltx.GroupKey != "" && leftMergeSeen[ltx.GroupKey] {
				return nil // skip duplicate; already counted in totalLeft
			}
			if ltx.GroupKey != "" {
				leftMergeSeen[ltx.GroupKey] = true
			}
			if err := emitFinancial(ltx); err != nil {
				return err
			}
			return doMatchLeft(ltx)
		case config.DuplicatePolicyLatest:
			if ltx.GroupKey != "" {
				leftLatestBuf.put(ltx.GroupKey, ltx) // overwrite with latest row
				return nil
			}
			if err := emitFinancial(ltx); err != nil {
				return err
			}
			return doMatchLeft(ltx)
		}
		if err := emitFinancial(ltx); err != nil {
			return err
		}
		return doMatchLeft(ltx)
	}); err != nil {
		return fmt.Errorf("parse left source: %w", err)
	}
	// For "latest": process buffered left rows after the full scan completes.
	if leftPolicy == config.DuplicatePolicyLatest {
		if err := leftLatestBuf.each(func(ltx Transaction) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := emitFinancial(ltx); err != nil {
				return err
			}
			return doMatchLeft(ltx)
		}); err != nil {
			return err
		}
	}
	reporter.Complete(totalLeft)

	// Emit left duplicate groups via targeted re-scan (flag policy only).
	if leftPolicy == config.DuplicatePolicyFlag && len(leftDupKeys) > 0 {
		reporter.Start("left_duplicate_scan", leftSource, rightSource, nil)
		groups, err := matching.CollectDuplicates(ctx, leftSource, leftPath, leftCfg, leftDupKeys)
		if err != nil {
			return fmt.Errorf("collect left duplicates: %w", err)
		}
		for _, g := range groups {
			dupTxnCount += len(g.Transactions)
			reporter.Progress(dupTxnCount)
			if err := w.WriteDuplicate(g); err != nil {
				return err
			}
		}
		reporter.Complete(dupTxnCount)
	}

	// -----------------------------------------------------------------------
	// After pass 2: collect unused right-side transactions
	// -----------------------------------------------------------------------
	var tokenUnmatchedRight []Transaction
	unmatchedRightCount := 0

	if err := idx.IterateUnused(func(tx Transaction) error {
		unmatchedRightCount++
		unmatchedAmtRight += tx.Amount
		if effectiveTokenMode || effectiveSubsetSumMode {
			tokenUnmatchedRight = append(tokenUnmatchedRight, tx)
			return nil
		}
		return w.WriteUnmatched(tx, "right")
	}); err != nil {
		return err
	}

	// -----------------------------------------------------------------------
	// Optional token-mode second pass on unmatched transactions
	//
	// Token mode is an edge case / fallback. It buffers unmatched transactions
	// from both sides. Worst case (all unmatched): O(n_total) memory.
	// Guarded by maxTokenBuffer advisory limit.
	// -----------------------------------------------------------------------
	var (
		subsetSumMatchedCount   int
		subsetSumAmbiguousCount int
		subsetSumSkippedCount   int
	)

	// postLeft/postRight hold the unmatched buffers available for the next
	// optional pass (subset_sum). After token mode they contain its remainders;
	// when token mode is absent but subset_sum is present they are the raw buffers.
	var postLeft, postRight []Transaction

	if effectiveTokenMode {
		reporter.Start("token_match", leftSource, rightSource, nil)
		bufTotal := len(tokenUnmatchedLeft) + len(tokenUnmatchedRight)
		reporter.Progress(bufTotal)
		if maxTokenBuffer > 0 && bufTotal > maxTokenBuffer {
			observeWarning(w, reporter, Warning{Code: WarningTokenBufferPressure, Message: fmt.Sprintf("token mode unmatched buffer is %d rows (limit %d); memory usage may be high", bufTotal, maxTokenBuffer)})
		}

		// Run Jaccard secondary matching on the buffered unmatched transactions.
		// Returns matched pairs plus the remaining (still unmatched) left/right slices.
		tokenMatches, remainLeft, remainRight := matchByNameTokensStreaming(
			tokenUnmatchedLeft, tokenUnmatchedRight, tolerance, dateWindowDays, threshold,
		)
		for _, mp := range tokenMatches {
			matchedCount++
			unmatchedLeftCount--
			unmatchedRightCount--
			matchedAmtLeft += mp.Left.Amount
			matchedAmtRight += mp.Right.Amount
			unmatchedAmtLeft -= mp.Left.Amount
			unmatchedAmtRight -= mp.Right.Amount
			if err := w.WriteMatch(mp); err != nil {
				return err
			}
		}
		if effectiveSubsetSumMode {
			postLeft = remainLeft
			postRight = remainRight
		} else {
			for _, tx := range remainLeft {
				if err := w.WriteUnmatched(tx, "left"); err != nil {
					return err
				}
			}
			for _, tx := range remainRight {
				if err := w.WriteUnmatched(tx, "right"); err != nil {
					return err
				}
			}
		}
		reporter.Complete(bufTotal)
	} else if effectiveSubsetSumMode {
		postLeft = tokenUnmatchedLeft
		postRight = tokenUnmatchedRight
		bufTotal := len(postLeft) + len(postRight)
		if maxTokenBuffer > 0 && bufTotal > maxTokenBuffer {
			observeWarning(w, reporter, Warning{Code: WarningTokenBufferPressure, Message: fmt.Sprintf("subset_sum unmatched buffer is %d rows (limit %d); memory usage may be high", bufTotal, maxTokenBuffer)})
		}
	}

	// -----------------------------------------------------------------------
	// Optional subset-sum pass — runs after reference (and optional token)
	// matching on whatever rows remain unmatched. Uses a bounded combinatorial
	// search; hard limits prevent unbounded work.
	// -----------------------------------------------------------------------
	if effectiveSubsetSumMode {
		reporter.Start("subset_sum", leftSource, rightSource, nil)
		// Build a temporary Result to hold the subset-sum events.
		tmpResult := &Result{}
		ssLeft, ssRight := matching.MatchBySubsetSum(
			tmpResult, postLeft, postRight,
			tolerance, dateWindowDays, subsetSumPassCfg,
		)
		subsetSumMatchedCount = len(tmpResult.SubsetSumMatched)
		subsetSumAmbiguousCount = len(tmpResult.SubsetSumAmbiguous)
		subsetSumSkippedCount = len(tmpResult.SubsetSumSkipped)

		ssw, hasSSW := w.(SubsetSumEventWriter)
		for _, sm := range tmpResult.SubsetSumMatched {
			matchedAmtLeft += sm.Left.Amount
			unmatchedAmtLeft -= sm.Left.Amount
			unmatchedLeftCount--
			for _, r := range sm.Rights {
				matchedAmtRight += r.Amount
				unmatchedAmtRight -= r.Amount
				unmatchedRightCount--
			}
			if hasSSW {
				if err := ssw.WriteSubsetSumMatch(sm); err != nil {
					return err
				}
			}
		}
		for _, sa := range tmpResult.SubsetSumAmbiguous {
			// Left row stays unmatched; right rows were consumed (not returned in
			// ssRight) but may repeat across alternatives — dedupe by ID before
			// adjusting counters so each right row is only counted once.
			seen := make(map[string]bool)
			for _, alt := range sa.Alternatives {
				for _, r := range alt {
					if seen[r.ID] {
						continue
					}
					seen[r.ID] = true
					unmatchedAmtRight -= r.Amount
					unmatchedRightCount--
					ambiguousAmtRight += r.Amount
				}
			}
			if hasSSW {
				if err := ssw.WriteSubsetSumAmbiguous(sa); err != nil {
					return err
				}
			}
		}
		for _, sk := range tmpResult.SubsetSumSkipped {
			_ = sk // left row stays unmatched, written below via ssLeft
			if hasSSW {
				if err := ssw.WriteSubsetSumSkipped(sk); err != nil {
					return err
				}
			}
		}
		for _, tx := range ssLeft {
			if err := w.WriteUnmatched(tx, "left"); err != nil {
				return err
			}
		}
		for _, tx := range ssRight {
			if err := w.WriteUnmatched(tx, "right"); err != nil {
				return err
			}
		}
		reporter.Complete(len(postLeft) + len(postRight))
	}

	// -----------------------------------------------------------------------
	// Summary
	// -----------------------------------------------------------------------
	total := totalLeft
	if totalRight > total {
		total = totalRight
	}
	matchRate := 0.0
	if total > 0 {
		matchRate = math.Round(float64(matchedCount)/float64(total)*10000) / 100
	}
	reconciledCount := matchedCount + amountDiffCount + timingDiffCount + subsetSumMatchedCount
	reconciledRate := 0.0
	if total > 0 {
		reconciledRate = math.Round(float64(reconciledCount)/float64(total)*10000) / 100
	}

	for _, warning := range cc.Warnings() {
		observeWarning(w, reporter, Warning{Code: WarningEmptyCurrency, Message: warning})
	}

	reporter.Start("finalization", leftSource, rightSource, nil)
	reporter.Progress(totalLeft + totalRight)
	if err := w.WriteSummary(Summary{
		Currency:                  cc.Currency(),
		TotalLeft:                 totalLeft,
		TotalRight:                totalRight,
		MatchedCount:              matchedCount,
		UnmatchedLeft:             unmatchedLeftCount,
		UnmatchedRight:            unmatchedRightCount,
		AmountDiffCount:           amountDiffCount,
		TimingDiffCount:           timingDiffCount,
		DuplicateCount:            dupTxnCount,
		MatchRatePct:              matchRate,
		ReconciledRatePct:         reconciledRate,
		SubsetSumMatchedCount:     subsetSumMatchedCount,
		SubsetSumAmbiguousCount:   subsetSumAmbiguousCount,
		SubsetSumSkippedCount:     subsetSumSkippedCount,
		FinancialEffectMatchCount: financialMatchCount,
		FinancialEffectDiffCount:  financialDiffCount,
		FinancialUncheckedCount:   financialUncheckedCount,
		SettlementMatchCount:      settlementMatchCount,
		SettlementDiffCount:       settlementDiffCount,
		MatchedAmountLeft:         matchedAmtLeft,
		MatchedAmountRight:        matchedAmtRight,
		UnmatchedAmountLeft:       unmatchedAmtLeft,
		UnmatchedAmountRight:      unmatchedAmtRight,
		AmountDiffTotal:           amountDiffTotal,
		AmbiguousAmountRight:      ambiguousAmtRight,
		TotalDiscrepancy:          unmatchedAmtLeft + unmatchedAmtRight + amountDiffTotal + ambiguousAmtRight,
	}); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	reporter.Complete(totalLeft + totalRight)
	return nil
}

func restorePartitionTransactionID(tx *Transaction, source string, originalRows []int, rowNum int) {
	if len(originalRows) == 0 {
		return
	}
	tx.ID = fmt.Sprintf("%s-%d", source, partitionOriginalRow(originalRows, rowNum))
}

// matchByNameTokensStreaming runs the Jaccard secondary pass on pre-buffered
// unmatched slices and returns a slice of MatchedPairs plus the remaining
// (still unmatched) slices in-place.
func matchByNameTokensStreaming(
	left, right []Transaction,
	tolerance int64,
	windowDays int,
	threshold float64,
) (matches []MatchedPair, remainLeft, remainRight []Transaction) {
	usedRight := make(map[string]bool)

	for _, ltx := range left {
		if ltx.Name == "" {
			remainLeft = append(remainLeft, ltx)
			continue
		}
		ltokens := matching.Tokenize(ltx.Name)
		bestScore := 0.0
		bestIdx := -1
		for i, rtx := range right {
			if usedRight[rtx.ID] || rtx.Name == "" {
				continue
			}
			amtDiff := ltx.Amount - rtx.Amount
			if amtDiff < 0 {
				amtDiff = -amtDiff
			}
			if amtDiff > tolerance {
				continue
			}
			if windowDays > 0 && daysBetween(ltx.Date, rtx.Date) > windowDays {
				continue
			}
			score := matching.TokenOverlap(ltokens, matching.Tokenize(rtx.Name))
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestScore > threshold && bestIdx >= 0 {
			matches = append(matches, MatchedPair{Left: ltx, Right: right[bestIdx]})
			usedRight[right[bestIdx].ID] = true
		} else {
			remainLeft = append(remainLeft, ltx)
		}
	}
	for _, rtx := range right {
		if !usedRight[rtx.ID] {
			remainRight = append(remainRight, rtx)
		}
	}
	return matches, remainLeft, remainRight
}

// containsPass returns true if passes contains a pass of the given type.
func containsPass(passes []config.PassConfig, passType string) bool {
	for _, p := range passes {
		if p.Type == passType {
			return true
		}
	}
	return false
}

// validateStreamingPassOrder returns an error when the pass list uses an ordering
// that the streaming path cannot support. Specifically: the streaming path runs
// reference matching inline during the left-file scan; name-token matching runs
// afterward on the unmatched buffer. A name_tokens_one_to_one pass that appears
// before reference_one_to_one would require buffering all left rows upfront, which
// violates the streaming memory contract.
func validateStreamingPassOrder(passes []config.PassConfig) error {
	sawRef := false
	for i, p := range passes {
		switch p.Type {
		case config.PassTypeReferenceOneToOne:
			sawRef = true
		case config.PassTypeNameTokensOneToOne:
			if !sawRef {
				return fmt.Errorf("streaming: %s at passes[%d] must be preceded by %s — streaming always indexes reference first",
					config.PassTypeNameTokensOneToOne, i, config.PassTypeReferenceOneToOne)
			}
		case config.PassTypeSubsetSum:
			if !sawRef {
				return fmt.Errorf("streaming: %s at passes[%d] must be preceded by %s — subset_sum runs on unmatched rows after the reference pass",
					config.PassTypeSubsetSum, i, config.PassTypeReferenceOneToOne)
			}
		default:
			return fmt.Errorf("streaming: unsupported pass type %q at passes[%d]", p.Type, i)
		}
	}
	return nil
}

// findPassConfig returns the first PassConfig with the given type, or a zero value.
func findPassConfig(passes []config.PassConfig, passType string) config.PassConfig {
	for _, p := range passes {
		if p.Type == passType {
			return p
		}
	}
	return config.PassConfig{}
}
