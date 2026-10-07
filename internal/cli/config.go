// Package cli provides command-line interface commands for Reconify
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/reconifyhq/reconify/config"
	"github.com/reconifyhq/reconify/engine"
	"github.com/reconifyhq/reconify/engine/sample"
	"github.com/reconifyhq/reconify/schemas"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management commands",
		Long:  "Commands for validating and checking configuration files",
	}

	cmd.AddCommand(newConfigValidateCmd())
	cmd.AddCommand(newConfigCheckSourceCmd())
	cmd.AddCommand(newConfigInitCmd())
	cmd.AddCommand(newConfigSchemaCmd())
	cmd.AddCommand(newConfigInferCmd())

	return cmd
}

func newConfigValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration file structure",
		Long: `Validate the structure and syntax of a reconify configuration file.
This checks that all required fields are present and have valid values.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args // avoid unused variable warning
			cfgPath := getConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return configErrf("failed to load config: %v", err)
			}

			if errs := cfg.Validate(); len(errs) > 0 {
				// Under --agent or --error-format json the diagnostic envelope
				// (details.errors) is the only stderr output.
				if errorFormat != "json" {
					cmd.PrintErrf("❌ %s is invalid:\n", cfgPath)
					for _, e := range errs {
						cmd.PrintErrf("  - %v\n", e)
					}
				}
				return validationErr("validation failed", errs)
			}

			cmd.PrintErrf("✅ %s is valid\n", cfgPath)
			return nil
		},
	}
}

func newConfigCheckSourceCmd() *cobra.Command {
	var sourceName string
	var filePath string
	var rows int

	cmd := &cobra.Command{
		Use:   "check-source",
		Short: "Check if an input file matches a source configuration",
		Long: `Check if an input file's structure matches the expected configuration for a source.
This validates that required columns exist and that sample data can be parsed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args // avoid unused variable warning
			if rows < 0 {
				return configErr("--rows must be non-negative")
			}
			if sourceName == "" {
				return configErr("--source is required")
			}
			if filePath == "" {
				return configErr("--file is required")
			}

			cfgPath := getConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return configErrf("failed to load config: %v", err)
			}

			cmd.PrintErrf("Checking source %q against file %q...\n", sourceName, filePath)

			// Verify that the source in the command exist in the config file
			source, ok := cfg.Sources[sourceName]
			if !ok {
				return configErrf("source %q not found in config", sourceName)
			}

			return runSourceCheck(cmd.Context(), sourceName, source, filePath, rows, cmd.PrintErrf)
		},
	}

	cmd.Flags().StringVar(&sourceName, "source", "", "Source name to check")
	cmd.Flags().StringVar(&filePath, "file", "", "Input file path to validate")
	cmd.Flags().IntVar(&rows, "rows", 10, "Number of data rows to parse after checking headers (0 checks headers only)")

	return cmd
}

// runSourceCheck verifies that filePath carries the columns and sample rows the
// source mapping expects. Findings are reported line by line through logf
// ("[ok]" / "[x]" prefixes); the returned error is a typed diagnostic.
func runSourceCheck(ctx context.Context, name string, source config.Source, filePath string, rows int, logf func(format string, args ...any)) error {
	headers, err := engine.ReadInputHeaders(ctx, filePath, source.Parser)
	if err != nil {
		return inputErr(ErrCodeConfig, "config_error", fmt.Sprintf("failed to read file: %v", err), diagnosticCodeInputUnreadable)
	}

	headerSet := make(map[string]bool)
	for _, header := range headers {
		headerSet[strings.ToLower(strings.TrimSpace(header))] = true
	}

	valid := true

	if !hasHeader(headerSet, source.Parser.DateCol) {
		logf("[x] date_col %q not found in input fields\n", source.Parser.DateCol)
		valid = false
	} else {
		logf("[ok] date_col %q found\n", source.Parser.DateCol)
	}

	if !hasHeader(headerSet, source.Parser.AmountCol) {
		logf("[x] amount_col %q not found in input fields\n", source.Parser.AmountCol)
		valid = false
	} else {
		logf("[ok] amount_col %q found\n", source.Parser.AmountCol)
	}

	if source.Parser.CurrencyCol != "" && !hasHeader(headerSet, source.Parser.CurrencyCol) {
		logf("[x] currency_col %q not found in input fields\n", source.Parser.CurrencyCol)
		valid = false
	} else {
		logf("[ok] currency_col %q found\n", source.Parser.CurrencyCol)
	}

	if source.Parser.NameCol != "" && !hasHeader(headerSet, source.Parser.NameCol) {
		logf("[x] name_col %q not found in input fields\n", source.Parser.NameCol)
		valid = false
	} else {
		logf("[ok] name_col %q found\n", source.Parser.NameCol)
	}

	if source.Parser.RefCol != "" && !hasHeader(headerSet, source.Parser.RefCol) {
		logf("[x] ref_col %q not found in input fields\n", source.Parser.RefCol)
		valid = false
	} else {
		logf("[ok] ref_col %q found\n", source.Parser.RefCol)
	}
	if source.Parser.Financials != nil {
		financialColumns := make(map[string]string, len(source.Parser.Financials.Fields)+2)
		for name, col := range source.Parser.Financials.Fields {
			financialColumns[name] = col
		}
		if source.Parser.Financials.GrossCol != "" {
			financialColumns["gross"] = source.Parser.Financials.GrossCol
		}
		if source.Parser.Financials.NetCol != "" {
			financialColumns["net"] = source.Parser.Financials.NetCol
		}
		for name, col := range financialColumns {
			if !hasHeader(headerSet, col) {
				logf("[x] financial field %q column %q not found in input fields\n", name, col)
				valid = false
			} else {
				logf("[ok] financial field %q column %q found\n", name, col)
			}
		}
	}

	if !valid {
		logf("Available columns: %s\n", strings.Join(headers, ", "))
		return inputErr(ErrCodeConfig, "config_error", fmt.Sprintf("source %q does not match file %q", name, filePath), diagnosticCodeInputMismatch)
	}

	if rows > 0 {
		result, err := sample.Validate(ctx, filePath, source.Parser, rows)
		if err != nil {
			return inputErr(ErrCodeConfig, "config_error", fmt.Sprintf("failed to parse sample rows: %v", err), diagnosticCodeInputMismatch)
		}
		for _, rowErr := range result.Errors {
			logf("[x] row %d: %s\n", rowErr.Row, rowErr.Message)
		}
		if len(result.Errors) > 0 {
			logf("[x] headers valid; %d of %d sampled rows failed to parse\n", len(result.Errors), result.RowsScanned)
			return inputErr(ErrCodeConfig, "config_error", fmt.Sprintf("source %q has invalid sample rows", name), diagnosticCodeInputMismatch)
		}
		logf("[ok] %d sampled rows parsed\n", result.SuccessfulRows)
	}

	logf("[OK] source %q matches file %q\n", name, filePath)
	return nil
}

func hasHeader(headers map[string]bool, name string) bool {
	return headers[strings.ToLower(strings.TrimSpace(name))]
}

// schemaOutput is the machine-readable description emitted by `reconify config schema`.
// It is hand-authored to stay stable and readable — not generated via reflection.
type schemaOutput struct {
	Version            string                              `json:"version"`
	ConfigSchema       map[string]interface{}              `json:"config_schema"`
	OutputFormats      map[string]schemas.FormatCapability `json:"output_formats"`
	NDJSONEvents       map[string]string                   `json:"ndjson_event_types"`
	ExitCodes          map[string]string                   `json:"exit_codes"`
	ResultSchemaID     string                              `json:"result_schema_id"`
	ResultSchema       json.RawMessage                     `json:"result_schema"`
	DiagnosticSchemaID string                              `json:"diagnostic_schema_id"`
	DiagnosticSchema   json.RawMessage                     `json:"diagnostic_schema"`
}

func newConfigSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print machine-readable schema for reconify.yaml and output formats",
		Long: `Print a JSON document describing the valid fields for reconify.yaml,
supported output formats, NDJSON event types, and exit codes.
Agents can call this once to self-bootstrap context without reading the source code.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			out := schemaOutput{
				Version:            cliVersion,
				ResultSchemaID:     schemas.ResultSchemaV1,
				ResultSchema:       schemas.ResultV1(),
				DiagnosticSchemaID: schemas.DiagnosticSchemaV1,
				DiagnosticSchema:   schemas.DiagnosticV1(),
				ConfigSchema: map[string]interface{}{
					"version": map[string]interface{}{
						"type": "integer", "required": true, "value": 1,
						"description": "Schema version. Must be 1.",
					},
					"timezone": map[string]interface{}{
						"type": "string", "required": false,
						"description": "IANA timezone (e.g. UTC, America/New_York). Overrides parser.tz when set.",
					},
					"index": map[string]interface{}{
						"type": "object", "required": false,
						"fields": map[string]interface{}{
							"backend": map[string]interface{}{
								"type": "string", "enum": []string{"memory", "disk", "auto", "partitioned"}, "default": "memory",
								"description": "memory (fastest), disk (lower RAM via SQLite), auto (resource-aware selection), partitioned (bounded-memory CSV reconciliation for one-to-one and eligible multi-counterpart runs).",
							},
							"spill_dir": map[string]interface{}{
								"type": "string", "required": false,
								"description": "Directory for disk index temp files. Uses OS temp dir when empty.",
							},
							"auto_max_right_file_mb": map[string]interface{}{
								"type": "integer", "default": 2048,
								"description": "Right-file size threshold in MB at which the auto backend switches to disk.",
							},
							"max_memory_mb": map[string]interface{}{
								"type": "integer", "required": false,
								"description": "Maximum estimated memory budget for index selection; zero leaves it uncapped.",
							},
							"max_temp_disk_mb": map[string]interface{}{
								"type": "integer", "required": false,
								"description": "Maximum estimated temporary-disk budget for disk or partitioned indexing; zero leaves it uncapped.",
							},
						},
					},
					"sources": map[string]interface{}{
						"type": "map[name]source", "required": true,
						"description": "Map of source names to source configs.",
						"value_schema": map[string]interface{}{
							"file_pattern": map[string]interface{}{
								"type": "string", "required": true,
								"description": "Glob pattern for input files, relative to the config file directory.",
							},
							"parser": map[string]interface{}{
								"type": "object", "required": true,
								"fields": map[string]interface{}{
									"type":         map[string]interface{}{"type": "string", "enum": []string{"csv", "json", "xlsx", "auto"}, "default": "auto", "description": "Input format. auto detects from file extension."},
									"date_col":     map[string]interface{}{"type": "string", "required": true, "description": "Column name for transaction date."},
									"date_layout":  map[string]interface{}{"type": "string", "required": true, "description": "Go time layout (e.g. 2006-01-02). NOT strftime format."},
									"amount_col":   map[string]interface{}{"type": "string", "required": true, "description": "Column name for transaction amount."},
									"multiplier":   map[string]interface{}{"type": "integer", "required": true, "description": "Multiply parsed amount to convert to minor units (e.g. 100 for dollar→cents)."},
									"decimal":      map[string]interface{}{"type": "string", "default": ".", "description": "Decimal separator character."},
									"thousands":    map[string]interface{}{"type": "string", "default": "", "description": "Thousands separator to strip before parsing."},
									"currency_col": map[string]interface{}{"type": "string", "required": false, "description": "Column name for currency code."},
									"name_col":     map[string]interface{}{"type": "string", "required": false, "description": "Column name for counterpart name. Required when pair name_mode is tokens."},
									"ref_col":      map[string]interface{}{"type": "string", "required": false, "description": "Column name for transaction reference/ID used in matching."},
									"group_col":    map[string]interface{}{"type": "string", "required": false, "description": "Column for duplicate grouping key. Falls back to ref_col when absent."},
									"tz":           map[string]interface{}{"type": "string", "required": false, "description": "IANA timezone for dates without timezone info. Overridden by top-level timezone."},
									"sheet":        map[string]interface{}{"type": "string", "required": false, "description": "Sheet name for xlsx files. Uses first sheet when empty."},
									"skip_raw":     map[string]interface{}{"type": "boolean", "default": false, "description": "Skip allocating the Raw field map. Reduces memory for large files."},
									"financials": map[string]interface{}{"type": "object", "required": false, "description": "Optional normalized monetary fields and source-local fee expectations.", "fields": map[string]interface{}{
										"gross_col": map[string]interface{}{"type": "string", "required": false}, "net_col": map[string]interface{}{"type": "string", "required": false},
										"fields":       map[string]interface{}{"type": "map[name]string", "required": false},
										"expectations": map[string]interface{}{"type": "map[name]expectation", "required": false, "description": "Exactly one of field, fixed, percentage, fixed_plus_percentage, or components per expectation."},
									}},
								},
							},
						},
					},
					"pairs": map[string]interface{}{
						"type": "map[name]pair", "required": true,
						"description": "Map of pair names to reconciliation pair configs.",
						"value_schema": map[string]interface{}{
							"left":                   map[string]interface{}{"type": "string", "required": true, "description": "Name of the left source."},
							"right":                  map[string]interface{}{"type": "string", "required": false, "description": "Name of a single right source. Use rights for multiple counterparts."},
							"rights":                 map[string]interface{}{"type": "array[string]", "required": false, "description": "Names of multiple right sources for 1-N reconciliation. Mutually exclusive with right."},
							"date_window":            map[string]interface{}{"type": "string", "default": "0d", "description": "Allowed date difference between matched transactions (e.g. 1d, 2d). 0d requires exact date match."},
							"amount_tolerance_minor": map[string]interface{}{"type": "integer", "default": 0, "description": "Allowed amount difference in minor units before reporting amount_diff."},
							"name_mode":              map[string]interface{}{"type": "string", "enum": []string{"none", "tokens"}, "required": false, "description": "none = match by reference only; tokens = also match by tokenized counterpart name. Optional when passes is explicitly set (name_mode=tokens is rejected when passes is set)."},
							"name_match_threshold":   map[string]interface{}{"type": "float", "default": 0.5, "description": "Minimum token match ratio (0–1) when name_mode is tokens."},
							"passes": map[string]interface{}{
								"type": "array[pass]", "required": false,
								"description": "Explicit reconciliation pass pipeline. Inferred from name_mode when absent.",
								"item_schema": map[string]interface{}{
									"type": map[string]interface{}{"type": "string", "enum": []string{"reference_one_to_one", "name_tokens_one_to_one", "one_to_many", "many_to_many", "subset_sum"}, "required": true},
								},
							},
						},
					},
				},
				OutputFormats: formatMetadataForConfigSchema(),
				// Event names match engine/format.go ndjsonWriter exactly.
				// Each line is {"type":"<name>","data":{...}}.
				NDJSONEvents: capabilityNDJSONEvents(),
				ExitCodes:    capabilityExitCodes(),
			}

			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}
}

// resolvePairName returns the pair to run. An explicit name is returned as is
// (callers report unknown names themselves). When name is empty, a config with
// exactly one pair defaults to it; otherwise the CONFIG_INVALID diagnostic lists
// the available pair names in details.pairs.
func resolvePairName(cfg *config.Config, name string) (string, error) {
	if name != "" {
		return name, nil
	}
	pairs := make([]string, 0, len(cfg.Pairs))
	for pairName := range cfg.Pairs {
		pairs = append(pairs, pairName)
	}
	sort.Strings(pairs)
	if len(pairs) == 1 {
		return pairs[0], nil
	}
	msg := "--pair is required: config defines no pairs"
	if len(pairs) > 1 {
		msg = fmt.Sprintf("--pair is required: config defines %d pairs (%s)", len(pairs), strings.Join(pairs, ", "))
	}
	return "", configErrDetails(msg, map[string]any{"pairs": pairs})
}
