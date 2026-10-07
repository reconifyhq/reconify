package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/reconifyhq/reconify/config"
	"github.com/reconifyhq/reconify/engine"
	"github.com/spf13/cobra"
)

func newParseCmd() *cobra.Command {
	var sourceName string
	var filePath string
	var format string

	cmd := &cobra.Command{
		Use:   "parse",
		Short: "Parse an input file according to source configuration",
		Long: `Parse a CSV, JSON, or XLSX input file using a configured source parser.
Streams parsed transactions to stdout in the requested format.

Formats:
  ndjson  (default) One JSON transaction per line; streaming, O(1) memory
  csv               CSV rows; streaming, O(1) memory
  table             Aligned ASCII table; buffers all rows in memory
  json              JSON array; loads all transactions before writing`,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			if sourceName == "" {
				return usageErr(cmd, "--source is required", "")
			}
			if filePath == "" {
				return usageErr(cmd, "--file is required", "")
			}

			cfgPath := getConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return configErrf("failed to load config: %v", err)
			}

			if errs := cfg.Validate(); len(errs) > 0 {
				return validationErr(fmt.Sprintf("config validation failed: %v", errs[0]), errs)
			}

			source, ok := cfg.Sources[sourceName]
			if !ok {
				return configErrf("source %q not found in config (available: %v)", sourceName, sourceNames(cfg.Sources))
			}

			if _, err := os.Stat(filePath); err != nil {
				return inputErr(ErrCodeConfig, "config_error", fmt.Sprintf("file %q not found", filePath), diagnosticCodeInputUnreadable)
			}
			resolvedPath := filePath

			switch format {
			case "json":
				return parseJSON(sourceName, resolvedPath, source.Parser, cmd)
			case "ndjson":
				return parseNDJSON(sourceName, resolvedPath, source.Parser, cmd)
			case "csv":
				return parseCSVOut(sourceName, resolvedPath, source.Parser, cmd)
			case "table":
				return parseTable(sourceName, resolvedPath, source.Parser, cmd)
			default:
				return usageErr(cmd, fmt.Sprintf("unknown format %q (valid: ndjson, csv, table, json)", format), "")
			}
		},
	}

	cmd.Flags().StringVar(&sourceName, "source", "", "Source name to use for parsing (required)")
	cmd.Flags().StringVar(&filePath, "file", "", "Input file path to parse (required)")
	cmd.Flags().StringVar(&format, "format", "ndjson", `Output format: ndjson (default), csv, table, json`)

	return cmd
}

// parseNDJSON streams transactions as NDJSON (one JSON object per line).
func parseNDJSON(sourceName, filePath string, parserCfg config.ParserCfg, cmd *cobra.Command) error {
	enc := json.NewEncoder(os.Stdout)
	count := 0
	err := engine.ParseEach(cmd.Context(), sourceName, filePath, parserCfg, func(tx engine.Transaction, _ int) error {
		count++
		return enc.Encode(tx)
	})
	if err != nil {
		return fmt.Errorf("parse failed: %w", err)
	}
	cmd.PrintErrf("Parsed %d transactions from %q\n", count, filePath)
	return nil
}

// parseJSON loads all transactions and writes a JSON array.
func parseJSON(sourceName, filePath string, parserCfg config.ParserCfg, cmd *cobra.Command) error {
	var txns []engine.Transaction
	err := engine.ParseEach(cmd.Context(), sourceName, filePath, parserCfg, func(tx engine.Transaction, _ int) error {
		txns = append(txns, tx)
		return nil
	})
	if err != nil {
		return fmt.Errorf("parse failed: %w", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(txns); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	cmd.PrintErrf("Parsed %d transactions from %q\n", len(txns), filePath)
	return nil
}

var txCSVHeader = []string{"id", "date", "amount_minor", "currency", "reference", "name", "source", "group_key"}

// parseCSVOut streams transactions as CSV rows.
func parseCSVOut(sourceName, filePath string, parserCfg config.ParserCfg, cmd *cobra.Command) error {
	w := csv.NewWriter(os.Stdout)
	if err := w.Write(txCSVHeader); err != nil {
		return err
	}
	count := 0
	err := engine.ParseEach(cmd.Context(), sourceName, filePath, parserCfg, func(tx engine.Transaction, _ int) error {
		count++
		return w.Write([]string{
			tx.ID,
			tx.Date.Format(time.RFC3339),
			strconv.FormatInt(tx.Amount, 10),
			engine.SanitizeCSVField(tx.Currency),
			engine.SanitizeCSVField(tx.Reference),
			engine.SanitizeCSVField(tx.Name),
			tx.Source,
			engine.SanitizeCSVField(tx.GroupKey),
		})
	})
	if err != nil {
		return fmt.Errorf("parse failed: %w", err)
	}
	w.Flush()
	if flushErr := w.Error(); flushErr != nil {
		return flushErr
	}
	cmd.PrintErrf("Parsed %d transactions from %q\n", count, filePath)
	return nil
}

// parseTable buffers all transactions and renders an ASCII table.
func parseTable(sourceName, filePath string, parserCfg config.ParserCfg, cmd *cobra.Command) error {
	var txns []engine.Transaction
	err := engine.ParseEach(cmd.Context(), sourceName, filePath, parserCfg, func(tx engine.Transaction, _ int) error {
		txns = append(txns, tx)
		return nil
	})
	if err != nil {
		return fmt.Errorf("parse failed: %w", err)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tDATE\tAMOUNT\tCURRENCY\tREFERENCE\tNAME\tSOURCE\tGROUP_KEY"); err != nil {
		return err
	}
	for _, tx := range txns {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			tx.ID, tx.Date.Format("2006-01-02"), tx.Amount,
			tx.Currency, tx.Reference, tx.Name, tx.Source, tx.GroupKey); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	cmd.PrintErrf("Parsed %d transactions from %q\n", len(txns), filePath)
	return nil
}

func sourceNames(sources map[string]config.Source) []string {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	return names
}
