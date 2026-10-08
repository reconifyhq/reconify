package evals

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func call(exit int, diagnostic string, argv ...string) TraceEntry {
	return TraceEntry{Argv: argv, ExitCode: exit, DiagnosticCode: diagnostic}
}

func TestParseTraceSkipsMalformedLines(t *testing.T) {
	data := []byte(`{"argv":["capabilities"],"exit_code":0,"duration_ms":3}
not json

{"argv":[],"exit_code":0}
{"argv":["config","validate","--config","reconify.yaml"],"exit_code":2,"duration_ms":14,"diagnostic_code":"CONFIG_INVALID"}
{"argv":["reconcile"
`)
	got := parseTrace(data)
	if len(got) != 2 {
		t.Fatalf("entries = %+v", got)
	}
	if got[1].ExitCode != 2 || got[1].DiagnosticCode != "CONFIG_INVALID" || got[1].DurationMS != 14 {
		t.Fatalf("entry = %+v", got[1])
	}
}

func TestReadTraceToleratesMissingFile(t *testing.T) {
	if got := readTrace(filepath.Join(t.TempDir(), "absent.jsonl")); got != nil {
		t.Fatalf("trace = %+v", got)
	}
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	if err := os.WriteFile(path, []byte(`{"argv":["explain","result.json"],"exit_code":0}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readTrace(path); len(got) != 1 || got[0].Argv[0] != "explain" {
		t.Fatalf("trace = %+v", got)
	}
}

func TestSubcommandIgnoresGlobalFlags(t *testing.T) {
	for _, testCase := range []struct {
		argv []string
		want string
	}{
		{[]string{"config", "validate", "--config", "reconify.yaml"}, "config validate"},
		{[]string{"--agent", "config", "validate"}, "config validate"},
		{[]string{"--error-format", "json", "reconcile", "--pair", "p"}, "reconcile"},
		{[]string{"-c", "x.yaml", "--agent", "capabilities"}, "capabilities"},
		{[]string{"--error-format=json", "explain", "result.json"}, "explain"},
		{[]string{"--help"}, ""},
	} {
		if got := subcommand(testCase.argv); got != testCase.want {
			t.Errorf("subcommand(%v) = %q, want %q", testCase.argv, got, testCase.want)
		}
	}
}

func TestComputeEfficiency(t *testing.T) {
	two := 2
	for _, testCase := range []struct {
		name  string
		trace []TraceEntry
		want  *Efficiency
	}{
		{name: "no trace", want: nil},
		{
			name: "clean run",
			trace: []TraceEntry{
				call(0, "", "capabilities"),
				call(0, "", "config", "validate"),
				call(0, "", "reconcile", "--pair", "p"),
			},
			want: &Efficiency{EngineCalls: 3, CallsToFirstValidation: &two},
		},
		{
			name: "exceptions exit codes are not failures",
			trace: []TraceEntry{
				call(0, "", "config", "validate"),
				call(3, "UNMATCHED_ROWS", "reconcile"),
				call(4, "EXCEPTIONS_FOUND", "reconcile"),
			},
			want: &Efficiency{EngineCalls: 3, CallsToFirstValidation: intPtr(1)},
		},
		{
			name: "exit 3 from another command is a failure",
			trace: []TraceEntry{
				call(3, "", "config", "validate"),
			},
			want: &Efficiency{EngineCalls: 1, FailedCalls: 1},
		},
		{
			name: "usage error then recovery",
			trace: []TraceEntry{
				call(2, "USAGE_ERROR", "config", "valdiate"),
				call(2, "USAGE_ERROR", "config", "validate", "--bogus"),
				call(2, "CONFIG_INVALID", "config", "validate"),
				call(0, "", "config", "validate"),
			},
			want: &Efficiency{EngineCalls: 4, FailedCalls: 3, UsageErrors: 2, CallsToFirstValidation: intPtr(4), Recovered: true},
		},
		{
			name: "failure never recovered by a different command",
			trace: []TraceEntry{
				call(2, "CONFIG_INVALID", "config", "validate"),
				call(0, "", "capabilities"),
			},
			want: &Efficiency{EngineCalls: 2, FailedCalls: 1},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := computeEfficiency(testCase.trace); !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("efficiency = %+v, want %+v", got, testCase.want)
			}
		})
	}
}
