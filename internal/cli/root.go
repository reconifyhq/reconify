package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

var (
	configFile     string
	configExplicit bool
	verbose        bool
	agentMode      bool
	errorFormat    string // "text" or "json"
	cliVersion     string // set by Execute; used by subcommands for audit envelopes
)

// ErrorFormat returns the current --error-format value. Read by main after Execute returns.
func ErrorFormat() string { return errorFormat }

// Execute runs the CLI application
func Execute(version, buildTime string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rawAgentMode, rawErrorFormat, rawErrorFormatValue := detectAgentArgs(os.Args[1:])
	rootCmd := newRootCmd(version, buildTime)
	// Persistent flags are not bound when Cobra rejects an unknown command or
	// malformed invocation. Seed the profile from raw argv so those errors keep
	// the same machine-readable contract as command execution errors.
	if rawAgentMode {
		agentMode = true
		if rawErrorFormat {
			errorFormat = rawErrorFormatValue
		} else {
			errorFormat = "json"
		}
	} else if rawErrorFormat {
		errorFormat = rawErrorFormatValue
	}
	err := rootCmd.ExecuteContext(ctx)
	// Cobra can return argument and unknown-command errors before running
	// PersistentPreRunE. Preserve the agent error contract for those failures.
	if rawAgentMode && !rawErrorFormat {
		errorFormat = "json"
	}
	return err
}

func detectAgentArgs(args []string) (agent, errorFormatExplicit bool, errorFormatValue string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		switch {
		case arg == "--agent":
			agent = true
		case arg == "--agent=false":
			agent = false
		case arg == "--agent=true":
			agent = true
		case arg == "--error-format":
			errorFormatExplicit = true
			if i+1 < len(args) {
				i++
				errorFormatValue = args[i]
			}
		case strings.HasPrefix(arg, "--error-format="):
			errorFormatExplicit = true
			errorFormatValue = strings.TrimPrefix(arg, "--error-format=")
		}
	}
	return agent, errorFormatExplicit, errorFormatValue
}

func newRootCmd(version, buildTime string) *cobra.Command {
	cliVersion = version
	rootCmd := &cobra.Command{
		Use:   "reconify",
		Short: "A developer-first reconciliation engine",
		Long: `Reconify is a reconciliation engine designed for finance, ops, and accounting teams.
It ingests financial data from multiple sources, normalizes them, and compares transactions.`,
		Version: fmt.Sprintf("%s (built %s)", version, buildTime),
		// Silence Cobra's built-in error and usage printing so main.go owns
		// the error output format (plain text or JSON via --error-format).
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// Global flags
	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "reconify.yaml", "Path to configuration file")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().StringVar(&errorFormat, "error-format", "text",
		`Error output format: text (default) or json. When json, errors are written to stderr as reconify.engine.diagnostic.v1.`)
	rootCmd.PersistentFlags().BoolVar(&agentMode, "agent", false,
		"Use machine-readable defaults for agent and scripted callers")
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		_ = args
		configExplicit = cmd.Root().PersistentFlags().Changed("config")
		if !agentMode {
			return nil
		}
		return applyAgentProfile(cmd)
	}

	// Add subcommands
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newReconcileCmd())
	rootCmd.AddCommand(newParseCmd())
	rootCmd.AddCommand(newSchemaCmd())
	rootCmd.AddCommand(newCapabilitiesCmd())
	rootCmd.AddCommand(newInspectCmd())
	rootCmd.AddCommand(newExplainCmd())
	rootCmd.AddCommand(newMCPCmd())

	return rootCmd
}

func applyAgentProfile(cmd *cobra.Command) error {
	if !cmd.Root().PersistentFlags().Changed("error-format") {
		if err := cmd.Root().PersistentFlags().Set("error-format", "json"); err != nil {
			return fmt.Errorf("apply agent error format: %w", err)
		}
	}

	if cmd.CommandPath() == "reconify config init" {
		return newCLIError(
			ErrCodeConfig,
			"config_error",
			"config init is interactive and cannot run with --agent; use config infer or write reconify.yaml manually",
			diagnosticCodeInteractiveUnsupported,
			diagnosticCategoryConfig,
			"Use `reconify config infer` or provide a reconify.yaml configuration for non-interactive execution.",
			nil,
		)
	}

	if cmd.Name() != "reconcile" {
		return nil
	}
	if !cmd.Flags().Changed("format") {
		if err := cmd.Flags().Set("format", "ndjson"); err != nil {
			return fmt.Errorf("apply agent result format: %w", err)
		}
	}
	return nil
}

// getConfigPath returns the config file path, checking environment variable if not set
func getConfigPath() string {
	if configExplicit {
		return configFile
	}
	if envConfig := os.Getenv("RECONIFY_CONFIG"); envConfig != "" {
		return envConfig
	}
	return configFile
}
