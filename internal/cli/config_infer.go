package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reconifyhq/reconify/engine/inference"
	"github.com/spf13/cobra"
)

func newConfigInferCmd() *cobra.Command {
	var left, right, out string
	cmd := &cobra.Command{
		Use:   "infer [LEFT RIGHT | --left FILE --right FILE] [--out FILE]",
		Short: "Infer a deterministic reconify.yaml proposal from two input files",
		Long: `Infer date, amount, and reference mappings from two input files without prompts.
The command prints reconify.engine.config-proposal.v1 JSON. It returns needs_input
instead of guessing whenever confidence or sample-row gates are not satisfied.

The input files may be given positionally (infer LEFT RIGHT) or with --left and
--right. Mixing the two forms is a usage error.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}
			if len(args) != 2 {
				return usageErr(cmd, fmt.Sprintf("config infer takes exactly two positional files, LEFT and RIGHT (got %d)", len(args)), "")
			}
			if cmd.Flags().Changed("left") || cmd.Flags().Changed("right") {
				return usageErr(cmd, "config infer: pass the input files either positionally (LEFT RIGHT) or with --left/--right, not both", "")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 2 {
				left, right = args[0], args[1]
			}
			if left == "" {
				return configErr("--left is required")
			}
			if right == "" {
				return configErr("--right is required")
			}
			for _, filePath := range []string{left, right} {
				if _, err := os.Stat(filePath); err != nil {
					return inputErr(ErrCodeConfig, "config_error", fmt.Sprintf("file %q not found", filePath), diagnosticCodeInputUnreadable)
				}
			}
			proposal, err := inference.Infer(cmd.Context(), left, right)
			if err != nil {
				return inputErr(ErrCodeConfig, "config_error", fmt.Sprintf("infer config: %v", err), diagnosticCodeInputMismatch)
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(proposal); err != nil {
				return fmt.Errorf("write proposal: %w", err)
			}
			if out == "" {
				return nil
			}
			if proposal.Status != "ready" {
				return inferenceAmbiguousErr(proposal.Reasons)
			}
			if err := writeInferredConfig(out, []byte(proposal.ProposedYAML)); err != nil {
				return configErrf("write inferred config: %v", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&left, "left", "", "Left input file")
	cmd.Flags().StringVar(&right, "right", "", "Right input file")
	cmd.Flags().StringVar(&out, "out", "", "Write the ready proposed YAML config to this path")
	return cmd
}

func inferenceAmbiguousErr(reasons []string) *Error {
	return newCLIError(ErrCodeConfig, "config_error", "inference needs input", diagnosticCodeInferenceAmbiguous, diagnosticCategoryInference,
		"Review the proposed alternatives, select mappings explicitly, and rerun `reconify config validate`.", map[string]any{"reasons": reasons})
}

func writeInferredConfig(path string, data []byte) error {
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("output path %q is a directory", path)
		}
		return fmt.Errorf("output path %q already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check output path %q: %w", path, err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".reconify-infer-*")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary output: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set output permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary output: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("publish output: %w", err)
	}
	return nil
}
