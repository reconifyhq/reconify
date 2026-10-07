package cli

import (
	"io"

	"github.com/reconifyhq/reconify/internal/mcp"
	"github.com/spf13/cobra"
)

func newMCPCmd() *cobra.Command {
	var workDir string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve MCP over stdio",
		Long: `Run the Reconify Engine MCP server (identity "reconify-engine") over stdio.

The server speaks newline-delimited JSON-RPC 2.0 on stdin/stdout and exposes the
Engine workflow as MCP tools: capabilities, inspect_file, infer_config,
validate_config, check_source, reconcile, reconcile_auto, get_summary,
list_exceptions, explain_result, and verify_workspace. It works on local files
only and needs no Reconify account.

Tools run this same binary with --agent, so their output matches the CLI
contract. stdout carries protocol messages only; logs (with --verbose) go to
stderr. Register it in an MCP client with command "reconify" and args ["mcp"].`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			logs := io.Discard
			if verbose {
				logs = cmd.ErrOrStderr()
			}
			server, err := mcp.NewServer(mcp.Options{
				Version: cliVersion,
				WorkDir: workDir,
				Stderr:  logs,
			})
			if err != nil {
				return configErrf("start MCP server: %v", err)
			}
			if err := server.Serve(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout()); err != nil {
				return executionErr(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workDir, "workdir", "", "Working directory for tool calls and relative paths (defaults to the current directory)")
	return cmd
}
