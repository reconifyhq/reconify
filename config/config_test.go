package config

import (
	"errors"
	"math"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParserFinancialsConfigRoundTrips(t *testing.T) {
	var cfg CSVParserCfg
	if err := yaml.Unmarshal([]byte(`
type: csv
date_col: date
date_layout: 2006-01-02
amount_col: amount
multiplier: 100
financials:
  gross_col: gross
  fields:
    fee: fee
    tax: tax
  expectations:
    fee:
      percentage:
        base: gross
        rate: 1.5
      operation: subtract
      tolerance_minor: 1
`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Financials == nil || cfg.Financials.GrossCol != "gross" {
		t.Fatalf("financials not decoded: %#v", cfg.Financials)
	}
	fee := cfg.Financials.Expectations["fee"]
	if fee.Percentage == nil || fee.Percentage.Base != "gross" || fee.Percentage.Rate != 1.5 {
		t.Fatalf("fee expectation not decoded: %#v", fee)
	}
}

func TestFinancialsValidationRejectsInvalidDependencies(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Sources["left"] = Source{FilePattern: "left.csv", Parser: CSVParserCfg{
		Type: "csv", DateCol: "date", DateLayout: "2006-01-02", AmountCol: "amount", Multiplier: 100,
		Financials: &FinancialsCfg{GrossCol: "gross", Expectations: map[string]ExpectationCfg{
			"fee": {Percentage: &PercentageCfg{Base: "missing", Rate: 1}},
		}},
	}}
	var messages []string
	for _, err := range cfg.Validate() {
		messages = append(messages, err.Error())
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "gross_col and net_col") || !strings.Contains(joined, "unknown field \"missing\"") || !strings.Contains(joined, "field is not mapped") {
		t.Fatalf("validation errors missing: %s", joined)
	}
}

func baseValidConfig() Config {
	return Config{
		Version:  1,
		Timezone: "UTC",
		Sources: map[string]Source{
			"left": {
				FilePattern: "left.csv",
				Parser: CSVParserCfg{
					Type:       "csv",
					DateCol:    "date",
					DateLayout: "2006-01-02",
					AmountCol:  "amount",
					Multiplier: 100,
				},
			},
			"right": {
				FilePattern: "right.csv",
				Parser: CSVParserCfg{
					Type:       "csv",
					DateCol:    "date",
					DateLayout: "2006-01-02",
					AmountCol:  "amount",
					Multiplier: 100,
				},
			},
		},
		Pairs: map[string]Pair{
			"p": {
				Left:                 "left",
				Right:                "right",
				DateWindow:           "0d",
				AmountToleranceMinor: 0,
				NameMode:             "none",
			},
		},
	}
}

func TestConfigValidate_IndexBackend(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Index = IndexCfg{Backend: "disk"}

	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("expected valid config, got errors: %v", errs)
	}
}

func TestConfigValidate_IndexBackendInvalid(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Index = IndexCfg{Backend: "ramdisk"}

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for invalid index.backend")
	}
}

func TestConfigValidate_IndexAutoMaxRightFileMBNegative(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Index = IndexCfg{Backend: "auto", AutoMaxRightFileMB: -1}

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for negative auto_max_right_file_mb")
	}
}

func TestConfigValidate_IndexResourceBudgets(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Index = IndexCfg{Backend: "auto", MaxMemoryMB: 4096, MaxTempDiskMB: 8192}
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("expected valid resource budgets, got errors: %v", errs)
	}
}

func TestConfigValidate_IndexResourceBudgetsNegative(t *testing.T) {
	for name, index := range map[string]IndexCfg{
		"memory": {Backend: "memory", MaxMemoryMB: -1},
		"disk":   {Backend: "disk", MaxTempDiskMB: -1},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Index = index
			if errs := cfg.Validate(); len(errs) == 0 {
				t.Fatal("expected validation error for negative resource budget")
			}
		})
	}
}

func TestConfigValidate_IndexResourceBudgetsRejectByteOverflow(t *testing.T) {
	cfg := &Config{Version: 1, Index: IndexCfg{MaxMemoryMB: math.MaxInt64, MaxTempDiskMB: math.MaxInt64}}
	errs := cfg.Validate()
	joined := errors.Join(errs...)
	if joined == nil || !strings.Contains(joined.Error(), "too large to convert to bytes") {
		t.Fatalf("Validate() errors = %v, want byte-conversion overflow", errs)
	}
}

func TestCSVParserCfg_GroupColPassthrough(t *testing.T) {
	yamlSrc := `
type: csv
date_col: date
date_layout: "2006-01-02"
amount_col: amount
ref_col: txn_id
group_col: invoice_number
`
	var cfg CSVParserCfg
	if err := yaml.Unmarshal([]byte(yamlSrc), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.GroupCol != "invoice_number" {
		t.Errorf("GroupCol = %q, want %q", cfg.GroupCol, "invoice_number")
	}
	if cfg.RefCol != "txn_id" {
		t.Errorf("RefCol = %q, want %q", cfg.RefCol, "txn_id")
	}
}

func TestPair_Counterparts(t *testing.T) {
	tests := []struct {
		name string
		pair Pair
		want []string
	}{
		{name: "right only", pair: Pair{Right: "stripe"}, want: []string{"stripe"}},
		{name: "rights only", pair: Pair{Rights: []string{"stripe", "paypal"}}, want: []string{"stripe", "paypal"}},
		{name: "neither set", pair: Pair{}, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.pair.Counterparts()
			if len(got) != len(tc.want) {
				t.Fatalf("Counterparts() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Counterparts() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func withLedgerSource(cfg Config) Config {
	cfg.Sources["ledger"] = Source{
		FilePattern: "ledger.csv",
		Parser: CSVParserCfg{
			Type:       "csv",
			DateCol:    "date",
			DateLayout: "2006-01-02",
			AmountCol:  "amount",
			Multiplier: 100,
		},
	}
	return cfg
}

func TestConfigValidate_RightAndRightsMutuallyExclusive(t *testing.T) {
	cfg := withLedgerSource(baseValidConfig())
	p := cfg.Pairs["p"]
	p.Rights = []string{"right", "ledger"}
	cfg.Pairs["p"] = p

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error when both right and rights are set")
	}
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "mutually exclusive") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a mutually-exclusive error, got: %v", errs)
	}
}

func TestConfigValidate_RightsRequiredWhenRightUnset(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.Right = ""
	cfg.Pairs["p"] = p

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error when neither right nor rights is set")
	}
}

func TestConfigValidate_RightsValid(t *testing.T) {
	cfg := withLedgerSource(baseValidConfig())
	p := cfg.Pairs["p"]
	p.Right = ""
	p.Rights = []string{"right", "ledger"}
	cfg.Pairs["p"] = p

	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("expected valid multi-counterpart config, got errors: %v", errs)
	}
}

func TestConfigValidate_RightsRejectsDuplicateCounterparts(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.Right = ""
	p.Rights = []string{"right", "right"}
	cfg.Pairs["p"] = p

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for duplicate counterpart")
	}
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "duplicate counterpart") && strings.Contains(err.Error(), "rights[1]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected indexed duplicate counterpart error, got: %v", errs)
	}
}

func TestConfigValidate_RightsRejectsBlankCounterpart(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.Right = ""
	p.Rights = []string{"right", "   "}
	cfg.Pairs["p"] = p

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for blank counterpart")
	}
	for _, err := range errs {
		if strings.Contains(err.Error(), "counterpart name cannot be empty") && strings.Contains(err.Error(), "rights[1]") {
			return
		}
	}
	t.Fatalf("expected indexed blank counterpart error, got: %v", errs)
}

func TestConfigValidate_NameMatchThresholdBounds(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		wantErr   bool
	}{
		{name: "unset defaults fine", threshold: 0, wantErr: false},
		{name: "valid mid-range", threshold: 0.5, wantErr: false},
		{name: "valid just below upper bound", threshold: 0.999, wantErr: false},
		{name: "negative", threshold: -0.1, wantErr: true},
		{name: "above 1", threshold: 1.1, wantErr: true},
		{name: "exactly 1 is unreachable under strict >", threshold: 1, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseValidConfig()
			p := cfg.Pairs["p"]
			p.NameMatchThreshold = tc.threshold
			cfg.Pairs["p"] = p

			errs := cfg.Validate()
			if tc.wantErr && len(errs) == 0 {
				t.Fatal("expected validation error, got none")
			}
			if !tc.wantErr && len(errs) > 0 {
				t.Fatalf("expected no validation error, got: %v", errs)
			}
		})
	}
}

func TestConfigValidate_DateWindowNegative(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Pairs["p"] = Pair{
		Left:                 "left",
		Right:                "right",
		DateWindow:           "-1d",
		AmountToleranceMinor: 0,
		NameMode:             "none",
	}

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for negative date_window")
	}
}

func TestConfigValidate_IndexSpillDirRejectsParentTraversal(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Index = IndexCfg{Backend: "disk", SpillDir: "../tmp"}

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for spill_dir containing '..'")

	}
}

func TestConfigValidate_ParserTypes(t *testing.T) {
	for _, parserType := range []string{"", "auto", "csv", "json", "xlsx"} {
		t.Run(parserType, func(t *testing.T) {
			cfg := baseValidConfig()
			left := cfg.Sources["left"]
			left.Parser.Type = parserType
			cfg.Sources["left"] = left
			if errs := cfg.Validate(); len(errs) > 0 {
				t.Fatalf("expected valid parser type %q, got errors: %v", parserType, errs)
			}
		})
	}
}

func TestConfigValidate_ParserTypeInvalid(t *testing.T) {
	cfg := baseValidConfig()
	left := cfg.Sources["left"]
	left.Parser.Type = "xml"
	cfg.Sources["left"] = left

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for invalid parser type")
	}
}

func TestConfigValidate_PassesOmitted_PreservesLegacyBehavior(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.NameMode = "tokens"
	p.NameMatchThreshold = 0.6
	cfg.Pairs["p"] = p

	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("expected valid legacy config without passes, got errors: %v", errs)
	}
}

func TestConfigValidate_PassesExplicit_Valid(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.Passes = []PassConfig{
		{Type: PassTypeReferenceOneToOne},
		{Type: PassTypeNameTokensOneToOne},
	}
	cfg.Pairs["p"] = p

	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("expected valid explicit passes config, got errors: %v", errs)
	}
}

func TestConfigValidate_PassesUnknownType(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.Passes = []PassConfig{
		{Type: PassTypeReferenceOneToOne},
		{Type: "fuzzy_match"},
	}
	cfg.Pairs["p"] = p

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for unknown pass type")
	}
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "passes[1].type") && strings.Contains(err.Error(), "fuzzy_match") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error identifying passes[1].type, got: %v", errs)
	}
}

func TestConfigValidate_PassesEmpty_IsRejected(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.Passes = []PassConfig{}
	cfg.Pairs["p"] = p

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for empty passes list")
	}
}

func TestConfigValidate_PassesWithNameModeTokens_IsRejected(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.NameMode = "tokens"
	p.Passes = []PassConfig{
		{Type: PassTypeReferenceOneToOne},
	}
	cfg.Pairs["p"] = p

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error when name_mode=tokens is combined with passes")
	}
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "name_mode=tokens") && strings.Contains(err.Error(), PassTypeNameTokensOneToOne) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected migration-oriented error about name_mode=tokens, got: %v", errs)
	}
}

func TestConfigValidate_SupportedPassTypes_AreValid(t *testing.T) {
	for _, passType := range []string{PassTypeReferenceOneToOne, PassTypeNameTokensOneToOne, PassTypeManyToMany} {
		t.Run(passType, func(t *testing.T) {
			cfg := baseValidConfig()
			p := cfg.Pairs["p"]
			p.Passes = []PassConfig{{Type: passType}}
			cfg.Pairs["p"] = p

			if errs := cfg.Validate(); len(errs) > 0 {
				t.Fatalf("expected pass type %q to be valid, got errors: %v", passType, errs)
			}
		})
	}
}

func TestConfigValidate_OneToManyPass_IsAccepted(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.Passes = []PassConfig{{Type: "one_to_many"}}
	cfg.Pairs["p"] = p

	errs := cfg.Validate()
	if len(errs) != 0 {
		t.Fatalf("expected one_to_many pass to be accepted, got errors: %v", errs)
	}
}

func TestConfigValidate_ManyToManyPass_IsAccepted(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.Passes = []PassConfig{{Type: PassTypeManyToMany}}
	cfg.Pairs["p"] = p

	errs := cfg.Validate()
	if len(errs) != 0 {
		t.Fatalf("expected many_to_many pass to be accepted, got errors: %v", errs)
	}
}

func TestConfigValidate_PassesCompatibleWithRights(t *testing.T) {
	cfg := withLedgerSource(baseValidConfig())
	p := cfg.Pairs["p"]
	p.Right = ""
	p.Rights = []string{"right", "ledger"}
	p.Passes = []PassConfig{{Type: PassTypeReferenceOneToOne}}
	cfg.Pairs["p"] = p

	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("expected passes to be compatible with rights, got errors: %v", errs)
	}
}

func TestConfigValidate_OneToManyGroupBy_DefaultsToReference(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	pass := PassConfig{Type: PassTypeOneToMany}
	p.Passes = []PassConfig{pass}
	cfg.Pairs["p"] = p

	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("expected valid config with no group_by, got errors: %v", errs)
	}
	if pass.ResolvedGroupBy() != GroupByReference {
		t.Errorf("ResolvedGroupBy() = %q, want %q", pass.ResolvedGroupBy(), GroupByReference)
	}
}

func TestConfigValidate_OneToManyGroupBy_KnownKeysValid(t *testing.T) {
	for _, key := range []string{GroupByReference, GroupByName, GroupByGroupKey} {
		t.Run(key, func(t *testing.T) {
			cfg := baseValidConfig()
			p := cfg.Pairs["p"]
			p.Passes = []PassConfig{
				{Type: PassTypeOneToMany, GroupBy: key},
				{Type: PassTypeManyToMany, GroupBy: key},
			}
			cfg.Pairs["p"] = p

			if errs := cfg.Validate(); len(errs) > 0 {
				t.Fatalf("expected group_by %q to be valid, got errors: %v", key, errs)
			}
		})
	}
}

func TestConfigValidate_ManyToManyGroupBy_UnknownKeyRejected(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.Passes = []PassConfig{{Type: PassTypeManyToMany, GroupBy: "settlement_id"}}
	cfg.Pairs["p"] = p

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for unknown group_by key")
	}
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "passes[0].group_by") &&
			strings.Contains(err.Error(), "settlement_id") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error identifying passes[0].group_by, got: %v", errs)
	}
}

func TestConfigValidate_OneToManyGroupBy_UnknownKeyRejected(t *testing.T) {
	cfg := baseValidConfig()
	p := cfg.Pairs["p"]
	p.Passes = []PassConfig{{Type: PassTypeOneToMany, GroupBy: "invoice_number"}}
	cfg.Pairs["p"] = p

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for unknown group_by key")
	}
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "passes[0].group_by") &&
			strings.Contains(err.Error(), "invoice_number") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error identifying passes[0].group_by, got: %v", errs)
	}
}

func TestConfigValidate_DuplicatePolicy_ValidValues(t *testing.T) {
	for _, policy := range []string{"flag", "keep", "merge", "latest"} {
		t.Run(policy, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Sources["left"] = Source{
				FilePattern: "left.csv",
				Parser: CSVParserCfg{
					Type:            "csv",
					DateCol:         "date",
					DateLayout:      "2006-01-02",
					AmountCol:       "amount",
					Multiplier:      100,
					DuplicatePolicy: DuplicatePolicy(policy),
				},
			}
			if errs := cfg.Validate(); len(errs) > 0 {
				t.Fatalf("unexpected validation errors for duplicate_policy=%q: %v", policy, errs)
			}
		})
	}
}

func TestConfigValidate_DuplicatePolicy_Empty_DefaultsToFlag(t *testing.T) {
	cfg := baseValidConfig()
	// No duplicate_policy set — should resolve to "flag" without error.
	if errs := cfg.Validate(); len(errs) > 0 {
		t.Fatalf("unexpected validation errors with no duplicate_policy: %v", errs)
	}
	dp := cfg.Sources["left"].Parser.ResolvedDuplicatePolicy()
	if dp != DuplicatePolicyFlag {
		t.Errorf("ResolvedDuplicatePolicy() = %q, want %q", dp, DuplicatePolicyFlag)
	}
}

func TestConfigValidate_DuplicatePolicy_UnknownValue(t *testing.T) {
	cfg := baseValidConfig()
	src := cfg.Sources["left"]
	src.Parser.DuplicatePolicy = "deduplicate"
	cfg.Sources["left"] = src

	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Fatal("expected validation error for unknown duplicate_policy, got none")
	}
	found := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "duplicate_policy") && strings.Contains(err.Error(), "deduplicate") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error mentioning duplicate_policy and value, got: %v", errs)
	}
}

func TestConfigValidate_DuplicatePolicy_YAMLRoundTrip(t *testing.T) {
	raw := `
version: 1
sources:
  left:
    file_pattern: left.csv
    parser:
      type: csv
      date_col: date
      date_layout: "2006-01-02"
      amount_col: amount
      multiplier: 100
      duplicate_policy: merge
  right:
    file_pattern: right.csv
    parser:
      type: csv
      date_col: date
      date_layout: "2006-01-02"
      amount_col: amount
      multiplier: 100
pairs:
  p:
    left: left
    right: right
    date_window: 0d
`
	var parsed Config
	if err := yaml.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if got := parsed.Sources["left"].Parser.DuplicatePolicy; got != DuplicatePolicyMerge {
		t.Errorf("DuplicatePolicy = %q, want %q", got, DuplicatePolicyMerge)
	}
	if got := parsed.Sources["right"].Parser.ResolvedDuplicatePolicy(); got != DuplicatePolicyFlag {
		t.Errorf("right ResolvedDuplicatePolicy() = %q, want %q (default)", got, DuplicatePolicyFlag)
	}
}

func TestConfigValidate_ResultMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    ResultMode
		wantErr bool
	}{
		{name: "unset is valid", mode: "", wantErr: false},
		{name: "all is valid", mode: ResultModeAll, wantErr: false},
		{name: "exceptions_only is valid", mode: ResultModeExceptionsOnly, wantErr: false},
		{name: "summary_only is valid", mode: ResultModeSummaryOnly, wantErr: false},
		{name: "unknown value", mode: "unknown", wantErr: true},
		{name: "typo", mode: "exception_only", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseValidConfig()
			p := cfg.Pairs["p"]
			p.ResultMode = tc.mode
			cfg.Pairs["p"] = p

			errs := cfg.Validate()
			if tc.wantErr && len(errs) == 0 {
				t.Fatal("expected validation error, got none")
			}
			if !tc.wantErr && len(errs) > 0 {
				t.Fatalf("expected no errors, got: %v", errs)
			}
		})
	}
}

func TestConfigYAML_ResultMode(t *testing.T) {
	src := `version: 1
sources:
  left:
    file_pattern: left.csv
    parser:
      type: csv
      date_col: date
      date_layout: "2006-01-02"
      amount_col: amount
      multiplier: 100
  right:
    file_pattern: right.csv
    parser:
      type: csv
      date_col: date
      date_layout: "2006-01-02"
      amount_col: amount
      multiplier: 100
pairs:
  p:
    left: left
    right: right
    date_window: 0d
    result_mode: exceptions_only
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if got := cfg.Pairs["p"].ResultMode; got != ResultModeExceptionsOnly {
		t.Errorf("ResultMode = %q, want %q", got, ResultModeExceptionsOnly)
	}
}

func TestResolvedCandidateFilters(t *testing.T) {
	t.Run("unset defaults all true", func(t *testing.T) {
		p := PassConfig{Type: PassTypeSubsetSum}
		got := p.ResolvedCandidateFilters()
		want := SubsetSumFilters{Currency: true, DateWindow: true, SameSign: true}
		if got != want {
			t.Errorf("ResolvedCandidateFilters() = %+v, want %+v", got, want)
		}
	})

	t.Run("explicit all-false is honored, not treated as unset", func(t *testing.T) {
		explicit := SubsetSumFilters{Currency: false, DateWindow: false, SameSign: false}
		p := PassConfig{Type: PassTypeSubsetSum, CandidateFilters: &explicit}
		got := p.ResolvedCandidateFilters()
		if got != explicit {
			t.Errorf("ResolvedCandidateFilters() = %+v, want %+v (explicit all-false should not fall back to defaults)", got, explicit)
		}
	})

	t.Run("explicit partial override is honored", func(t *testing.T) {
		explicit := SubsetSumFilters{Currency: true, DateWindow: false, SameSign: true}
		p := PassConfig{Type: PassTypeSubsetSum, CandidateFilters: &explicit}
		got := p.ResolvedCandidateFilters()
		if got != explicit {
			t.Errorf("ResolvedCandidateFilters() = %+v, want %+v", got, explicit)
		}
	})
}

func TestValidateReturnsStructuredErrors(t *testing.T) {
	cfg := &Config{
		Version: 2,
		Sources: map[string]Source{
			"left": {FilePattern: "left.csv", Parser: ParserCfg{AmountCol: "amount", DateLayout: "2006-01-02", Multiplier: 100}},
		},
		Pairs: map[string]Pair{"p": {Left: "left", Right: "ghost"}},
	}
	errs := cfg.Validate()
	structured := ValidationErrors(errs)
	if len(structured) != len(errs) || len(errs) == 0 {
		t.Fatalf("got %d structured errors for %d errors", len(structured), len(errs))
	}
	byPath := map[string]string{}
	for i, entry := range structured {
		if entry.Error() != errs[i].Error() {
			t.Errorf("text changed: %q vs %q", entry.Error(), errs[i].Error())
		}
		byPath[entry.Path] = entry.Message
	}
	if byPath["version"] != "version must be 1 (got 2)" {
		t.Errorf("version entry = %q", byPath["version"])
	}
	if byPath["sources.left.parser.date_col"] != "required field is missing" {
		t.Errorf("date_col entry = %q (all: %v)", byPath["sources.left.parser.date_col"], byPath)
	}
	if _, ok := byPath["pairs.p.right"]; !ok && byPath["pairs.p"] == "" {
		t.Errorf("no entry for the pair: %v", byPath)
	}
}

func TestValidationErrorsKeepsForeignErrorsPathless(t *testing.T) {
	got := ValidationErrors([]error{errors.New("something else")})
	if len(got) != 1 || got[0].Path != "" || got[0].Message != "something else" {
		t.Fatalf("got %+v", got)
	}
}
