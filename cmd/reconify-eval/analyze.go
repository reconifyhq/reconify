package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/reconifyhq/reconify/internal/evals"
)

// Exit codes shared by compare and summarize.
const (
	exitOK         = 0
	exitFailure    = 1
	exitUsage      = 2
	exitRegression = 3
)

// printLine writes a diagnostic line; a failed write has nowhere to be reported.
func printLine(w io.Writer, args ...any) { _, _ = fmt.Fprintln(w, args...) }

// parseModelArguments turns repeated agent=id values into the adapter map.
func parseModelArguments(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(values))
	for _, value := range values {
		agent, model, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(agent) == "" || strings.TrimSpace(model) == "" {
			return nil, fmt.Errorf("--model %q must be agent=id", value)
		}
		result[strings.TrimSpace(agent)] = strings.TrimSpace(model)
	}
	return result, nil
}

// parseInterspersed parses flags that may appear before, between, or after
// positional arguments, which the standard library does not support.
func parseInterspersed(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}
		args = flags.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// compareMain diffs two reports. It exits 0 normally, 3 when the head
// regressed (some agent and scenario pair's pass rate dropped by 0.34 or
// more), and 2 on usage or read errors.
func compareMain(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("compare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		printLine(stderr, "usage: reconify-eval compare BASE.json HEAD.json [--markdown] [--out FILE] [--variant NAME]")
		printLine(stderr, "exit status: 0 normally; 3 when HEAD regressed, meaning an agent and scenario pair got worse and its pass rate dropped by 0.34 or more; 2 on errors")
		flags.PrintDefaults()
	}
	markdown := flags.Bool("markdown", false, "render a GitHub-flavored markdown summary instead of JSON")
	out := flags.String("out", "", "write the diff to this path instead of stdout")
	variant := flags.String("variant", "", "release-report variant to compare in both reports (default: candidate)")
	positional, err := parseInterspersed(flags, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(positional) != 2 {
		flags.Usage()
		return exitUsage
	}
	base, err := evals.LoadReport(positional[0])
	if err != nil {
		printLine(stderr, "reconify-eval compare:", err)
		return exitUsage
	}
	head, err := evals.LoadReport(positional[1])
	if err != nil {
		printLine(stderr, "reconify-eval compare:", err)
		return exitUsage
	}
	diff, err := evals.Compare(base, head, *variant)
	if err != nil {
		printLine(stderr, "reconify-eval compare:", err)
		return exitUsage
	}
	if *markdown {
		err = evals.WriteText(evals.RenderDiffMarkdown(diff), *out, stdout)
	} else {
		err = evals.WriteJSON(diff, *out, stdout)
	}
	if err != nil {
		printLine(stderr, "reconify-eval compare:", err)
		return exitFailure
	}
	if diff.Regression {
		return exitRegression
	}
	return exitOK
}

// summarizeMain digests one report. It exits 0 unless the report cannot be read.
func summarizeMain(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("summarize", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		printLine(stderr, "usage: reconify-eval summarize REPORT.json [--markdown] [--out FILE] [--variant NAME]")
		flags.PrintDefaults()
	}
	markdown := flags.Bool("markdown", false, "render a GitHub-flavored markdown summary instead of JSON")
	out := flags.String("out", "", "write the summary to this path instead of stdout")
	variant := flags.String("variant", "", "release-report variant to summarize (default: candidate)")
	positional, err := parseInterspersed(flags, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(positional) != 1 {
		flags.Usage()
		return exitUsage
	}
	report, err := evals.LoadReport(positional[0])
	if err != nil {
		printLine(stderr, "reconify-eval summarize:", err)
		return exitUsage
	}
	summary, err := evals.Summarize(report, *variant)
	if err != nil {
		printLine(stderr, "reconify-eval summarize:", err)
		return exitUsage
	}
	if *markdown {
		err = evals.WriteText(evals.RenderSummaryMarkdown(summary), *out, stdout)
	} else {
		err = evals.WriteJSON(summary, *out, stdout)
	}
	if err != nil {
		printLine(stderr, "reconify-eval summarize:", err)
		return exitFailure
	}
	return exitOK
}
