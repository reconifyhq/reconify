package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// maxSuggestionDistance is the largest edit distance at which a command or flag
// is offered as a did_you_mean suggestion.
const maxSuggestionDistance = 2

// installUsageErrors routes every Cobra-originated usage failure (unknown
// command or flag, wrong argument count, unparsable flag value) through
// USAGE_ERROR diagnostics instead of letting them surface as INTERNAL_ERROR.
func installUsageErrors(root *cobra.Command) {
	root.SetFlagErrorFunc(flagUsageError)
	walkCommands(root, func(cmd *cobra.Command) {
		switch {
		case cmd.HasSubCommands():
			// Group commands (and the root) print help when invoked bare and reject
			// anything that is not a known subcommand.
			cmd.Args = unknownSubcommandArgs
			if cmd.RunE == nil && cmd.Run == nil {
				cmd.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
			}
		case cmd.Args == nil:
			cmd.Args = wrapArgsValidator(cobra.NoArgs)
		default:
			cmd.Args = wrapArgsValidator(cmd.Args)
		}
	})
}

func walkCommands(cmd *cobra.Command, visit func(*cobra.Command)) {
	visit(cmd)
	for _, child := range cmd.Commands() {
		walkCommands(child, visit)
	}
}

// wrapArgsValidator converts a Cobra positional-argument validator's error into
// a USAGE_ERROR. Validators that already return *Error pass through.
func wrapArgsValidator(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		err := validate(cmd, args)
		if err == nil {
			return nil
		}
		var typed *Error
		if errors.As(err, &typed) {
			return err
		}
		if len(args) > 0 && strings.HasPrefix(err.Error(), "unknown command ") {
			return usageErr(cmd, fmt.Sprintf("%s takes no positional arguments (got %q)", cmd.CommandPath(), args[0]), "")
		}
		return usageErr(cmd, fmt.Sprintf("%s: %v", cmd.CommandPath(), err), "")
	}
}

func unknownSubcommandArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return unknownCommandErr(cmd, args[0])
}

func unknownCommandErr(cmd *cobra.Command, name string) *Error {
	msg := fmt.Sprintf("unknown command %q for %q", name, cmd.CommandPath())
	suggestion := ""
	var names []string
	for _, child := range cmd.Commands() {
		if child.IsAvailableCommand() {
			names = append(names, child.Name())
		}
	}
	if match := closestMatch(name, names); match != "" {
		suggestion = cmd.CommandPath() + " " + match
	}
	return usageErr(cmd, msg, suggestion)
}

// flagUsageError converts a Cobra flag-parsing error into USAGE_ERROR.
func flagUsageError(cmd *cobra.Command, err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	if errors.Is(err, pflag.ErrHelp) {
		return err
	}
	msg := err.Error()
	suggestion := ""
	if name, ok := strings.CutPrefix(msg, "unknown flag: --"); ok {
		var names []string
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if !f.Hidden {
				names = append(names, f.Name)
			}
		})
		if match := closestMatch(name, names); match != "" {
			suggestion = "--" + match
		}
	}
	return usageErr(cmd, msg, suggestion)
}

// closestMatch returns the candidate nearest to typed within
// maxSuggestionDistance (or sharing a prefix with it), or "" when none is close.
// Ties resolve alphabetically so the result is deterministic.
func closestMatch(typed string, candidates []string) string {
	typed = strings.ToLower(typed)
	if typed == "" {
		return ""
	}
	sorted := append([]string(nil), candidates...)
	sort.Strings(sorted)
	best, bestDistance := "", maxSuggestionDistance+1
	for _, candidate := range sorted {
		lower := strings.ToLower(candidate)
		distance := levenshtein(typed, lower)
		if strings.HasPrefix(lower, typed) && distance > maxSuggestionDistance {
			distance = maxSuggestionDistance
		}
		if distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
