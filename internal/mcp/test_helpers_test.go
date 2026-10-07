package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reconifyBin is the reconify binary built once for the whole package.
var reconifyBin string

// moduleRoot is the repository root (the directory holding go.mod).
var moduleRoot string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	root, err := findModuleRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "find module root:", err)
		return 1
	}
	moduleRoot = root
	dir, err := os.MkdirTemp("", "reconify-mcp-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "temp dir:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	reconifyBin = filepath.Join(dir, "reconify")
	build := exec.Command("go", "build", "-o", reconifyBin, "./cmd/reconify") // #nosec G204 -- fixed arguments in a test.
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build reconify: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// copyFile copies src to dst, creating parent directories.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src) // #nosec G304 -- test fixture path.
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dst, string(data))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil { // #nosec G703 -- test temp dir.
		t.Fatal(err)
	}
}

// settlementWorkspace copies the evals/003-settlement-fee fixtures (config and
// inputs) into a fresh temp directory and returns it.
func settlementWorkspace(t *testing.T) string {
	t.Helper()
	src := filepath.Join(moduleRoot, "evals", "003-settlement-fee")
	dir := t.TempDir()
	copyFile(t, filepath.Join(src, "reference", "reconify.yaml"), filepath.Join(dir, "reconify.yaml"))
	copyFile(t, filepath.Join(src, "inputs", "left.csv"), filepath.Join(dir, "inputs", "left.csv"))
	copyFile(t, filepath.Join(src, "inputs", "right.csv"), filepath.Join(dir, "inputs", "right.csv"))
	return dir
}

// client drives a Server over in-memory pipes the way an MCP host would.
type client struct {
	t      *testing.T
	in     io.WriteCloser
	out    *bufio.Reader
	nextID int
	done   chan error
	cancel context.CancelFunc
}

func startClient(t *testing.T, opts Options) *client {
	t.Helper()
	srv, err := NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	c := &client{t: t, in: inW, out: bufio.NewReader(outR), done: make(chan error, 1), cancel: cancel}
	go func() {
		err := srv.Serve(ctx, inR, outW)
		_ = outW.Close()
		c.done <- err
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			t.Error("server did not stop after stdin closed")
		}
		cancel()
	})
	return c
}

// startToolClient starts a client backed by the real reconify binary.
func startToolClient(t *testing.T, workDir string) *client {
	t.Helper()
	return startClient(t, Options{Version: "test", Executable: reconifyBin, WorkDir: workDir})
}

func (c *client) sendRaw(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatalf("write to server: %v", err)
	}
}

func (c *client) send(msg map[string]any) {
	c.t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatal(err)
	}
	c.sendRaw(string(data))
}

// readMessage reads one server line, failing the test on timeout.
func (c *client) readMessage() map[string]any {
	c.t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := c.out.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			c.t.Fatalf("read from server: %v", r.err)
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(r.line), &msg); err != nil {
			c.t.Fatalf("server wrote invalid JSON %q: %v", r.line, err)
		}
		return msg
	case <-time.After(60 * time.Second):
		c.t.Fatal("timed out waiting for a server message")
		return nil
	}
}

// request sends a JSON-RPC request and returns the matching response.
func (c *client) request(method string, params any) map[string]any {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	c.send(msg)
	resp := c.readMessage()
	if got, ok := resp["id"].(float64); !ok || int(got) != id {
		c.t.Fatalf("response id = %v, want %d (response: %v)", resp["id"], id, resp)
	}
	return resp
}

func (c *client) notify(method string, params any) {
	c.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	c.send(msg)
}

func (c *client) initialize() map[string]any {
	c.t.Helper()
	resp := c.request("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	})
	c.notify("notifications/initialized", nil)
	return resp
}

// toolOutput is a decoded tools/call result.
type toolOutput struct {
	IsError    bool
	Text       []string
	Structured map[string]any
}

func (c *client) callTool(name string, args map[string]any) toolOutput {
	c.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	resp := c.request("tools/call", map[string]any{"name": name, "arguments": args})
	if resp["error"] != nil {
		c.t.Fatalf("tools/call %s returned a protocol error: %v", name, resp["error"])
	}
	result, _ := resp["result"].(map[string]any)
	out := toolOutput{}
	out.IsError, _ = result["isError"].(bool)
	out.Structured, _ = result["structuredContent"].(map[string]any)
	blocks, _ := result["content"].([]any)
	for _, b := range blocks {
		block, _ := b.(map[string]any)
		if block["type"] != "text" {
			c.t.Fatalf("content block type = %v, want text", block["type"])
		}
		text, _ := block["text"].(string)
		out.Text = append(out.Text, text)
	}
	return out
}

func diagnosticCode(t *testing.T, out toolOutput) string {
	t.Helper()
	diag, _ := out.Structured["diagnostic"].(map[string]any)
	code, _ := diag["code"].(string)
	return code
}

func mustFloat(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%s = %v (%T), want a number in %v", key, m[key], m[key], m)
	}
	return v
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func assertJSONText(t *testing.T, out toolOutput) {
	t.Helper()
	if len(out.Text) == 0 {
		t.Fatal("tool result has no text content")
	}
	if !json.Valid(bytes.TrimSpace([]byte(out.Text[0]))) {
		t.Fatalf("first text block is not JSON: %q", strings.TrimSpace(out.Text[0]))
	}
}
