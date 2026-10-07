// Package matching provides pure reconciliation candidate selection.
//
//nolint:staticcheck // Domain aliases keep package-internal signatures readable.
package matching

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reconifyhq/reconify/config"
	. "github.com/reconifyhq/reconify/engine/domain"
	"github.com/reconifyhq/reconify/engine/parser"
)

// CollectDuplicates re-scans an input file and returns a DuplicateGroup for every
// group key in dupKeys. It is invoked only when duplicates were detected during
// the primary pass; for datasets with no duplicates this function is never called.
//
// Using the original cfg (not rightCfgNoRaw) preserves the Raw field if the
// caller has configured SkipRaw = false.
//
// Groups are returned in the order their first row appears in the file.
//
// Memory: O(n_dup_rows) — only rows whose group key is in dupKeys are retained.
func CollectDuplicates(
	ctx context.Context,
	sourceName string,
	path string,
	cfg config.ParserCfg,
	dupKeys map[string]bool,
) ([]DuplicateGroup, error) {
	byKey := make(map[string][]Transaction, len(dupKeys))
	// order records each group key at its first occurrence in the file so groups
	// are returned in input row order, matching AnnotateDuplicates. Ranging over
	// byKey directly would emit groups in a different order on every run.
	order := make([]string, 0, len(dupKeys))
	if err := parser.ParseEach(ctx, sourceName, path, cfg, func(tx Transaction, _ int) error {
		if dupKeys[tx.GroupKey] {
			if _, seen := byKey[tx.GroupKey]; !seen {
				order = append(order, tx.GroupKey)
			}
			byKey[tx.GroupKey] = append(byKey[tx.GroupKey], tx)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	groups := make([]DuplicateGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, DuplicateGroup{
			Source:       sourceName,
			Reference:    key,
			Transactions: byKey[key],
		})
	}
	return groups, nil
}

// CurrencyTracker validates that all non-empty currency values in a run are the same.
// This protects monetary summary totals from accidental cross-currency aggregation.
// Rows with an empty currency are still included in monetary totals (no currency to
// validate against), but are counted per-source so callers can warn about the mix.
type CurrencyTracker struct {
	base       string
	emptyCount map[string]int
}

// Currency returns the validated run currency, or empty when none was present.
func (c *CurrencyTracker) Currency() string { return c.base }

// Observe validates a transaction's currency against the run currency.
func (c *CurrencyTracker) Observe(source string, tx Transaction) error {
	cur := strings.TrimSpace(tx.Currency)
	if cur == "" {
		if c.emptyCount == nil {
			c.emptyCount = make(map[string]int)
		}
		c.emptyCount[source]++
		return nil
	}
	if c.base == "" {
		c.base = cur
		return nil
	}
	if cur != c.base {
		return fmt.Errorf(
			"mixed currencies are not supported for monetary totals: saw %q and %q (source=%s, id=%s, reference=%s); reconcile one currency per run",
			c.base, cur, source, tx.ID, tx.Reference,
		)
	}
	return nil
}

// Warnings returns human-readable warnings for sources that mixed empty-currency
// rows with a non-empty base currency. Empty-currency rows still count toward
// monetary totals; this only adds visibility, it never changes validation behavior.
func (c *CurrencyTracker) Warnings() []string {
	if c.base == "" || len(c.emptyCount) == 0 {
		return nil
	}
	sources := make([]string, 0, len(c.emptyCount))
	for source := range c.emptyCount {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	warnings := make([]string, 0, len(sources))
	for _, source := range sources {
		warnings = append(warnings, fmt.Sprintf(
			"source %q: %d row(s) had an empty currency and were included in monetary totals alongside currency %q",
			source, c.emptyCount[source], c.base,
		))
	}
	return warnings
}

// AnnotateDuplicates reports duplicate groups without affecting matching.
func AnnotateDuplicates(txns []Transaction) []DuplicateGroup {
	seen := make(map[string][]Transaction)
	order := make([]string, 0)

	for _, tx := range txns {
		if tx.GroupKey == "" {
			continue
		}
		if _, exists := seen[tx.GroupKey]; !exists {
			order = append(order, tx.GroupKey)
		}
		seen[tx.GroupKey] = append(seen[tx.GroupKey], tx)
	}

	var dups []DuplicateGroup
	for _, key := range order {
		group := seen[key]
		if len(group) > 1 {
			dups = append(dups, DuplicateGroup{
				Source:       group[0].Source,
				Reference:    key,
				Transactions: group,
			})
		}
	}
	return dups
}

// ApplyPolicy collapses txns to one representative per GroupKey when
// policy is merge or latest. merge keeps the first occurrence; latest keeps the
// last. For flag/keep it returns txns unchanged (no allocation).
// Rows with an empty GroupKey are never collapsed regardless of policy.
func ApplyPolicy(txns []Transaction, policy config.DuplicatePolicy) []Transaction {
	switch policy {
	case config.DuplicatePolicyMerge, config.DuplicatePolicyLatest:
	default:
		return txns
	}
	seen := make(map[string]int, len(txns)) // GroupKey → index in out
	out := make([]Transaction, 0, len(txns))
	for _, tx := range txns {
		if tx.GroupKey == "" {
			out = append(out, tx)
			continue
		}
		if idx, exists := seen[tx.GroupKey]; exists {
			if policy == config.DuplicatePolicyLatest {
				out[idx] = tx
			}
		} else {
			seen[tx.GroupKey] = len(out)
			out = append(out, tx)
		}
	}
	return out
}

// ApplyDuplicatePolicy applies the duplicate_policy from cfg to txns.
// Convenience wrapper for batch callers (Parse → Reconcile path).
func ApplyDuplicatePolicy(txns []Transaction, cfg config.ParserCfg) []Transaction {
	return ApplyPolicy(txns, cfg.ResolvedDuplicatePolicy())
}
