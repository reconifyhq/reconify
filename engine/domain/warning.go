package domain

// Warning is a non-fatal condition observed while reconciling. Codes are
// stable, machine-readable domain identifiers; Message is suitable for people.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Warning codes identify the non-fatal reconciliation condition observed.
const (
	WarningUnsupportedGroupedEvents    = "unsupported_grouped_events"
	WarningUnsupportedManyToManyEvents = "unsupported_many_to_many_events"
	WarningUnsupportedSubsetSumEvents  = "unsupported_subset_sum_events"
	WarningTokenBufferPressure         = "token_buffer_pressure"
	WarningCarryBufferPressure         = "carry_buffer_pressure"
	WarningEmptyCurrency               = "empty_currency"
	WarningDeterministicUnsupported    = "deterministic_unsupported"
)

// WarningObserver is an optional, best-effort observer for library warnings.
// It deliberately does not return an error: observing a warning must not alter
// reconciliation results.
type WarningObserver interface {
	ObserveWarning(Warning)
}
