package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Agent identifies a supported local coding-agent CLI.
type Agent string

// Supported local coding-agent CLIs.
const (
	AgentClaude   Agent = "claude"
	AgentCodex    Agent = "codex"
	AgentGemini   Agent = "gemini"
	AgentOpenCode Agent = "opencode"
)

// traceFileName is the per-workspace Engine trace written via traceEnv.
const traceFileName = ".reconify-eval-trace.jsonl"

// traceEnv makes the Engine append one JSON line per process to the named file.
const traceEnv = "RECONIFY_TRACE_FILE"

// claudeAllowedTools lets a headless Claude run commands and edit files in its workspace.
const claudeAllowedTools = "Bash,Read,Write,Edit,MultiEdit,Glob,Grep"

func supportedAgents() []Agent {
	return []Agent{AgentClaude, AgentCodex, AgentGemini, AgentOpenCode}
}

func agentCommand(ctx context.Context, agent Agent, workspace, prompt string) *exec.Cmd {
	return agentCommandWithModel(ctx, agent, workspace, prompt, "")
}

func agentCommandWithModel(ctx context.Context, agent Agent, workspace, prompt, model string) *exec.Cmd {
	modelArgs := func(args []string, flag string) []string {
		if model != "" {
			args = append(args, flag, model)
		}
		return args
	}
	switch agent {
	case AgentClaude:
		// acceptEdits approves edits but not Bash, so unattended runs need an
		// explicit allowlist. --allowedTools is variadic: keep it before other
		// flags so the trailing prompt is never read as a tool name.
		args := modelArgs([]string{"-p", "--allowedTools", claudeAllowedTools, "--output-format", "json", "--permission-mode", "acceptEdits"}, "--model")
		args = append(args, prompt)
		return exec.CommandContext(ctx, "claude", args...) // #nosec G204 -- fixed adapter command.
	case AgentCodex:
		args := modelArgs([]string{"exec", "--json", "-C", workspace, "--sandbox", "workspace-write"}, "-m")
		args = append(args, prompt)
		return exec.CommandContext(ctx, "codex", args...) // #nosec G204 -- fixed adapter command.
	case AgentGemini:
		args := modelArgs([]string{"--prompt", prompt, "--output-format", "json", "--approval-mode", "auto_edit", "--skip-trust"}, "--model")
		return exec.CommandContext(ctx, "gemini", args...) // #nosec G204 -- fixed adapter command.
	default:
		args := modelArgs([]string{"run", "--format", "json", "--dir", workspace, "--auto"}, "--model")
		args = append(args, prompt)
		return exec.CommandContext(ctx, "opencode", args...) // #nosec G204 -- fixed adapter command.
	}
}

func runAgent(ctx context.Context, agent Agent, workspace, prompt, reconifyPath, model string) ([]byte, error) {
	cmd := agentCommandWithModel(ctx, agent, workspace, prompt, model)
	cmd.Dir = workspace
	cmd.Env = append(envWithout(os.Environ(), traceEnv),
		"PATH="+filepath.Join(workspace, ".bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RECONIFY_EVAL_LOG="+filepath.Join(workspace, ".reconify-eval-commands.log"),
		"RECONIFY_EVAL_BINARY="+reconifyPath,
		traceEnv+"="+filepath.Join(workspace, traceFileName),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s: %w", agent, err)
	}
	return output, nil
}

// envWithout returns env minus every entry for the named variables.
func envWithout(env []string, names ...string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		drop := false
		for _, name := range names {
			if strings.HasPrefix(entry, name+"=") {
				drop = true
				break
			}
		}
		if !drop {
			result = append(result, entry)
		}
	}
	return result
}

// jsonValues decodes every top-level JSON object found in output, skipping
// interleaved non-JSON noise such as stderr. It handles a single pretty-printed
// document, JSONL streams, and a top-level array of events.
func jsonValues(output []byte) []map[string]any {
	var values []map[string]any
	rest := output
	for len(rest) > 0 {
		start := bytes.IndexAny(rest, "{[")
		if start < 0 {
			break
		}
		decoder := json.NewDecoder(bytes.NewReader(rest[start:]))
		var value any
		if err := decoder.Decode(&value); err != nil {
			rest = rest[start+1:]
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			values = append(values, typed)
		case []any:
			for _, item := range typed {
				if object, ok := item.(map[string]any); ok {
					values = append(values, object)
				}
			}
		}
		rest = rest[start+int(decoder.InputOffset()):]
	}
	return values
}

func asMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func asString(value any) string {
	result, _ := value.(string)
	return result
}

// number reads a JSON number from object[key]; ok is false when absent.
func number(object map[string]any, key string) (float64, bool) {
	value, ok := object[key].(float64)
	return value, ok
}

func intPtr(value int) *int { return &value }

// parseUsage converts an agent CLI's output into resource usage. Values the
// CLI does not expose stay nil; wall time is always the measured duration.
func parseUsage(agent Agent, output []byte, wallMS int64) *Usage {
	usage := &Usage{WallMS: wallMS}
	values := jsonValues(output)
	switch agent {
	case AgentClaude:
		parseClaudeUsage(values, usage)
	case AgentCodex:
		parseCodexUsage(values, usage)
	case AgentGemini:
		parseGeminiUsage(values, usage)
	case AgentOpenCode:
		parseOpenCodeUsage(values, usage)
	}
	return usage
}

// parseClaudeUsage reads the final `result` object of `-p --output-format json`.
// Cache tokens are reported separately from input_tokens, so they are added.
func parseClaudeUsage(values []map[string]any, usage *Usage) {
	var result map[string]any
	for _, value := range values {
		if _, ok := value["num_turns"]; ok || value["type"] == "result" {
			result = value
		}
	}
	if result == nil {
		return
	}
	if turns, ok := number(result, "num_turns"); ok {
		usage.Turns = intPtr(int(turns))
	}
	if cost, ok := number(result, "total_cost_usd"); ok {
		usage.CostUSD = &cost
	}
	tokens := asMap(result["usage"])
	if tokens == nil {
		return
	}
	input, hasInput := number(tokens, "input_tokens")
	for _, key := range []string{"cache_creation_input_tokens", "cache_read_input_tokens"} {
		if extra, ok := number(tokens, key); ok {
			input, hasInput = input+extra, true
		}
	}
	if hasInput {
		usage.InputTokens = intPtr(int(input))
	}
	if output, ok := number(tokens, "output_tokens"); ok {
		usage.OutputTokens = intPtr(int(output))
	}
}

// parseCodexUsage sums `turn.completed` events from `codex exec --json`.
// Codex input_tokens already include cached_input_tokens, so only the former is used.
func parseCodexUsage(values []map[string]any, usage *Usage) {
	turns, input, output := 0, 0, 0
	var seenInput, seenOutput bool
	for _, value := range values {
		if value["type"] != "turn.completed" {
			continue
		}
		turns++
		tokens := asMap(value["usage"])
		if in, ok := number(tokens, "input_tokens"); ok {
			input, seenInput = input+int(in), true
		}
		if out, ok := number(tokens, "output_tokens"); ok {
			output, seenOutput = output+int(out), true
		}
	}
	if turns == 0 {
		return
	}
	usage.Turns = intPtr(turns)
	if seenInput {
		usage.InputTokens = intPtr(input)
	}
	if seenOutput {
		usage.OutputTokens = intPtr(output)
	}
}

// parseGeminiUsage sums stats.models.<name>.tokens.{prompt,candidates} from
// `--output-format json`. The turn count is not exposed and stays nil.
func parseGeminiUsage(values []map[string]any, usage *Usage) {
	input, output := 0, 0
	var seenInput, seenOutput bool
	for _, value := range values {
		models := asMap(asMap(value["stats"])["models"])
		for _, model := range models {
			tokens := asMap(asMap(model)["tokens"])
			if prompt, ok := number(tokens, "prompt"); ok {
				input, seenInput = input+int(prompt), true
			}
			if candidates, ok := number(tokens, "candidates"); ok {
				output, seenOutput = output+int(candidates), true
			}
		}
	}
	if seenInput {
		usage.InputTokens = intPtr(input)
	}
	if seenOutput {
		usage.OutputTokens = intPtr(output)
	}
}

// parseOpenCodeUsage sums `step_finish` events from `run --format json`. Cache
// reads and writes are reported separately from input and are added to it.
func parseOpenCodeUsage(values []map[string]any, usage *Usage) {
	steps, input, output := 0, 0, 0
	cost := 0.0
	var seenTokens, seenCost bool
	for _, value := range values {
		if value["type"] != "step_finish" {
			continue
		}
		steps++
		part := asMap(value["part"])
		if stepCost, ok := number(part, "cost"); ok {
			cost, seenCost = cost+stepCost, true
		}
		tokens := asMap(part["tokens"])
		if tokens == nil {
			continue
		}
		seenTokens = true
		in, _ := number(tokens, "input")
		out, _ := number(tokens, "output")
		cache := asMap(tokens["cache"])
		read, _ := number(cache, "read")
		write, _ := number(cache, "write")
		input += int(in + read + write)
		output += int(out)
	}
	if steps == 0 {
		return
	}
	usage.Turns = intPtr(steps)
	if seenCost {
		usage.CostUSD = &cost
	}
	if seenTokens {
		usage.InputTokens, usage.OutputTokens = intPtr(input), intPtr(output)
	}
}

// agentMessages returns the assistant's textual messages in order, so the last
// one is the final answer. Unrecognized output is returned whole as one message.
func agentMessages(agent Agent, output []byte) []string {
	values := jsonValues(output)
	var messages []string
	switch agent {
	case AgentClaude:
		for _, value := range values {
			if text := asString(value["result"]); text != "" {
				messages = append(messages, text)
			}
		}
	case AgentCodex:
		for _, value := range values {
			item := asMap(value["item"])
			if value["type"] == "item.completed" && item["type"] == "agent_message" {
				if text := asString(item["text"]); text != "" {
					messages = append(messages, text)
				}
			}
		}
	case AgentGemini:
		for _, value := range values {
			if text := asString(value["response"]); text != "" {
				messages = append(messages, text)
			}
		}
	case AgentOpenCode:
		for _, value := range values {
			if value["type"] == "text" {
				if text := asString(asMap(value["part"])["text"]); text != "" {
					messages = append(messages, text)
				}
			}
		}
	}
	if len(messages) == 0 && len(bytes.TrimSpace(output)) > 0 {
		return []string{string(output)}
	}
	return messages
}
