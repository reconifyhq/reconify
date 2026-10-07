package cli

import (
	"encoding/json"
	"os"
	"time"
)

// TraceFileEnv names the environment variable that, when set, makes every
// reconify process append one JSON line describing its invocation to that file.
const TraceFileEnv = "RECONIFY_TRACE_FILE"

// TraceEntry is the line appended to the trace file when the process exits.
type TraceEntry struct {
	Argv           []string `json:"argv"`
	ExitCode       int      `json:"exit_code"`
	DurationMS     int64    `json:"duration_ms"`
	DiagnosticCode string   `json:"diagnostic_code,omitempty"`
}

// DiagnosticCode returns the diagnostic.code of err, or "" when err is nil.
func DiagnosticCode(err error) string {
	if err == nil {
		return ""
	}
	return DiagnosticEnvelope(err).Diagnostic.Code
}

// AppendTrace writes entry as one JSON line to path using a single append write.
// An empty path is a no-op. Failures are deliberately ignored: tracing must never
// change a command's output or exit code.
func AppendTrace(path string, entry TraceEntry) {
	if path == "" {
		return
	}
	if entry.Argv == nil {
		entry.Argv = []string{}
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	line = append(line, '\n')
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 G703 -- explicit opt-in path from the environment.
	if err != nil {
		return
	}
	_, _ = file.Write(line)
	_ = file.Close()
}

// TraceFromEnv appends the trace entry for a finished process when
// RECONIFY_TRACE_FILE is set.
func TraceFromEnv(argv []string, exitCode int, started time.Time, err error) {
	AppendTrace(os.Getenv(TraceFileEnv), TraceEntry{
		Argv:           argv,
		ExitCode:       exitCode,
		DurationMS:     time.Since(started).Milliseconds(),
		DiagnosticCode: DiagnosticCode(err),
	})
}
