// Package config provides functionality to load and validate the Reconify configuration
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the root configuration structure
type Config struct {
	Version  int               `yaml:"version"`
	Timezone string            `yaml:"timezone"`
	Index    IndexCfg          `yaml:"index,omitempty"`
	Sources  map[string]Source `yaml:"sources"`
	Pairs    map[string]Pair   `yaml:"pairs"`
}

// Source defines a data source configuration
type Source struct {
	FilePattern string    `yaml:"file_pattern"`
	Parser      ParserCfg `yaml:"parser"`
}

// ParserCfg defines source parsing configuration for CSV, JSON, and XLSX files.
type ParserCfg struct {
	Type        string `yaml:"type"`
	DateCol     string `yaml:"date_col"`
	DateLayout  string `yaml:"date_layout"`
	TZ          string `yaml:"tz"`
	AmountCol   string `yaml:"amount_col"`
	Decimal     string `yaml:"decimal,omitempty"`
	Thousands   string `yaml:"thousands,omitempty"`
	Multiplier  int64  `yaml:"multiplier"`
	CurrencyCol string `yaml:"currency_col,omitempty"`
	NameCol     string `yaml:"name_col,omitempty"`
	RefCol      string `yaml:"ref_col,omitempty"`
	// GroupCol is the duplicate/grouping key column. It is independent of RefCol:
	// RefCol is the matching key, GroupCol is the key used to detect duplicates
	// (e.g. an invoice number shared by several installment payments that each
	// have a unique RefCol value). Falls back to RefCol when empty.
	GroupCol string `yaml:"group_col,omitempty"`
	Sheet    string `yaml:"sheet,omitempty"`
	// SkipRaw skips the per-row Raw map[string]string allocation.
	// Set to true for large files to reduce allocator pressure.
	// Default false preserves the Raw field on every Transaction.
	SkipRaw bool `yaml:"skip_raw,omitempty"`
	// DuplicatePolicy controls how transactions sharing the same GroupKey are handled.
	// Valid values: "flag" (default), "keep", "merge", "latest".
	DuplicatePolicy DuplicatePolicy `yaml:"duplicate_policy,omitempty"`
	// Financials optionally maps additional monetary columns and declares
	// source-local expectations (fees, taxes, and settlement identities).
	Financials *FinancialsCfg `yaml:"financials,omitempty"`
}

// FinancialsCfg describes optional monetary fields beyond the transaction
// amount. Values use the same decimal, thousands, and multiplier settings as
// the source's primary amount column.
type FinancialsCfg struct {
	GrossCol     string                    `yaml:"gross_col,omitempty"`
	NetCol       string                    `yaml:"net_col,omitempty"`
	Fields       map[string]string         `yaml:"fields,omitempty"`
	Expectations map[string]ExpectationCfg `yaml:"expectations,omitempty"`
}

// ExpectationCfg supports one expected-value form per named financial field.
// Field refers to another normalized financial field; fixed and percentage
// provide literal and rate-based expectations. FixedPlusPercentage combines
// both components, while components sums named fields.
type ExpectationCfg struct {
	Field               string                  `yaml:"field,omitempty"`
	Fixed               *int64                  `yaml:"fixed,omitempty"`
	Percentage          *PercentageCfg          `yaml:"percentage,omitempty"`
	FixedPlusPercentage *FixedPlusPercentageCfg `yaml:"fixed_plus_percentage,omitempty"`
	Components          []string                `yaml:"components,omitempty"`
	Operation           string                  `yaml:"operation,omitempty"`
	ToleranceMinor      int64                   `yaml:"tolerance_minor,omitempty"`
}

// PercentageCfg defines a percentage of a named normalized financial field.
type PercentageCfg struct {
	Base string  `yaml:"base"`
	Rate float64 `yaml:"rate"`
}

// FixedPlusPercentageCfg defines a fixed minor-unit amount plus a percentage.
type FixedPlusPercentageCfg struct {
	Fixed      int64         `yaml:"fixed"`
	Percentage PercentageCfg `yaml:"percentage"`
}

// CSVParserCfg is kept as an alias for existing Go callers.
type CSVParserCfg = ParserCfg

// Pass type constants for recognized matching strategies.
const (
	PassTypeReferenceOneToOne  = "reference_one_to_one"
	PassTypeNameTokensOneToOne = "name_tokens_one_to_one"
	PassTypeOneToMany          = "one_to_many"
	PassTypeManyToMany         = "many_to_many"
	// PassTypeSubsetSum is a bounded combinatorial pass that searches subsets of
	// right-side rows whose sum equals one left row's amount (within tolerance).
	// It runs after cheaper passes and enforces hard limits on candidate count,
	// subset size, and search time to prevent unbounded work.
	PassTypeSubsetSum = "subset_sum"
)

// GroupBy constants for grouped passes.
const (
	GroupByReference = "reference"
	GroupByName      = "name"
	GroupByGroupKey  = "group_key"
)

// ResultMode controls which reconciliation events are emitted by the result writer.
type ResultMode string

const (
	// ResultModeAll emits every event: matches, diffs, unmatched, duplicates. Default.
	ResultModeAll ResultMode = "all"
	// ResultModeExceptionsOnly suppresses clean matches; emits unmatched, diffs,
	// duplicates, ambiguous groups, and grouped/N:M exception events.
	ResultModeExceptionsOnly ResultMode = "exceptions_only"
	// ResultModeSummaryOnly suppresses all item events; only the summary is emitted.
	ResultModeSummaryOnly ResultMode = "summary_only"
)

// DuplicatePolicy controls how transactions sharing the same GroupKey are handled.
type DuplicatePolicy string

const (
	// DuplicatePolicyFlag surfaces duplicates via WriteDuplicate; all rows
	// participate in matching. Default when duplicate_policy is unset.
	DuplicatePolicyFlag DuplicatePolicy = "flag"
	// DuplicatePolicyKeep treats each duplicate as a distinct row; all rows
	// participate in matching; WriteDuplicate is never called.
	DuplicatePolicyKeep DuplicatePolicy = "keep"
	// DuplicatePolicyMerge collapses duplicates to the first-seen row per
	// GroupKey before matching; WriteDuplicate is never called.
	DuplicatePolicyMerge DuplicatePolicy = "merge"
	// DuplicatePolicyLatest collapses duplicates to the last-seen row per
	// GroupKey (by file order) before matching; WriteDuplicate is never called.
	DuplicatePolicyLatest DuplicatePolicy = "latest"
)

// SubsetSumFilters controls which candidate constraints the subset_sum pass
// applies before attempting combinatorial search. All filters default to true.
type SubsetSumFilters struct {
	// Currency, when true, restricts candidates to rows whose currency matches
	// the left row. Default true.
	Currency bool `yaml:"currency"`
	// DateWindow, when true, restricts candidates to rows within the pair's
	// date_window. Default true.
	DateWindow bool `yaml:"date_window"`
	// SameSign, when true, restricts candidates to rows whose amount has the same
	// sign as the left row. Default true.
	SameSign bool `yaml:"same_sign"`
}

// PassConfig defines a single matching pass within a pair's pipeline.
// Passes run in configured order; each pass only sees rows left unmatched
// by earlier passes.
type PassConfig struct {
	Type    string `yaml:"type"`
	GroupBy string `yaml:"group_by,omitempty"`

	// Fields below apply only to the subset_sum pass type.

	// MaxCandidates is the maximum number of right-side rows considered as
	// subset candidates for one left row. Rows exceeding this limit emit a
	// SubsetSumSkippedPair. Default 24.
	MaxCandidates int `yaml:"max_candidates,omitempty"`
	// MaxSubsetSize is the maximum subset cardinality searched. Default 8.
	MaxSubsetSize int `yaml:"max_subset_size,omitempty"`
	// MaxAlternatives is the maximum number of valid subsets returned before
	// a result is classified as ambiguous. Default 3.
	MaxAlternatives int `yaml:"max_alternatives,omitempty"`
	// TimeoutMS is the per-left-row search deadline in milliseconds. When
	// exceeded, a SubsetSumSkippedPair is emitted. Default 50.
	TimeoutMS int `yaml:"timeout_ms,omitempty"`
	// CandidateFilters restricts which right-side rows are eligible candidates.
	// A nil value (the field omitted) defaults all filters to true. An explicit
	// value — including one with all fields false — is honored as-is, so
	// `candidate_filters: {currency: false, date_window: false, same_sign: false}`
	// genuinely disables every filter.
	CandidateFilters *SubsetSumFilters `yaml:"candidate_filters,omitempty"`
}

// ResolvedGroupBy returns the configured group_by key, defaulting to
// GroupByReference when the field is empty.
func (p PassConfig) ResolvedGroupBy() string {
	if p.GroupBy != "" {
		return p.GroupBy
	}
	return GroupByReference
}

// ResolvedMaxCandidates returns the configured value or the default of 24.
func (p PassConfig) ResolvedMaxCandidates() int {
	if p.MaxCandidates > 0 {
		return p.MaxCandidates
	}
	return 24
}

// ResolvedMaxSubsetSize returns the configured value or the default of 8.
func (p PassConfig) ResolvedMaxSubsetSize() int {
	if p.MaxSubsetSize > 0 {
		return p.MaxSubsetSize
	}
	return 8
}

// ResolvedMaxAlternatives returns the configured value or the default of 3.
func (p PassConfig) ResolvedMaxAlternatives() int {
	if p.MaxAlternatives > 0 {
		return p.MaxAlternatives
	}
	return 3
}

// ResolvedTimeoutMS returns the configured value or the default of 50.
func (p PassConfig) ResolvedTimeoutMS() int {
	if p.TimeoutMS > 0 {
		return p.TimeoutMS
	}
	return 50
}

// ResolvedCandidateFilters returns the configured filters, defaulting all
// fields to true when CandidateFilters was left unset (nil). An explicitly
// configured value, including one with every field false, is returned as-is.
func (p PassConfig) ResolvedCandidateFilters() SubsetSumFilters {
	if p.CandidateFilters == nil {
		return SubsetSumFilters{Currency: true, DateWindow: true, SameSign: true}
	}
	return *p.CandidateFilters
}

// ResolvedDuplicatePolicy returns the configured policy, defaulting to
// DuplicatePolicyFlag when the field is empty (preserves backward compatibility).
func (p ParserCfg) ResolvedDuplicatePolicy() DuplicatePolicy {
	if p.DuplicatePolicy == "" {
		return DuplicatePolicyFlag
	}
	return p.DuplicatePolicy
}

// Pair defines a reconciliation pair configuration
type Pair struct {
	Left                 string `yaml:"left"`
	Right                string `yaml:"right"`
	DateWindow           string `yaml:"date_window"`
	AmountToleranceMinor int64  `yaml:"amount_tolerance_minor"`
	NameMode             string `yaml:"name_mode"`
	// NameMatchThreshold is the minimum Jaccard token-overlap score (0 < x < 1)
	// a candidate must exceed (strictly) for a name_mode=tokens match. Defaults
	// to 0.5 when unset. 1.0 is rejected by validation: since the comparison is
	// strict (score > threshold) and a Jaccard score never exceeds 1.0, a
	// threshold of exactly 1.0 would silently match nothing.
	NameMatchThreshold float64 `yaml:"name_match_threshold,omitempty"`
	// Rights lists multiple counterpart sources reconciled against Left in order:
	// unmatched left transactions from one pass feed into the next. Mutually
	// exclusive with Right — set exactly one of the two. Use Counterparts() to
	// read the resolved list regardless of which field was set.
	Rights []string `yaml:"rights,omitempty"`
	// Passes defines an explicit ordered list of matching strategies. When set,
	// passes run in order and each pass only sees rows left unmatched by earlier
	// passes. Omitting Passes preserves legacy behavior: reference matching
	// followed by optional name_mode=tokens. When Passes is set, name_mode=tokens
	// is rejected — add a name_tokens_one_to_one pass explicitly instead.
	Passes []PassConfig `yaml:"passes,omitempty"`
	// ResultMode controls which events the result writer emits for this pair.
	// Valid values: "all" (default), "exceptions_only", "summary_only".
	// The CLI flag --result-mode overrides this when explicitly provided.
	ResultMode ResultMode `yaml:"result_mode,omitempty"`
}

// Counterparts returns the ordered list of counterpart source names for this pair,
// regardless of whether Right or Rights was configured. Single-counterpart configs
// (Right set, Rights empty) return a one-element slice — callers should treat that
// as equivalent to today's behavior.
func (p Pair) Counterparts() []string {
	if len(p.Rights) > 0 {
		return p.Rights
	}
	if p.Right != "" {
		return []string{p.Right}
	}
	return nil
}

// IndexCfg controls which right-side index backend ReconcileStreaming uses.
type IndexCfg struct {
	// Backend is one of: memory, disk, auto, partitioned.
	// Default (empty) is memory.
	Backend string `yaml:"backend,omitempty"`
	// SpillDir is the directory used for disk index temporary files.
	// Ignored when Backend=memory.
	SpillDir string `yaml:"spill_dir,omitempty"`
	// AutoMaxRightFileMB is the threshold (in MB) for Backend=auto.
	// If right file size exceeds this value, disk backend is selected.
	// Default (0) is 2048 MB.
	AutoMaxRightFileMB int64 `yaml:"auto_max_right_file_mb,omitempty"`
	// MaxMemoryMB is the maximum estimated resident memory for index selection.
	// Zero leaves memory uncapped for backwards compatibility.
	MaxMemoryMB int64 `yaml:"max_memory_mb,omitempty"`
	// MaxTempDiskMB is the maximum estimated temporary storage for disk or
	// partitioned indexing. Zero leaves the configured budget uncapped, but the
	// selector still checks actual free space.
	MaxTempDiskMB int64 `yaml:"max_temp_disk_mb,omitempty"`
	// PartitionCount controls the number of hash partitions for the bounded-memory
	// backend. Zero selects an adaptive power-of-two count; positive values must
	// be at least 2.
	PartitionCount int `yaml:"partition_count,omitempty"`
}

// Load reads and parses a YAML configuration file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- config path is explicit user input.
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	return &cfg, nil
}

// ValidationError is one structural configuration problem. Error returns the
// full human-readable text; Path and Message expose it in structured form for
// machine-readable diagnostics. Path is a dotted YAML path such as
// "sources.left.parser.date_col" and is empty when the problem is not tied to a
// single field.
type ValidationError struct {
	Path    string
	Message string
	text    string
}

// Error returns the human-readable validation text.
func (e *ValidationError) Error() string { return e.text }

// pathPrefix matches the leading YAML path token of a validation message.
var pathPrefix = regexp.MustCompile(`^(version|timezone|(?:index|sources|pairs)(?:\.[A-Za-z0-9_\-\[\]]+)*)`)

func newValidationError(err error) *ValidationError {
	text := err.Error()
	out := &ValidationError{Message: text, text: text}
	switch {
	case strings.HasPrefix(text, "at least one source is required"):
		out.Path = "sources"
	case pathPrefix.MatchString(text):
		path := pathPrefix.FindString(text)
		out.Path = path
		if rest, ok := strings.CutPrefix(text, path+": "); ok {
			out.Message = rest
		}
	}
	return out
}

// ValidationErrors converts errors returned by Validate into structured
// entries. Errors that did not originate from Validate keep an empty path.
func ValidationErrors(errs []error) []ValidationError {
	out := make([]ValidationError, 0, len(errs))
	for _, err := range errs {
		var ve *ValidationError
		if errors.As(err, &ve) {
			out = append(out, ValidationError{Path: ve.Path, Message: ve.Message, text: ve.text})
			continue
		}
		out = append(out, ValidationError{Message: err.Error(), text: err.Error()})
	}
	return out
}

// Validate performs structural validation on the configuration. Every returned
// error is a *ValidationError.
func (c *Config) Validate() []error {
	errs := c.validate()
	for i, err := range errs {
		errs[i] = newValidationError(err)
	}
	return errs
}

func (c *Config) validate() []error {
	var errs []error

	// Validate version
	if c.Version != 1 {
		errs = append(errs, fmt.Errorf("version must be 1 (got %d)", c.Version))
	}

	// Validate timezone
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			errs = append(errs, fmt.Errorf("timezone %q is invalid: %v", c.Timezone, err))
		}
	}

	// Validate index settings
	if indexErrs := validateIndex(c.Index); len(indexErrs) > 0 {
		errs = append(errs, indexErrs...)
	}

	// Validate sources
	if len(c.Sources) == 0 {
		errs = append(errs, fmt.Errorf("at least one source is required"))
	}

	for _, name := range getSourceNames(c.Sources) {
		source := c.Sources[name]
		if sourceErrs := validateSource(name, source); len(sourceErrs) > 0 {
			errs = append(errs, sourceErrs...)
		}
	}

	// Validate pairs
	pairNames := make([]string, 0, len(c.Pairs))
	for name := range c.Pairs {
		pairNames = append(pairNames, name)
	}
	sort.Strings(pairNames)
	for _, name := range pairNames {
		pair := c.Pairs[name]
		if pairErrs := validatePair(name, pair, c.Sources); len(pairErrs) > 0 {
			errs = append(errs, pairErrs...)
		}
	}

	return errs
}

func validateSource(name string, source Source) []error {
	var errs []error

	if source.FilePattern == "" {
		errs = append(errs, fmt.Errorf("sources.%s.file_pattern: required field is missing", name))
	}

	parser := source.Parser
	switch strings.ToLower(strings.TrimSpace(parser.Type)) {
	case "", "auto", "csv", "json", "xlsx":
	default:
		errs = append(errs, fmt.Errorf("sources.%s.parser.type: must be one of [csv, json, xlsx, auto] (got %q)", name, parser.Type))
	}

	if parser.DateCol == "" {
		errs = append(errs, fmt.Errorf("sources.%s.parser.date_col: required field is missing", name))
	}

	if parser.DateLayout == "" {
		errs = append(errs, fmt.Errorf("sources.%s.parser.date_layout: required field is missing", name))
	}

	if parser.AmountCol == "" {
		errs = append(errs, fmt.Errorf("sources.%s.parser.amount_col: required field is missing", name))
	}

	if len(parser.Decimal) > 1 {
		errs = append(errs, fmt.Errorf("sources.%s.parser.decimal: must be a single character or empty (got %q)", name, parser.Decimal))
	}

	if len(parser.Thousands) > 1 {
		errs = append(errs, fmt.Errorf("sources.%s.parser.thousands: must be a single character or empty (got %q)", name, parser.Thousands))
	}

	if parser.Decimal != "" && parser.Thousands != "" && parser.Decimal == parser.Thousands {
		errs = append(errs, fmt.Errorf("sources.%s.parser.decimal and thousands cannot be the same (both %q)", name, parser.Decimal))
	}

	if parser.Multiplier <= 0 {
		errs = append(errs, fmt.Errorf("sources.%s.parser.multiplier: must be > 0 (got %d)", name, parser.Multiplier))
	}

	if parser.TZ != "" {
		if _, err := time.LoadLocation(parser.TZ); err != nil {
			errs = append(errs, fmt.Errorf("sources.%s.parser.tz: invalid timezone %q: %v", name, parser.TZ, err))
		}
	}

	switch parser.ResolvedDuplicatePolicy() {
	case DuplicatePolicyFlag, DuplicatePolicyKeep, DuplicatePolicyMerge, DuplicatePolicyLatest:
		// valid
	default:
		errs = append(errs, fmt.Errorf(
			"sources.%s.parser.duplicate_policy: must be one of [flag, keep, merge, latest] (got %q)",
			name, parser.DuplicatePolicy))
	}
	if parser.Financials != nil {
		if (parser.Financials.GrossCol == "") != (parser.Financials.NetCol == "") {
			errs = append(errs, fmt.Errorf("sources.%s.parser.financials: gross_col and net_col must be configured together", name))
		}
		knownFields := make(map[string]bool, len(parser.Financials.Fields)+2)
		for field := range parser.Financials.Fields {
			knownFields[field] = true
		}
		if parser.Financials.GrossCol != "" {
			knownFields["gross"] = true
		}
		if parser.Financials.NetCol != "" {
			knownFields["net"] = true
		}
		for field, col := range parser.Financials.Fields {
			if strings.TrimSpace(field) == "" || strings.TrimSpace(col) == "" {
				errs = append(errs, fmt.Errorf("sources.%s.parser.financials.fields: names and columns must be non-empty", name))
			}
		}
		for field, expectation := range parser.Financials.Expectations {
			if !knownFields[field] {
				errs = append(errs, fmt.Errorf("sources.%s.parser.financials.expectations.%s: field is not mapped", name, field))
			}
			forms := 0
			if expectation.Field != "" {
				forms++
			}
			if expectation.Fixed != nil {
				forms++
			}
			if expectation.Percentage != nil {
				forms++
			}
			if expectation.FixedPlusPercentage != nil {
				forms++
			}
			if len(expectation.Components) > 0 {
				forms++
			}
			if forms != 1 {
				errs = append(errs, fmt.Errorf("sources.%s.parser.financials.expectations.%s: exactly one expectation form is required", name, field))
			}
			if expectation.ToleranceMinor < 0 {
				errs = append(errs, fmt.Errorf("sources.%s.parser.financials.expectations.%s.tolerance_minor: must be >= 0", name, field))
			}
			if expectation.Operation != "" && expectation.Operation != "add" && expectation.Operation != "subtract" {
				errs = append(errs, fmt.Errorf("sources.%s.parser.financials.expectations.%s.operation: must be add or subtract", name, field))
			}
			if expectation.Percentage != nil && (expectation.Percentage.Rate < 0 || math.IsNaN(expectation.Percentage.Rate) || math.IsInf(expectation.Percentage.Rate, 0)) {
				errs = append(errs, fmt.Errorf("sources.%s.parser.financials.expectations.%s.percentage.rate: must be finite and >= 0", name, field))
			}
			if expectation.Percentage != nil && !knownFields[expectation.Percentage.Base] {
				errs = append(errs, fmt.Errorf("sources.%s.parser.financials.expectations.%s.percentage.base: unknown field %q", name, field, expectation.Percentage.Base))
			}
			if expectation.FixedPlusPercentage != nil && !knownFields[expectation.FixedPlusPercentage.Percentage.Base] {
				errs = append(errs, fmt.Errorf("sources.%s.parser.financials.expectations.%s.percentage.base: unknown field %q", name, field, expectation.FixedPlusPercentage.Percentage.Base))
			}
			for _, component := range expectation.Components {
				if !knownFields[component] {
					errs = append(errs, fmt.Errorf("sources.%s.parser.financials.expectations.%s.components: unknown field %q", name, field, component))
				}
				if component == field {
					errs = append(errs, fmt.Errorf("sources.%s.parser.financials.expectations.%s.components: self-reference is not allowed", name, field))
				}
			}
		}
		// Expectations may refer to other expectations through field form; reject
		// cycles so evaluation cannot recurse indefinitely when that graph grows.
		graph := make(map[string]string)
		for field, expectation := range parser.Financials.Expectations {
			if expectation.Field != "" && parser.Financials.Expectations[expectation.Field].Field != "" {
				graph[field] = expectation.Field
			}
		}
		for start := range graph {
			seen := map[string]bool{}
			for current := start; graph[current] != ""; current = graph[current] {
				if seen[current] {
					errs = append(errs, fmt.Errorf("sources.%s.parser.financials.expectations: dependency cycle involving %q", name, current))
					break
				}
				seen[current] = true
			}
		}
	}

	return errs
}

func validatePair(name string, pair Pair, sources map[string]Source) []error {
	var errs []error

	if pair.Left == "" {
		errs = append(errs, fmt.Errorf("pairs.%s.left: required field is missing", name))
	} else if _, exists := sources[pair.Left]; !exists {
		errs = append(errs, fmt.Errorf("pairs.%s.left: unknown source %q (available: %v)", name, pair.Left, getSourceNames(sources)))
	}

	if pair.Right != "" && len(pair.Rights) > 0 {
		errs = append(errs, fmt.Errorf("pairs.%s: right and rights are mutually exclusive — set exactly one", name))
	} else if pair.Right == "" && len(pair.Rights) == 0 {
		errs = append(errs, fmt.Errorf("pairs.%s: one of right or rights is required", name))
	}

	// Counterparts form an ordered pipeline. Reject duplicate names before source
	// lookup so no later pass can overwrite an earlier source's breakdown.
	seenCounterparts := make(map[string]int, len(pair.Counterparts()))
	for index, counterpart := range pair.Counterparts() {
		trimmedCounterpart := strings.TrimSpace(counterpart)
		if trimmedCounterpart == "" {
			errs = append(errs, fmt.Errorf("pairs.%s.rights[%d]: counterpart name cannot be empty", name, index))
			continue
		}
		if previousIndex, exists := seenCounterparts[trimmedCounterpart]; exists {
			errs = append(errs, fmt.Errorf(
				"pairs.%s.rights[%d]: duplicate counterpart %q (already configured at index %d)",
				name, index, counterpart, previousIndex,
			))
			continue
		}
		seenCounterparts[trimmedCounterpart] = index
		if _, exists := sources[counterpart]; !exists {
			errs = append(errs, fmt.Errorf("pairs.%s: unknown source %q (available: %v)", name, counterpart, getSourceNames(sources)))
		}
		if counterpart == pair.Left {
			errs = append(errs, fmt.Errorf("pairs.%s: left and right cannot be the same source", name))
		}
	}

	// Validate date_window format (e.g., "1d", "2d", "7d")
	if pair.DateWindow != "" {
		var days int
		var unit string
		if _, err := fmt.Sscanf(pair.DateWindow, "%d%s", &days, &unit); err != nil {
			errs = append(errs, fmt.Errorf("pairs.%s.date_window: invalid format (expected format like '1d', '2d'): %v", name, err))
		} else if unit != "d" && unit != "D" {
			errs = append(errs, fmt.Errorf("pairs.%s.date_window: unit must be 'd' or 'D' (got %q)", name, unit))
		} else if days < 0 {
			errs = append(errs, fmt.Errorf("pairs.%s.date_window: must be >= 0d (got %q)", name, pair.DateWindow))
		}
	}

	// Validate amount_tolerance_minor
	if pair.AmountToleranceMinor < 0 {
		errs = append(errs, fmt.Errorf("pairs.%s.amount_tolerance_minor: must be >= 0 (got %d)", name, pair.AmountToleranceMinor))
	}

	// Validate name_mode
	allowedNameModes := map[string]bool{
		"none":   true,
		"tokens": true,
	}
	if pair.NameMode != "" && !allowedNameModes[pair.NameMode] {
		errs = append(errs, fmt.Errorf("pairs.%s.name_mode: must be one of [none, tokens] (got %q)", name, pair.NameMode))
	}

	// Validate name_match_threshold. 1.0 is rejected, not just >1: the match
	// comparison is strict (score > threshold) and a Jaccard score never exceeds
	// 1.0, so a threshold of exactly 1.0 would be silently unreachable.
	if pair.NameMatchThreshold != 0 && (pair.NameMatchThreshold <= 0 || pair.NameMatchThreshold >= 1) {
		errs = append(errs, fmt.Errorf("pairs.%s.name_match_threshold: must be > 0 and < 1 (got %v)", name, pair.NameMatchThreshold))
	}

	// Validate result_mode.
	switch pair.ResultMode {
	case "", ResultModeAll, ResultModeExceptionsOnly, ResultModeSummaryOnly:
		// valid
	default:
		errs = append(errs, fmt.Errorf(
			"pairs.%s.result_mode: must be one of [all, exceptions_only, summary_only] (got %q)",
			name, pair.ResultMode))
	}

	// Validate passes when explicitly set.
	if pair.Passes != nil {
		if len(pair.Passes) == 0 {
			errs = append(errs, fmt.Errorf("pairs.%s.passes: must not be empty — omit the field to use default behavior", name))
		}
		if pair.NameMode == "tokens" {
			errs = append(errs, fmt.Errorf("pairs.%s: name_mode=tokens cannot be combined with explicit passes — add a %s pass instead", name, PassTypeNameTokensOneToOne))
		}
		validPassTypes := map[string]bool{
			PassTypeReferenceOneToOne:  true,
			PassTypeNameTokensOneToOne: true,
			PassTypeOneToMany:          true,
			PassTypeManyToMany:         true,
			PassTypeSubsetSum:          true,
		}
		for i, pass := range pair.Passes {
			if !validPassTypes[pass.Type] {
				errs = append(errs, fmt.Errorf("pairs.%s.passes[%d].type: unknown pass type %q (valid: %s, %s, %s, %s, %s)",
					name, i, pass.Type,
					PassTypeReferenceOneToOne, PassTypeNameTokensOneToOne, PassTypeOneToMany, PassTypeManyToMany, PassTypeSubsetSum))
			}
			if (pass.Type == PassTypeOneToMany || pass.Type == PassTypeManyToMany) && pass.GroupBy != "" {
				switch pass.GroupBy {
				case GroupByReference, GroupByName, GroupByGroupKey:
					// valid built-in key
				default:
					errs = append(errs, fmt.Errorf(
						"pairs.%s.passes[%d].group_by: unknown group key %q (built-in keys: reference, name, group_key)",
						name, i, pass.GroupBy))
				}
			}
			if pass.Type == PassTypeSubsetSum {
				if pass.MaxCandidates < 0 {
					errs = append(errs, fmt.Errorf("pairs.%s.passes[%d].max_candidates: must be >= 0 (got %d)", name, i, pass.MaxCandidates))
				}
				if pass.MaxSubsetSize < 0 {
					errs = append(errs, fmt.Errorf("pairs.%s.passes[%d].max_subset_size: must be >= 0 (got %d)", name, i, pass.MaxSubsetSize))
				}
				if pass.MaxAlternatives < 0 {
					errs = append(errs, fmt.Errorf("pairs.%s.passes[%d].max_alternatives: must be >= 0 (got %d)", name, i, pass.MaxAlternatives))
				}
				if pass.TimeoutMS < 0 {
					errs = append(errs, fmt.Errorf("pairs.%s.passes[%d].timeout_ms: must be >= 0 (got %d)", name, i, pass.TimeoutMS))
				}
			}
		}
	}

	return errs
}

func validateIndex(index IndexCfg) []error {
	var errs []error
	backend := index.Backend
	if backend == "" {
		backend = "memory"
	}
	allowed := map[string]bool{
		"memory":      true,
		"disk":        true,
		"auto":        true,
		"partitioned": true,
	}
	if !allowed[backend] {
		errs = append(errs, fmt.Errorf("index.backend: must be one of [memory, disk, auto, partitioned] (got %q)", index.Backend))
	}
	if index.PartitionCount == 1 || index.PartitionCount < 0 {
		errs = append(errs, fmt.Errorf("index.partition_count: must be 0 (default) or >= 2 (got %d)", index.PartitionCount))
	}
	if index.AutoMaxRightFileMB < 0 {
		errs = append(errs, fmt.Errorf("index.auto_max_right_file_mb: must be >= 0 (got %d)", index.AutoMaxRightFileMB))
	}
	if index.MaxMemoryMB < 0 {
		errs = append(errs, fmt.Errorf("index.max_memory_mb: must be >= 0 (got %d)", index.MaxMemoryMB))
	}
	if index.MaxMemoryMB > math.MaxInt64/(1024*1024) {
		errs = append(errs, fmt.Errorf("index.max_memory_mb is too large to convert to bytes (got %d)", index.MaxMemoryMB))
	}
	if index.MaxTempDiskMB < 0 {
		errs = append(errs, fmt.Errorf("index.max_temp_disk_mb: must be >= 0 (got %d)", index.MaxTempDiskMB))
	}
	if index.MaxTempDiskMB > math.MaxInt64/(1024*1024) {
		errs = append(errs, fmt.Errorf("index.max_temp_disk_mb is too large to convert to bytes (got %d)", index.MaxTempDiskMB))
	}
	if index.SpillDir != "" && strings.Contains(index.SpillDir, "..") {
		errs = append(errs, fmt.Errorf("index.spill_dir: must not contain '..' (got %q)", index.SpillDir))
	}
	return errs
}

func getSourceNames(sources map[string]Source) []string {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
