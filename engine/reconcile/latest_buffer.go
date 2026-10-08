//nolint:staticcheck // Domain aliases keep package-internal signatures readable.
package reconcile

import . "github.com/reconifyhq/reconify/engine/domain"

// latestBuffer keeps the last-seen transaction per group key for the "latest"
// duplicate policy. Replay order is the order in which each group key was first
// seen, which is the position the batch ApplyPolicy path gives the survivor, so
// every execution path emits the same sequence on every run.
type latestBuffer struct {
	order []string
	rows  map[string]Transaction
}

func newLatestBuffer() *latestBuffer {
	return &latestBuffer{rows: make(map[string]Transaction)}
}

// put records tx as the latest row for key, overwriting any earlier row while
// keeping the key's original position.
func (b *latestBuffer) put(key string, tx Transaction) {
	if _, ok := b.rows[key]; !ok {
		b.order = append(b.order, key)
	}
	b.rows[key] = tx
}

// each calls fn for the buffered rows in first-seen key order.
func (b *latestBuffer) each(fn func(tx Transaction) error) error {
	for _, key := range b.order {
		if err := fn(b.rows[key]); err != nil {
			return err
		}
	}
	return nil
}
