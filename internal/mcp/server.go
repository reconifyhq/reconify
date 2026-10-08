// Package mcp implements the Reconify Engine MCP server.
//
// The server speaks newline-delimited JSON-RPC 2.0 over stdio (the MCP "stdio"
// transport) and exposes the Reconify Engine CLI as MCP tools. It is
// deliberately dependency-free: tools execute by re-invoking the reconify
// binary with --agent and explicit arguments, so tool output is byte-for-byte
// the CLI contract. Result files are read in-process only for the lightweight
// get_summary and list_exceptions tools, and are always streamed.
//
// Nothing in this package writes to stdout except protocol messages; logs go to
// the configured stderr writer.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ServerName is the locked MCP server identity for the Reconify Engine.
const ServerName = "reconify-engine"

// SupportedProtocolVersions lists the MCP protocol revisions this server can
// speak, newest first. The wire behaviour used here is identical across them.
var SupportedProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

const instructions = "Reconify Engine reconciles financial files locally. Typical workflow: capabilities -> " +
	"inspect_file (each input) -> infer_config -> validate_config -> check_source -> reconcile (results go to a file) -> " +
	"get_summary -> list_exceptions / explain_result -> verify_workspace. " +
	"reconcile_auto runs the whole inference-plus-reconcile path in one call when the files are unambiguous. " +
	"Paths are relative to the server working directory unless a cwd argument is given."

// Options configures a Server.
type Options struct {
	// Version is reported as serverInfo.version.
	Version string
	// Executable is the reconify binary re-invoked for each tool call.
	// Defaults to os.Executable().
	Executable string
	// WorkDir is the default working directory for tool calls and the base for
	// relative paths. Defaults to the process working directory.
	WorkDir string
	// Stderr receives diagnostic logs. Defaults to io.Discard.
	Stderr io.Writer
}

// Server is a Reconify Engine MCP server instance.
type Server struct {
	version string
	exe     string
	workDir string
	logw    io.Writer
	tools   []*tool
	byName  map[string]*tool

	writeMu sync.Mutex
	out     io.Writer

	inflightMu sync.Mutex
	inflight   map[string]context.CancelFunc
}

// NewServer validates options and builds a Server.
func NewServer(opts Options) (*Server, error) {
	exe := opts.Executable
	if exe == "" {
		resolved, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve reconify executable: %w", err)
		}
		exe = resolved
	}
	workDir := opts.WorkDir
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve working directory: %w", err)
		}
		workDir = wd
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("working directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("working directory %q is not a directory", abs)
	}
	logw := opts.Stderr
	if logw == nil {
		logw = io.Discard
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	s := &Server{
		version:  version,
		exe:      exe,
		workDir:  abs,
		logw:     logw,
		inflight: map[string]context.CancelFunc{},
	}
	tools, err := buildTools()
	if err != nil {
		return nil, err
	}
	s.tools = tools
	s.byName = make(map[string]*tool, len(tools))
	for _, t := range tools {
		s.byName[t.Name] = t
	}
	return s, nil
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type inputLine struct {
	data []byte
	err  error
}

// Serve reads newline-delimited JSON-RPC messages from r and writes responses
// to w until r reaches EOF or ctx is cancelled. In-flight tool calls finish
// (or are cancelled with ctx) before Serve returns.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	s.out = w
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := make(chan inputLine)
	go func() {
		defer close(lines)
		br := bufio.NewReaderSize(r, 64*1024)
		for {
			data, err := br.ReadBytes('\n')
			if len(data) > 0 {
				select {
				case lines <- inputLine{data: data}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					select {
					case lines <- inputLine{err: err}:
					case <-ctx.Done():
					}
				}
				return
			}
		}
	}()

	// Tool calls run one at a time, in arrival order, on a single worker so
	// that dependent calls (reconcile, then list_exceptions) and file writes
	// never race. Reading continues meanwhile, so ping and cancellation stay
	// responsive during a long reconcile.
	queue := make(chan func(), 1024)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for job := range queue {
			job()
		}
	}()
	defer func() {
		close(queue)
		wg.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			if line.err != nil {
				return fmt.Errorf("read stdin: %w", line.err)
			}
			s.handleLine(ctx, queue, bytes.TrimSpace(line.data))
		}
	}
}

func (s *Server) handleLine(ctx context.Context, queue chan<- func(), line []byte) {
	if len(line) == 0 {
		return
	}
	if line[0] == '[' {
		s.reply(json.RawMessage("null"), nil, &rpcError{Code: codeInvalidRequest, Message: "JSON-RPC batches are not supported"})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(json.RawMessage("null"), nil, &rpcError{Code: codeParseError, Message: "Parse error: " + err.Error()})
		return
	}
	isNotification := req.ID == nil
	if req.Method == "" {
		// A response from the client to a server request; this server never
		// sends requests, so there is nothing to correlate.
		if !isNotification {
			s.logf("ignoring unsolicited response for id %s", string(req.ID))
		}
		return
	}
	if req.JSONRPC != "2.0" {
		if !isNotification {
			s.reply(req.ID, nil, &rpcError{Code: codeInvalidRequest, Message: `Invalid Request: "jsonrpc" must be "2.0"`})
		}
		return
	}
	if isNotification {
		s.handleNotification(req)
		return
	}
	switch req.Method {
	case "initialize":
		s.reply(req.ID, s.initializeResult(req.Params), nil)
	case "ping":
		s.reply(req.ID, map[string]any{}, nil)
	case "tools/list":
		s.reply(req.ID, s.listTools(), nil)
	case "tools/call":
		callCtx, cancel := context.WithCancel(ctx)
		key := idKey(req.ID)
		s.inflightMu.Lock()
		s.inflight[key] = cancel
		s.inflightMu.Unlock()
		queue <- func() {
			defer cancel()
			defer func() {
				s.inflightMu.Lock()
				delete(s.inflight, key)
				s.inflightMu.Unlock()
			}()
			if callCtx.Err() != nil {
				// Cancelled by the client before or during the call (the spec
				// says not to respond), or the server is shutting down.
				return
			}
			result, rpcErr := s.callTool(callCtx, req.Params)
			if callCtx.Err() != nil {
				return
			}
			s.reply(req.ID, result, rpcErr)
		}
	default:
		s.reply(req.ID, nil, &rpcError{Code: codeMethodNotFound, Message: "Method not found: " + req.Method})
	}
}

func (s *Server) handleNotification(req rpcRequest) {
	switch req.Method {
	case "notifications/initialized":
		s.logf("client initialized")
	case "notifications/cancelled":
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
			Reason    string          `json:"reason"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.RequestID == nil {
			return
		}
		s.inflightMu.Lock()
		cancel := s.inflight[idKey(params.RequestID)]
		s.inflightMu.Unlock()
		if cancel != nil {
			s.logf("cancelling request %s: %s", string(params.RequestID), params.Reason)
			cancel()
		}
	default:
		// Unknown notifications are ignored per JSON-RPC.
	}
}

// idKey normalises a JSON-RPC id so that equal ids compare equal regardless of
// whitespace in the raw message.
func idKey(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func (s *Server) initializeResult(params json.RawMessage) map[string]any {
	version := SupportedProtocolVersions[0]
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 && json.Unmarshal(params, &p) == nil {
		for _, supported := range SupportedProtocolVersions {
			if p.ProtocolVersion == supported {
				version = supported
				break
			}
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": ServerName, "version": s.version},
		"instructions":    instructions,
	}
}

func (s *Server) listTools() map[string]any {
	defs := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		defs = append(defs, t.definition())
	}
	return map[string]any{"tools": defs}
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Invalid params: " + err.Error()}
	}
	t, ok := s.byName[p.Name]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("Unknown tool: %q", p.Name)}
	}
	args := map[string]any{}
	if trimmed := bytes.TrimSpace(p.Arguments); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &args); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "Invalid params: arguments must be an object"}
		}
	}
	if err := t.resolved.Validate(args); err != nil {
		return errorResult("INVALID_ARGUMENTS", fmt.Sprintf("invalid arguments for %s: %v", t.Name, err)).toMCP(), nil
	}
	if containsNUL(args) {
		return errorResult("INVALID_ARGUMENTS", "arguments must not contain NUL characters").toMCP(), nil
	}
	return t.run(ctx, s, args).toMCP(), nil
}

func (s *Server) reply(id json.RawMessage, result any, rpcErr *rpcError) {
	resp := rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr}
	if rpcErr != nil {
		resp.Result = nil
	} else if resp.Result == nil {
		resp.Result = map[string]any{}
	}
	data, err := json.Marshal(resp)
	if err != nil {
		s.logf("marshal response: %v", err)
		data, _ = json.Marshal(rpcResponse{
			JSONRPC: "2.0", ID: id,
			Error: &rpcError{Code: codeInternalError, Message: "failed to encode response"},
		})
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.out.Write(append(data, '\n')); err != nil {
		s.logf("write response: %v", err)
	}
}

func (s *Server) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.logw, "reconify-engine: "+strings.TrimRight(format, "\n")+"\n", args...)
}
