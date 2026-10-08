package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readTrace(t *testing.T, path string) []TraceEntry {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- t.TempDir test file.
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	var entries []TraceEntry
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var entry TraceEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("trace line %q is not JSON: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestAppendTraceWritesOneJSONLinePerCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	AppendTrace(path, TraceEntry{Argv: []string{"config", "validate"}, ExitCode: 2, DurationMS: 14, DiagnosticCode: "CONFIG_INVALID"})
	AppendTrace(path, TraceEntry{Argv: []string{"capabilities"}, ExitCode: 0, DurationMS: 3})

	data, err := os.ReadFile(path) // #nosec G304 -- t.TempDir test file.
	if err != nil {
		t.Fatal(err)
	}
	want := `{"argv":["config","validate"],"exit_code":2,"duration_ms":14,"diagnostic_code":"CONFIG_INVALID"}` + "\n" +
		`{"argv":["capabilities"],"exit_code":0,"duration_ms":3}` + "\n"
	if string(data) != want {
		t.Fatalf("trace file =\n%s\nwant\n%s", data, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("trace file mode = %o, want 600", perm)
	}
}

func TestAppendTraceIgnoresFailures(t *testing.T) {
	// A directory path cannot be opened for append; AppendTrace must stay silent.
	AppendTrace(t.TempDir(), TraceEntry{Argv: []string{"x"}})
	AppendTrace("", TraceEntry{Argv: []string{"x"}})
	AppendTrace(filepath.Join(t.TempDir(), "missing", "dir", "trace.jsonl"), TraceEntry{Argv: []string{"x"}})
}

func TestDiagnosticCodeOfErrors(t *testing.T) {
	if got := DiagnosticCode(nil); got != "" {
		t.Fatalf("DiagnosticCode(nil) = %q", got)
	}
	if got := DiagnosticCode(configErr("x")); got != diagnosticCodeConfigInvalid {
		t.Fatalf("DiagnosticCode(config) = %q", got)
	}
	if got := DiagnosticCode(errString("boom")); got != diagnosticCodeInternalError {
		t.Fatalf("DiagnosticCode(plain) = %q", got)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestTraceFileFromBinary(t *testing.T) {
	bin := runReconcileBinary(t)
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace.jsonl")
	run := func(args ...string) int {
		// #nosec G204 -- test invokes the locally built CLI binary.
		command := exec.Command(bin, args...)
		command.Dir = dir
		command.Env = append(os.Environ(), TraceFileEnv+"="+trace)
		err := command.Run()
		if err == nil {
			return 0
		}
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %v: %v", args, err)
		}
		return exitErr.ExitCode()
	}

	if code := run("capabilities"); code != 0 {
		t.Fatalf("capabilities exit = %d", code)
	}
	if code := run("--agent", "config", "validate", "--config", "missing.yaml"); code != 2 {
		t.Fatalf("config validate exit = %d", code)
	}
	if code := run("--agent", "reconcile", "--bogus"); code != 2 {
		t.Fatalf("usage error exit = %d", code)
	}

	entries := readTrace(t, trace)
	if len(entries) != 3 {
		t.Fatalf("trace has %d lines, want 3: %+v", len(entries), entries)
	}
	if got := strings.Join(entries[0].Argv, " "); got != "capabilities" || entries[0].ExitCode != 0 || entries[0].DiagnosticCode != "" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if got := strings.Join(entries[1].Argv, " "); got != "--agent config validate --config missing.yaml" ||
		entries[1].ExitCode != 2 || entries[1].DiagnosticCode != "CONFIG_INVALID" {
		t.Errorf("entry 1 = %+v", entries[1])
	}
	if entries[2].ExitCode != 2 || entries[2].DiagnosticCode != "USAGE_ERROR" {
		t.Errorf("entry 2 = %+v", entries[2])
	}
	for _, entry := range entries {
		if entry.DurationMS < 0 {
			t.Errorf("negative duration in %+v", entry)
		}
	}
}

func TestTraceDoesNotChangeOutputOrExitCode(t *testing.T) {
	bin := runReconcileBinary(t)
	dir := t.TempDir()
	// The trace path is unwritable (its parent does not exist).
	unwritable := filepath.Join(dir, "no", "such", "dir", "trace.jsonl")
	run := func(trace string) (string, int) {
		// #nosec G204 -- test invokes the locally built CLI binary.
		command := exec.Command(bin, "--agent", "reconcile", "--bogus")
		command.Dir = dir
		command.Env = append(os.Environ(), TraceFileEnv+"="+trace)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		err := command.Run()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		return stderr.String(), code
	}
	withTrace, codeWith := run(unwritable)
	without, codeWithout := run("")
	if !strings.Contains(without, `"code":"USAGE_ERROR"`) {
		t.Fatalf("--agent usage error is not a JSON diagnostic envelope: %q", without)
	}
	if withTrace != without || codeWith != codeWithout || codeWith != 2 {
		t.Fatalf("trace changed behavior:\n%q (%d)\n%q (%d)", withTrace, codeWith, without, codeWithout)
	}
}
