package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxStdoutBytes = 32 << 20
	maxStderrBytes = 4 << 20
)

// engineRun is the captured outcome of one reconify invocation.
type engineRun struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

// cappedBuffer keeps at most max bytes and remembers whether it dropped any.
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := c.max - c.buf.Len(); remaining > 0 {
		c.buf.Write(p[:min(len(p), remaining)])
	}
	if c.buf.Len() >= c.max {
		c.truncated = true
	}
	return len(p), nil
}

// runEngine re-invokes the reconify binary with --agent and explicit args. No
// shell is involved and stdin is /dev/null so the child can never consume the
// MCP transport.
func (s *Server) runEngine(ctx context.Context, dir string, argv []string) (*engineRun, error) {
	full := append([]string{"--agent"}, argv...)
	cmd := exec.CommandContext(ctx, s.exe, full...) // #nosec G204 -- executable is this binary; args are built from schema-validated tool input and run without a shell.
	cmd.Dir = dir
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 3 * time.Second
	stdout := &cappedBuffer{max: maxStdoutBytes}
	stderr := &cappedBuffer{max: maxStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	s.logf("exec %s", strings.Join(full, " "))
	err := cmd.Run()
	run := &engineRun{stdout: stdout.buf.Bytes(), stderr: stderr.buf.Bytes()}
	if err == nil {
		if stdout.truncated {
			return nil, fmt.Errorf("reconify output exceeded %d bytes", maxStdoutBytes)
		}
		return run, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		run.exitCode = exitErr.ExitCode()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return run, nil
	}
	return nil, fmt.Errorf("run reconify: %w", err)
}

// invoke runs the engine and converts unacceptable outcomes into an error
// result. Exit codes listed in okCodes (0 is always accepted) are returned to
// the caller as a normal run.
func (s *Server) invoke(ctx context.Context, args map[string]any, argv []string, okCodes ...int) (*engineRun, *toolResult) {
	dir, errRes := s.workDirFor(args)
	if errRes != nil {
		return nil, errRes
	}
	run, err := s.runEngine(ctx, dir, argv)
	if err != nil {
		res := errorResult("EXECUTION_FAILED", err.Error())
		return nil, &res
	}
	if run.exitCode == 0 {
		return run, nil
	}
	for _, code := range okCodes {
		if run.exitCode == code {
			return run, nil
		}
	}
	res := failureResult(run)
	return nil, &res
}

// failureResult turns a failed invocation into an isError result. The stderr
// diagnostic JSON becomes structuredContent; any stdout (for example a config
// proposal that explains an inference failure) is appended as a second block.
func failureResult(run *engineRun) toolResult {
	stderr := strings.TrimSpace(string(run.stderr))
	var structured any
	var text string
	if line, parsed, ok := lastJSONObjectLine(stderr); ok {
		structured, text = parsed, line
	} else {
		message := stderr
		if message == "" {
			message = fmt.Sprintf("reconify exited with code %d and no diagnostic", run.exitCode)
		}
		res := errorResultExit("EXECUTION_FAILED", message, run.exitCode)
		structured, text = res.structured, message
	}
	result := toolResult{text: []string{text}, structured: structured, isError: true}
	if out := strings.TrimSpace(string(run.stdout)); out != "" {
		result.text = append(result.text, out)
	}
	return result
}

// lastJSONObjectLine finds the last stderr line that is a JSON object.
func lastJSONObjectLine(text string) (string, map[string]any, bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) == nil {
			return line, obj, true
		}
	}
	return "", nil, false
}

// stdoutJSON turns a successful run's stdout into a result mirroring the CLI
// output exactly.
func stdoutJSON(run *engineRun) toolResult {
	text := strings.TrimSpace(string(run.stdout))
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err != nil {
		return errorResult("EXECUTION_FAILED", "reconify produced output that is not a JSON object: "+truncate(text, 200))
	}
	return toolResult{text: []string{text}, structured: obj}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------------------
// Argument and path handling
// ---------------------------------------------------------------------------

func argStr(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

func argBool(args map[string]any, key string, def bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return def
}

func argInt(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return def
}

// containsNUL reports whether any string in the decoded JSON value holds a NUL
// byte, which no path or name argument may contain.
func containsNUL(v any) bool {
	switch val := v.(type) {
	case string:
		return strings.ContainsRune(val, 0)
	case []any:
		for _, item := range val {
			if containsNUL(item) {
				return true
			}
		}
	case map[string]any:
		for k, item := range val {
			if strings.ContainsRune(k, 0) || containsNUL(item) {
				return true
			}
		}
	}
	return false
}

// workDirFor resolves the directory a call runs in: the optional cwd argument
// (relative to the server working directory) or the server working directory.
func (s *Server) workDirFor(args map[string]any) (string, *toolResult) {
	cwd := argStr(args, "cwd")
	if cwd == "" {
		return s.workDir, nil
	}
	dir := cwd
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.workDir, dir)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		res := errorResult("INPUT_UNREADABLE", fmt.Sprintf("cwd %q is not an existing directory", cwd))
		return "", &res
	}
	return dir, nil
}

// resolvePath resolves a user path against dir for in-process reads.
func resolvePath(dir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(dir, p)
}

// rejectStdout refuses "-" for out paths, which would send the result inline.
func rejectStdout(out string) *toolResult {
	if out == "-" {
		res := errorResult("INVALID_ARGUMENTS", `out must be a file path; "-" (stdout) is not allowed because results are never returned inline`)
		return &res
	}
	return nil
}
