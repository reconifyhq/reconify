package evals

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
)

// maxTraceLineBytes bounds one trace line so a hostile workspace cannot exhaust memory.
const maxTraceLineBytes = 1 << 20

// readTrace parses the Engine trace file. A missing file yields no entries and
// malformed lines are skipped, so partial traces from crashed processes survive.
func readTrace(path string) []TraceEntry {
	data, err := os.ReadFile(path) // #nosec G304 -- evaluator workspace.
	if err != nil {
		return nil
	}
	return parseTrace(data)
}

func parseTrace(data []byte) []TraceEntry {
	var entries []TraceEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxTraceLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry TraceEntry
		if json.Unmarshal(line, &entry) != nil || len(entry.Argv) == 0 {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// globalValueFlags are root flags that consume the following argv element.
var globalValueFlags = map[string]bool{"--config": true, "-c": true, "--error-format": true}

// positionals returns the non-flag argv words, skipping values of root flags,
// so `--agent config validate -c x.yaml` reads as [config validate x.yaml].
func positionals(argv []string) []string {
	var words []string
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case globalValueFlags[arg]:
			i++
		case strings.HasPrefix(arg, "-"):
		default:
			words = append(words, arg)
		}
	}
	return words
}

// subcommand names the invoked command: the first word, plus the second for
// command groups such as `config validate`.
func subcommand(argv []string) string {
	words := positionals(argv)
	if len(words) == 0 {
		return ""
	}
	if (words[0] == "config" || words[0] == "schema") && len(words) > 1 {
		return words[0] + " " + words[1]
	}
	return words[0]
}

// isFailure reports whether a traced call failed. Reconcile exits 3 and 4 mean
// the run completed and reported exceptions, which is not a failure.
func isFailure(entry TraceEntry) bool {
	if entry.ExitCode == 0 {
		return false
	}
	if subcommand(entry.Argv) == "reconcile" && (entry.ExitCode == 3 || entry.ExitCode == 4) {
		return false
	}
	return true
}

// traceIndex returns the index of the first entry whose subcommand is name, or -1.
func traceIndex(trace []TraceEntry, name string) int {
	for i, entry := range trace {
		if subcommand(entry.Argv) == name {
			return i
		}
	}
	return -1
}

func computeEfficiency(trace []TraceEntry) *Efficiency {
	if len(trace) == 0 {
		return nil
	}
	result := &Efficiency{EngineCalls: len(trace)}
	for i, entry := range trace {
		if entry.DiagnosticCode == "USAGE_ERROR" {
			result.UsageErrors++
		}
		if subcommand(entry.Argv) == "config validate" && entry.ExitCode == 0 && result.CallsToFirstValidation == nil {
			result.CallsToFirstValidation = intPtr(i + 1)
		}
		if !isFailure(entry) {
			continue
		}
		result.FailedCalls++
		for _, later := range trace[i+1:] {
			if subcommand(later.Argv) == subcommand(entry.Argv) && !isFailure(later) {
				result.Recovered = true
				break
			}
		}
	}
	return result
}
