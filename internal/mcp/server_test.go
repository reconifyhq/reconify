package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func protocolClient(t *testing.T) *client {
	t.Helper()
	// Protocol tests never execute the engine.
	return startClient(t, Options{Version: "1.2.3", Executable: "/nonexistent/reconify", WorkDir: t.TempDir()})
}

func TestInitializeNegotiatesProtocolVersion(t *testing.T) {
	tests := []struct {
		name      string
		requested any
		want      string
	}{
		{"latest", "2025-11-25", "2025-11-25"},
		{"2025-06-18", "2025-06-18", "2025-06-18"},
		{"2025-03-26", "2025-03-26", "2025-03-26"},
		{"2024-11-05", "2024-11-05", "2024-11-05"},
		{"unsupported falls back to latest", "1999-01-01", "2025-11-25"},
		{"missing falls back to latest", nil, "2025-11-25"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := protocolClient(t)
			params := map[string]any{"capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "x", "version": "1"}}
			if tt.requested != nil {
				params["protocolVersion"] = tt.requested
			}
			resp := c.request("initialize", params)
			result, _ := resp["result"].(map[string]any)
			if got := result["protocolVersion"]; got != tt.want {
				t.Fatalf("protocolVersion = %v, want %s", got, tt.want)
			}
			info, _ := result["serverInfo"].(map[string]any)
			if info["name"] != "reconify-engine" || info["version"] != "1.2.3" {
				t.Fatalf("serverInfo = %v", info)
			}
			caps, _ := result["capabilities"].(map[string]any)
			if _, ok := caps["tools"]; !ok {
				t.Fatalf("capabilities = %v, want a tools entry", caps)
			}
		})
	}
}

func TestPingBeforeAndAfterInitialize(t *testing.T) {
	c := protocolClient(t)
	if resp := c.request("ping", nil); resp["error"] != nil || resp["result"] == nil {
		t.Fatalf("ping = %v", resp)
	}
	c.initialize()
	if resp := c.request("ping", nil); resp["error"] != nil || resp["result"] == nil {
		t.Fatalf("ping after initialize = %v", resp)
	}
}

func TestNotificationsNeverGetResponses(t *testing.T) {
	c := protocolClient(t)
	c.notify("notifications/initialized", nil)
	c.notify("notifications/something_unknown", map[string]any{"x": 1})
	c.notify("tools/list", nil) // a request method sent as a notification is still not answered
	c.notify("ping", nil)
	// The next message on the wire must be the answer to this ping, proving
	// none of the notifications above produced output.
	resp := c.request("ping", nil)
	if resp["error"] != nil {
		t.Fatalf("ping = %v", resp)
	}
}

func TestUnknownMethodReturnsMethodNotFound(t *testing.T) {
	c := protocolClient(t)
	resp := c.request("resources/list", nil)
	rpcErr, _ := resp["error"].(map[string]any)
	if rpcErr == nil || rpcErr["code"] != float64(-32601) {
		t.Fatalf("error = %v, want code -32601", resp["error"])
	}
	if resp["result"] != nil {
		t.Fatalf("error response must not carry a result: %v", resp)
	}
}

func TestMalformedJSONReturnsParseErrorAndServerSurvives(t *testing.T) {
	c := protocolClient(t)
	c.sendRaw(`{"jsonrpc":"2.0","id":1,"method":`)
	resp := c.readMessage()
	rpcErr, _ := resp["error"].(map[string]any)
	if rpcErr == nil || rpcErr["code"] != float64(-32700) {
		t.Fatalf("error = %v, want code -32700", resp["error"])
	}
	if id, present := resp["id"]; !present || id != nil {
		t.Fatalf("parse error id = %v (present=%v), want null", id, present)
	}
	if resp := c.request("ping", nil); resp["error"] != nil {
		t.Fatalf("server did not survive malformed input: %v", resp)
	}
}

func TestInvalidRequestShapes(t *testing.T) {
	c := protocolClient(t)
	c.sendRaw(`{"jsonrpc":"1.0","id":7,"method":"ping"}`)
	resp := c.readMessage()
	rpcErr, _ := resp["error"].(map[string]any)
	if rpcErr == nil || rpcErr["code"] != float64(-32600) || resp["id"] != float64(7) {
		t.Fatalf("response = %v, want -32600 for id 7", resp)
	}
	c.sendRaw(`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`)
	resp = c.readMessage()
	rpcErr, _ = resp["error"].(map[string]any)
	if rpcErr == nil || rpcErr["code"] != float64(-32600) {
		t.Fatalf("batch response = %v, want -32600", resp)
	}
	// String ids are echoed verbatim.
	c.sendRaw(`{"jsonrpc":"2.0","id":"abc","method":"ping"}`)
	if resp := c.readMessage(); resp["id"] != "abc" || resp["error"] != nil {
		t.Fatalf("string id response = %v", resp)
	}
}

func TestBlankLinesAreIgnored(t *testing.T) {
	c := protocolClient(t)
	c.sendRaw("")
	c.sendRaw("   ")
	if resp := c.request("ping", nil); resp["error"] != nil {
		t.Fatalf("ping = %v", resp)
	}
}

// expectedTools is the contract snapshot: tool name -> required input fields.
var expectedTools = []struct {
	name     string
	required []string
	readOnly bool
}{
	{"capabilities", nil, true},
	{"inspect_file", []string{"path"}, true},
	{"infer_config", []string{"left", "right"}, false},
	{"validate_config", nil, true},
	{"check_source", []string{"source", "file"}, true},
	{"reconcile", []string{"out"}, false},
	{"reconcile_auto", []string{"left", "right", "out"}, false},
	{"get_summary", []string{"result"}, true},
	{"list_exceptions", []string{"result"}, true},
	{"explain_result", []string{"result"}, true},
	{"verify_workspace", nil, true},
}

func TestToolsListSnapshot(t *testing.T) {
	c := protocolClient(t)
	c.initialize()
	resp := c.request("tools/list", nil)
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != len(expectedTools) {
		t.Fatalf("got %d tools, want %d", len(tools), len(expectedTools))
	}
	for i, want := range expectedTools {
		tool, _ := tools[i].(map[string]any)
		if tool["name"] != want.name {
			t.Fatalf("tool %d = %v, want %s", i, tool["name"], want.name)
		}
		if desc, _ := tool["description"].(string); len(desc) < 40 {
			t.Errorf("%s: description too short for an LLM: %q", want.name, desc)
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s: inputSchema must be a closed object: %v", want.name, schema)
		}
		props, _ := schema["properties"].(map[string]any)
		if _, ok := props["cwd"]; !ok {
			t.Errorf("%s: missing cwd property", want.name)
		}
		var required []string
		if raw, ok := schema["required"].([]any); ok {
			for _, r := range raw {
				required = append(required, r.(string))
			}
		}
		if strings.Join(required, ",") != strings.Join(want.required, ",") {
			t.Errorf("%s: required = %v, want %v", want.name, required, want.required)
		}
		for _, field := range want.required {
			if _, ok := props[field]; !ok {
				t.Errorf("%s: required field %s has no property definition", want.name, field)
			}
		}
		ann, _ := tool["annotations"].(map[string]any)
		if ann["readOnlyHint"] != want.readOnly {
			t.Errorf("%s: readOnlyHint = %v, want %v", want.name, ann["readOnlyHint"], want.readOnly)
		}
		if !want.readOnly && ann["destructiveHint"] != false {
			t.Errorf("%s: writing tools must set destructiveHint=false: %v", want.name, ann)
		}
	}
}

func TestToolsListNamesCoverLockedContract(t *testing.T) {
	// The tools locked by roadmap issue #100 must all be present.
	locked := []string{"capabilities", "inspect_file", "infer_config", "validate_config", "reconcile",
		"reconcile_auto", "get_summary", "list_exceptions", "explain_result"}
	var names []string
	for _, tl := range expectedTools {
		names = append(names, tl.name)
	}
	for _, name := range locked {
		if !contains(names, name) {
			t.Errorf("locked tool %s missing", name)
		}
	}
}

func TestUnknownToolIsProtocolError(t *testing.T) {
	c := protocolClient(t)
	resp := c.request("tools/call", map[string]any{"name": "nope", "arguments": map[string]any{}})
	rpcErr, _ := resp["error"].(map[string]any)
	if rpcErr == nil || rpcErr["code"] != float64(-32602) {
		t.Fatalf("error = %v, want -32602", resp["error"])
	}
}

func TestInvalidArgumentsAreToolErrors(t *testing.T) {
	c := protocolClient(t)
	tests := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"missing required", "reconcile", map[string]any{}},
		{"unknown property", "validate_config", map[string]any{"bogus": true}},
		{"wrong type", "inspect_file", map[string]any{"path": 5}},
		{"bad enum", "reconcile", map[string]any{"out": "r.json", "format": "xml"}},
		{"limit too large", "list_exceptions", map[string]any{"result": "r.json", "limit": 100000}},
		{"unknown exception type", "list_exceptions", map[string]any{"result": "r.json", "types": []any{"match"}}},
		{"empty path", "inspect_file", map[string]any{"path": ""}},
		{"NUL in path", "inspect_file", map[string]any{"path": "a\x00b.csv"}},
		{"NUL in cwd", "capabilities", map[string]any{"cwd": "a\x00b"}},
		{"stdout out", "reconcile", map[string]any{"out": "-"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := c.callTool(tt.tool, tt.args)
			if !out.IsError {
				t.Fatalf("expected isError, got %+v", out)
			}
			if code := diagnosticCode(t, out); code != "USAGE_ERROR" {
				t.Fatalf("diagnostic code = %q, want USAGE_ERROR (%+v)", code, out.Structured)
			}
		})
	}
}

func TestMissingExecutableIsToolError(t *testing.T) {
	c := startClient(t, Options{Executable: "/nonexistent/reconify", WorkDir: t.TempDir()})
	out := c.callTool("capabilities", nil)
	if !out.IsError || diagnosticCode(t, out) != "EXECUTION_FAILED" {
		t.Fatalf("expected EXECUTION_FAILED tool error, got %+v", out)
	}
}

func TestNewServerRejectsBadWorkDir(t *testing.T) {
	if _, err := NewServer(Options{WorkDir: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("expected an error for a missing working directory")
	}
	file := filepath.Join(t.TempDir(), "f")
	writeFile(t, file, "x")
	if _, err := NewServer(Options{WorkDir: file}); err == nil {
		t.Fatal("expected an error when the working directory is a file")
	}
}

func TestServeStopsOnEOFAndContextCancel(t *testing.T) {
	srv, err := NewServer(Options{Executable: "/nonexistent", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":1`) {
		t.Fatalf("output = %q", out.String())
	}
	// A line without a trailing newline at EOF is still processed.
	out.Reset()
	if err := srv.Serve(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"ping"}`), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":2`) {
		t.Fatalf("output = %q", out.String())
	}
}

// writeSlowExecutable writes a script that sleeps, standing in for a long
// reconcile so cancellation can be tested deterministically.
func writeSlowExecutable(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in is not available on windows")
	}
	path := filepath.Join(t.TempDir(), "slow-reconify")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil { // #nosec G306 -- test executable.
		t.Fatal(err)
	}
	return path
}

func TestCancellationStopsToolCallWithoutResponse(t *testing.T) {
	c := startClient(t, Options{Executable: writeSlowExecutable(t), WorkDir: t.TempDir()})
	c.send(map[string]any{"jsonrpc": "2.0", "id": 100, "method": "tools/call",
		"params": map[string]any{"name": "capabilities", "arguments": map[string]any{}}})
	// The server stays responsive while a call is running.
	if resp := c.request("ping", nil); resp["error"] != nil {
		t.Fatalf("ping during tool call = %v", resp)
	}
	start := time.Now()
	c.notify("notifications/cancelled", map[string]any{"requestId": 100, "reason": "test"})
	// A cancelled request gets no response, so the next message must be the
	// ping answer, and it arrives only after the worker is free again.
	c.notify("ping", nil)
	resp := c.request("ping", nil)
	if resp["error"] != nil {
		t.Fatalf("ping after cancel = %v", resp)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func TestToolCallsRunInArrivalOrder(t *testing.T) {
	// Responses to queued tool calls come back in request order even though
	// the client never waits between them.
	c := startClient(t, Options{Executable: "/nonexistent/reconify", WorkDir: t.TempDir()})
	for id := 1; id <= 5; id++ {
		c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call",
			"params": map[string]any{"name": "capabilities", "arguments": map[string]any{}}})
	}
	for id := 1; id <= 5; id++ {
		resp := c.readMessage()
		if resp["id"] != float64(id) {
			t.Fatalf("response id = %v, want %d", resp["id"], id)
		}
	}
}

func TestServerNeverWritesNonProtocolOutput(t *testing.T) {
	srv, err := NewServer(Options{Executable: "/nonexistent", WorkDir: t.TempDir(), Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`not json`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n"
	if err := srv.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d output lines, want 3 (initialize, parse error, tools/list):\n%s", len(lines), out.String())
	}
	for _, line := range lines {
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil || msg["jsonrpc"] != "2.0" {
			t.Fatalf("non-protocol output line: %q", line)
		}
	}
}
