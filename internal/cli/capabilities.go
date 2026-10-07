package cli

import (
	"github.com/reconifyhq/reconify/config"
	"github.com/reconifyhq/reconify/schemas"
)

func capabilityFormats() map[string]schemas.FormatCapability {
	return map[string]schemas.FormatCapability{
		"reconcile": {
			Formats:                 []string{"json", "json-stream", "ndjson", "csv", "table"},
			Default:                 "json",
			StreamingFormats:        []string{"ndjson", "csv", "json-stream"},
			AuditCompatible:         []string{"json", "json-stream", "ndjson"},
			DeterministicCompatible: []string{"json"},
		},
		"parse": {
			Formats:          []string{"ndjson", "csv", "table", "json"},
			Default:          "ndjson",
			StreamingFormats: []string{"ndjson", "csv"},
		},
		"inspect": {
			Formats: []string{"json"},
			Default: "json",
		},
		"config infer": {
			Formats: []string{"json"},
			Default: "json",
		},
		"explain": {
			Formats: []string{"json"},
			Default: "json",
		},
	}
}

func capabilityNDJSONEvents() map[string]string {
	return map[string]string{
		"run_info":                 "Run provenance: tool version, file hashes, timestamps; --auto also includes the exact inferred config and mapping confidence. Emitted by --audit or --auto. Always the first line. Payload: RunInfo.",
		"match":                    "Left+right pair that reconciled cleanly. Payload: {left: Transaction, right: Transaction}.",
		"amount_diff":              "Reference matched but amount differs beyond amount_tolerance_minor. Payload: {left, right, diff_minor}.",
		"timing_diff":              "Reference+amount matched but date is outside date_window. Payload: {left, right, days_diff}.",
		"unmatched_left":           "Left transaction with no match on the right side. Payload: Transaction.",
		"unmatched_right":          "Right transaction with no match on the left side. Payload: Transaction.",
		"grouped_match":            "One left matched to N rights (one_to_many pass). Payload: {left, rights: []Transaction}.",
		"grouped_amount_diff":      "Grouped match where sum of right amounts differs beyond tolerance. Payload: {left, rights, diff_minor}.",
		"grouped_timing_diff":      "Grouped match where at least one right date is outside date_window. Payload: {left, rights, days_diff}.",
		"many_to_many_match":       "M left rows matched to N right rows by shared group key. Payload: {lefts: []Transaction, rights: []Transaction}.",
		"many_to_many_amount_diff": "N:M grouped match where summed left and right amounts differ beyond tolerance. Payload: {lefts, rights, diff_minor}.",
		"many_to_many_timing_diff": "N:M grouped match where at least one cross-side date is outside date_window. Payload: {lefts, rights, days_diff}.",
		"subset_sum_match":         "One left row matched to a bounded subset of right rows summing within tolerance (subset_sum pass). Payload: {left, rights: []Transaction}.",
		"subset_sum_ambiguous":     "Multiple valid right-row subsets sum within tolerance of the left row; manual review required. Payload: {left, alternatives: [][]Transaction}.",
		"subset_sum_skipped":       "Left row skipped by the subset_sum pass because its candidate pool or search exceeded configured bounds. Payload: {left, reason}.",
		"financial_effect_match":   "Configured financial expectation matched. Payload: FinancialEffectFinding.",
		"financial_effect_diff":    "Configured financial expectation exceeded tolerance. Payload: FinancialEffectFinding.",
		"financial_unchecked":      "Mapped financial field had no expectation rule; informational only. Payload: FinancialEffectFinding.",
		"settlement_match":         "Gross-to-net settlement identity matched within tolerance. Payload: SettlementFinding.",
		"settlement_diff":          "Gross-to-net settlement identity differed beyond tolerance. Payload: SettlementFinding.",
		"ambiguous_group":          "Multiple left rows share the same reference; manual review required. Payload: {reference, left_rows, rights}.",
		"duplicate":                "Transactions within the same source sharing the same reference. Payload: {source, reference, transactions}.",
		"source_summary":           "Per-counterpart summary for 1-N runs. One per counterpart, before final summary. Payload: {source: string, summary: Summary}.",
		"summary":                  "Aggregate counts and match rate. Always the last line. Payload: Summary.",
	}
}

func capabilityExitCodes() map[string]string {
	return map[string]string{
		"0": "Success.",
		"1": "Unexpected or internal error.",
		"2": "Config or validation error (bad YAML, missing pair/source, column not found).",
		"3": "Reconcile completed with unmatched rows. Only returned when --fail-if-unmatched is set.",
		"4": "Reconcile completed with exception events (amount_diff, timing_diff, financial, settlement, or unmatched). Only returned when --fail-if-exceptions is set. Takes precedence over exit code 3 when both flags are set.",
	}
}

func capabilityCommands() map[string]schemas.CommandCapability {
	return map[string]schemas.CommandCapability{
		"capabilities":           {Description: "Describe the installed Reconify Engine interface and its versioned schemas.", Interactive: false},
		"config check-source":    {Description: "Check whether an input file matches a configured source mapping.", Interactive: false},
		"config infer":           {Description: "Infer a confidence-gated reconify.yaml proposal from two input files.", Interactive: false},
		"explain":                {Description: "Summarize a completed reconciliation result deterministically.", Interactive: false},
		"config schema":          {Description: "Print the machine-readable configuration and output metadata schema.", Interactive: false},
		"config validate":        {Description: "Validate a reconify.yaml configuration.", Interactive: false},
		"config init":            {Description: "Interactively create a reconify.yaml configuration from sample files.", Interactive: true},
		"inspect":                {Description: "Deterministically profile an input file's format and column types before writing a config.", Interactive: false},
		"mcp":                    {Description: "Serve the Reconify Engine MCP server over stdio for MCP clients.", Interactive: false},
		"parse":                  {Description: "Parse an input file according to a configured source parser.", Interactive: false},
		"reconcile":              {Description: "Run a configured or confidence-gated auto-inferred reconciliation and emit result events.", Interactive: false},
		"schema capabilities":    {Description: "Print the published capabilities schema.", Interactive: false},
		"schema config-proposal": {Description: "Print the published config proposal schema.", Interactive: false},
		"schema diagnostic":      {Description: "Print the published structured diagnostic schema.", Interactive: false},
		"schema explanation":     {Description: "Print the published explanation schema.", Interactive: false},
		"schema profile":         {Description: "Print the published file profile schema.", Interactive: false},
		"schema result":          {Description: "Print the published reconciliation result schema.", Interactive: false},
	}
}

func capabilityMatching() schemas.MatchingCapabilities {
	return schemas.MatchingCapabilities{
		Passes: []schemas.MatchingPass{
			{
				Type:        config.PassTypeReferenceOneToOne,
				Description: "Match one left row to one right row by reference, amount, and date window.",
				Cardinality: "one_to_one",
				Requires:    []string{"reference"},
			},
			{
				Type:        config.PassTypeNameTokensOneToOne,
				Description: "Match remaining rows one-to-one by token similarity of counterpart names.",
				Cardinality: "one_to_one",
				Requires:    []string{"name"},
			},
			{
				Type:        config.PassTypeOneToMany,
				Description: "Match one left row to multiple right rows by a shared group key.",
				Cardinality: "one_to_many",
				GroupBy:     []string{config.GroupByReference, config.GroupByName, config.GroupByGroupKey},
			},
			{
				Type:        config.PassTypeManyToMany,
				Description: "Match multiple left rows to multiple right rows by a shared group key.",
				Cardinality: "many_to_many",
				GroupBy:     []string{config.GroupByReference, config.GroupByName, config.GroupByGroupKey},
			},
			{
				Type:        config.PassTypeSubsetSum,
				Description: "Match one left row to a bounded subset of right rows whose amounts sum within tolerance; no shared key required.",
				Cardinality: "one_to_many",
			},
		},
		DefaultPasses: []string{config.PassTypeReferenceOneToOne},
		GroupKeys:     []string{config.GroupByReference, config.GroupByName, config.GroupByGroupKey},
	}
}

func capabilityErrorCodes() map[string]schemas.ErrorCodeCapability {
	return map[string]schemas.ErrorCodeCapability{
		diagnosticCodeConfigInvalid: {
			Category: diagnosticCategoryConfig, LegacyCode: "config_error", ExitCode: ErrCodeConfig,
			Description: "Configuration or command validation failed.",
		},
		diagnosticCodeInputUnreadable: {
			Category: diagnosticCategoryInput, LegacyCode: "config_error", ExitCode: ErrCodeConfig,
			Description: "An input file could not be read.",
		},
		diagnosticCodeInputMismatch: {
			Category: diagnosticCategoryInput, LegacyCode: "config_error", ExitCode: ErrCodeConfig,
			Description: "An input file does not match its configured source mapping.",
		},
		diagnosticCodeInferenceAmbiguous: {
			Category: diagnosticCategoryInference, LegacyCode: "config_error", ExitCode: ErrCodeConfig,
			Description: "Inference could not select confident date, amount, or reference mappings.",
		},
		diagnosticCodeInteractiveUnsupported: {
			Category: diagnosticCategoryConfig, LegacyCode: "config_error", ExitCode: ErrCodeConfig,
			Description: "The requested command requires interactive input and cannot run in agent mode.",
		},
		diagnosticCodeExecutionFailed: {
			Category: diagnosticCategoryExecution, LegacyCode: "error", ExitCode: 1,
			Description: "Execution or output failed unexpectedly.",
		},
		diagnosticCodeUnmatchedRows: {
			Category: diagnosticCategoryExecution, LegacyCode: "unmatched", ExitCode: ErrCodeUnmatched,
			Description: "Reconciliation completed with unmatched rows under --fail-if-unmatched.",
		},
		diagnosticCodeExceptionsFound: {
			Category: diagnosticCategoryExecution, LegacyCode: "exceptions", ExitCode: ErrCodeExceptions,
			Description: "Reconciliation completed with exception events under --fail-if-exceptions.",
		},
		diagnosticCodeInternalError: {
			Category: diagnosticCategoryInternal, LegacyCode: "error", ExitCode: 1,
			Description: "An unexpected internal or command error occurred.",
		},
	}
}

func buildCapabilities() schemas.Capabilities {
	return schemas.Capabilities{
		Schema:           schemas.CapabilitiesSchemaV1,
		ProtocolVersion:  "v1",
		ProtocolVersions: []string{"v1"},
		Engine: schemas.EngineCapabilities{
			Name:    "Reconify Engine",
			Version: cliVersion,
		},
		Commands:    capabilityCommands(),
		Formats:     capabilityFormats(),
		Matching:    capabilityMatching(),
		ResultModes: []string{string(config.ResultModeAll), string(config.ResultModeExceptionsOnly), string(config.ResultModeSummaryOnly)},
		Schemas: map[string]string{
			"capabilities":    schemas.CapabilitiesSchemaV1,
			"config_proposal": schemas.ConfigProposalSchemaV1,
			"diagnostic":      schemas.DiagnosticSchemaV1,
			"explanation":     schemas.ExplanationSchemaV1,
			"profile":         schemas.ProfileSchemaV1,
			"result":          schemas.ResultSchemaV1,
		},
		ErrorCodes: capabilityErrorCodes(),
		ExitCodes:  capabilityExitCodes(),
	}
}

func formatMetadataForConfigSchema() map[string]schemas.FormatCapability {
	return capabilityFormats()
}
