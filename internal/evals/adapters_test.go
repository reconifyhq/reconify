package evals

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

const claudeSample = `{"type":"result","subtype":"success","is_error":false,"duration_ms":41000,"num_turns":9,"result":"Done. matched: 3","total_cost_usd":0.42,"usage":{"input_tokens":120,"cache_creation_input_tokens":1000,"cache_read_input_tokens":5000,"output_tokens":800}}`

const codexSample = `{"type":"thread.started","thread_id":"t1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"working on it"}}
{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":50}}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"i2","type":"agent_message","text":"Reconciled: 2 matched"}}
{"type":"turn.completed","usage":{"input_tokens":2000,"cached_input_tokens":900,"output_tokens":70}}
`

const geminiSample = `{
  "response": "All good: matched: 1",
  "stats": {
    "models": {
      "gemini-2.5-pro": {"api": {"totalRequests": 4}, "tokens": {"prompt": 900, "candidates": 60, "total": 1000, "cached": 10}},
      "gemini-2.5-flash": {"api": {"totalRequests": 2}, "tokens": {"prompt": 100, "candidates": 15, "total": 130}}
    }
  }
}`

const opencodeSample = `{"type":"step_start","timestamp":1,"sessionID":"s","part":{"type":"step-start"}}
{"type":"text","timestamp":2,"sessionID":"s","part":{"type":"text","text":"first"}}
{"type":"step_finish","timestamp":3,"sessionID":"s","part":{"type":"step-finish","cost":0.01,"tokens":{"input":100,"output":20,"reasoning":0,"cache":{"read":50,"write":10}}}}
{"type":"text","timestamp":4,"sessionID":"s","part":{"type":"text","text":"final answer"}}
{"type":"step_finish","timestamp":5,"sessionID":"s","part":{"type":"step-finish","cost":0.02,"tokens":{"input":30,"output":5,"cache":{"read":0,"write":0}}}}
`

func TestParseUsage(t *testing.T) {
	cost := func(v float64) *float64 { return &v }
	for _, testCase := range []struct {
		name   string
		agent  Agent
		output string
		want   Usage
	}{
		{"claude adds cache tokens", AgentClaude, claudeSample, Usage{Turns: intPtr(9), InputTokens: intPtr(6120), OutputTokens: intPtr(800), CostUSD: cost(0.42), WallMS: 7}},
		{"claude with stderr noise", AgentClaude, "warning: something\n" + claudeSample + "\n", Usage{Turns: intPtr(9), InputTokens: intPtr(6120), OutputTokens: intPtr(800), CostUSD: cost(0.42), WallMS: 7}},
		{"claude event array", AgentClaude, `[{"type":"system"},` + claudeSample + `]`, Usage{Turns: intPtr(9), InputTokens: intPtr(6120), OutputTokens: intPtr(800), CostUSD: cost(0.42), WallMS: 7}},
		{"claude without usage", AgentClaude, `{"type":"result","result":"x"}`, Usage{WallMS: 7}},
		{"claude plain text", AgentClaude, "not json at all", Usage{WallMS: 7}},
		{"codex sums turns", AgentCodex, codexSample, Usage{Turns: intPtr(2), InputTokens: intPtr(3000), OutputTokens: intPtr(120), WallMS: 7}},
		{"codex without events", AgentCodex, "plain output\n", Usage{WallMS: 7}},
		{"gemini sums models", AgentGemini, geminiSample, Usage{InputTokens: intPtr(1000), OutputTokens: intPtr(75), WallMS: 7}},
		{"gemini without stats", AgentGemini, `{"response":"hi"}`, Usage{WallMS: 7}},
		{"opencode sums steps", AgentOpenCode, opencodeSample, Usage{Turns: intPtr(2), InputTokens: intPtr(190), OutputTokens: intPtr(25), CostUSD: cost(0.03), WallMS: 7}},
		{"opencode without events", AgentOpenCode, "formatted output", Usage{WallMS: 7}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := parseUsage(testCase.agent, []byte(testCase.output), 7)
			if !usageEqual(*got, testCase.want) {
				t.Fatalf("usage = %s, want %s", describeUsage(*got), describeUsage(testCase.want))
			}
		})
	}
}

func usageEqual(a, b Usage) bool {
	near := func(x, y *float64) bool {
		if x == nil || y == nil {
			return x == y
		}
		return *x-*y < 1e-9 && *y-*x < 1e-9
	}
	return reflect.DeepEqual(a.Turns, b.Turns) && reflect.DeepEqual(a.InputTokens, b.InputTokens) &&
		reflect.DeepEqual(a.OutputTokens, b.OutputTokens) && near(a.CostUSD, b.CostUSD) && a.WallMS == b.WallMS
}

func describeUsage(u Usage) string {
	deref := func(p *int) any {
		if p == nil {
			return nil
		}
		return *p
	}
	var cost any
	if u.CostUSD != nil {
		cost = *u.CostUSD
	}
	return fmt.Sprintf("{turns:%v in:%v out:%v cost:%v wall:%d}", deref(u.Turns), deref(u.InputTokens), deref(u.OutputTokens), cost, u.WallMS)
}

func TestAgentMessages(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		agent  Agent
		output string
		want   []string
	}{
		{"claude result", AgentClaude, claudeSample, []string{"Done. matched: 3"}},
		{"codex agent messages", AgentCodex, codexSample, []string{"working on it", "Reconciled: 2 matched"}},
		{"gemini response", AgentGemini, geminiSample, []string{"All good: matched: 1"}},
		{"opencode text parts", AgentOpenCode, opencodeSample, []string{"first", "final answer"}},
		{"unrecognized falls back to raw", AgentClaude, "plain words", []string{"plain words"}},
		{"empty", AgentCodex, "", nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := agentMessages(testCase.agent, []byte(testCase.output)); !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("messages = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestAgentCommandsRequestMachineReadableOutput(t *testing.T) {
	workspace := t.TempDir()
	for agent, want := range map[Agent][]string{
		AgentClaude:   {"--output-format", "json"},
		AgentCodex:    {"--json"},
		AgentGemini:   {"--output-format", "json"},
		AgentOpenCode: {"--format", "json"},
	} {
		args := agentCommand(context.Background(), agent, workspace, "task").Args
		for _, flag := range want {
			if !containsString(args, flag) {
				t.Errorf("%s command %v lacks %q", agent, args, flag)
			}
		}
	}
}

func TestEnvWithoutDropsOnlyNamedVariables(t *testing.T) {
	got := envWithout([]string{"A=1", "RECONIFY_TRACE_FILE=/x", "RECONIFY_TRACE_FILE_OTHER=y", "B=2"}, traceEnv)
	want := []string{"A=1", "RECONIFY_TRACE_FILE_OTHER=y", "B=2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
}
