package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPCommandServesProtocolOnStdout(t *testing.T) {
	root := newRootCmd("test-version", "test")
	var out, errOut bytes.Buffer
	root.SetIn(strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"))
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"mcp", "--workdir", t.TempDir()})
	if err := root.Execute(); err != nil {
		t.Fatalf("mcp: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout has %d lines, want 2 (initialize, ping):\n%s", len(lines), out.String())
	}
	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil {
		t.Fatal(err)
	}
	if init.Result.ServerInfo.Name != "reconify-engine" || init.Result.ServerInfo.Version != "test-version" {
		t.Fatalf("serverInfo = %+v", init.Result.ServerInfo)
	}
	if init.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("protocolVersion = %q", init.Result.ProtocolVersion)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr should stay quiet without --verbose, got %q", errOut.String())
	}
}

func TestMCPCommandRejectsMissingWorkdir(t *testing.T) {
	root := newRootCmd("test", "test")
	root.SetIn(strings.NewReader(""))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"mcp", "--workdir", "/definitely/not/a/real/dir"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error for a missing working directory")
	}
	if ExitCode(err) != ErrCodeConfig {
		t.Fatalf("exit code = %d, want %d", ExitCode(err), ErrCodeConfig)
	}
}

func TestMCPCommandIsNonInteractiveCapability(t *testing.T) {
	capability, ok := buildCapabilities().Commands["mcp"]
	if !ok {
		t.Fatal("capabilities does not list the mcp command")
	}
	if capability.Interactive {
		t.Fatal("mcp must not be marked interactive")
	}
}
