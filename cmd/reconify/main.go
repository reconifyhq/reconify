// Package main handles the main entry point for the reconify CLI application
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/reconifyhq/reconify/internal/cli"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	os.Exit(run())
}

// run executes the CLI and returns the process exit code. When
// RECONIFY_TRACE_FILE is set it appends one trace line before returning.
func run() int {
	started := time.Now()
	err := cli.Execute(Version, BuildTime)
	exitCode := 0
	if err != nil {
		exitCode = cli.ExitCode(err)
		printError(err)
	}
	cli.TraceFromEnv(os.Args[1:], exitCode, started, err)
	return exitCode
}

func printError(err error) {
	if cli.ErrorFormat() == "json" {
		out, marshalErr := cli.MarshalDiagnosticEnvelope(err)
		if marshalErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", marshalErr)
			return
		}
		fmt.Fprintf(os.Stderr, "%s\n", out)
		return
	}
	fmt.Fprintln(os.Stderr, cli.TextError(err))
}
