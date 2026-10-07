package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reconifyhq/reconify/schemas"
)

func buildTestBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "reconify")
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- test builds the local CLI with a fixed command and a temp output path.
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/reconify")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, output)
	}
	return bin
}

func runTestBinary(t *testing.T, bin string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	// #nosec G204 -- test invokes the locally built binary with fixed test arguments.
	cmd := exec.Command(bin, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err == nil {
		return out.String(), errOut.String(), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run %v: %v", args, err)
	}
	return out.String(), errOut.String(), exitErr.ExitCode()
}

func TestJSONDiagnosticEnvelopeIsAdditiveAndKeepsStreamsSeparate(t *testing.T) {
	bin := buildTestBinary(t)
	missingConfig := filepath.Join(t.TempDir(), "missing.yaml")
	stdout, stderr, exitCode := runTestBinary(t, bin, "--error-format", "json", "config", "validate", "--config", missingConfig)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", exitCode, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	var envelope schemas.DiagnosticEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &envelope); err != nil {
		t.Fatalf("decode stderr JSON: %v\nstderr=%s", err, stderr)
	}
	if envelope.Error == "" || envelope.Code != "config_error" || envelope.OK || envelope.Schema != schemas.DiagnosticSchemaV1 {
		t.Fatalf("envelope = %+v", envelope)
	}
	if envelope.Diagnostic.Code != "CONFIG_INVALID" || envelope.Diagnostic.Category != "config" {
		t.Fatalf("diagnostic = %+v", envelope.Diagnostic)
	}
}

func TestDefaultTextDiagnosticRemainsLegacyPresentation(t *testing.T) {
	bin := buildTestBinary(t)
	missingConfig := filepath.Join(t.TempDir(), "missing.yaml")
	stdout, stderr, exitCode := runTestBinary(t, bin, "config", "validate", "--config", missingConfig)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", exitCode, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.HasPrefix(stderr, "Error: failed to load config:") || strings.Contains(stderr, "diagnostic") {
		t.Fatalf("text stderr changed unexpectedly: %q", stderr)
	}
}

func TestJSONDiagnosticCoversUsageErrors(t *testing.T) {
	bin := buildTestBinary(t)
	stdout, stderr, exitCode := runTestBinary(t, bin, "--error-format", "json", "reconcile", "--bogus")
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", exitCode, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	var envelope schemas.DiagnosticEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &envelope); err != nil {
		t.Fatalf("decode stderr JSON: %v\nstderr=%s", err, stderr)
	}
	if envelope.Code != "usage_error" || envelope.Diagnostic.Code != "USAGE_ERROR" || envelope.Diagnostic.Category != "usage" {
		t.Fatalf("envelope = %+v", envelope)
	}
	if envelope.Diagnostic.Details["usage"] == "" || len(envelope.Diagnostic.Suggestions) != 1 ||
		!strings.Contains(envelope.Diagnostic.Suggestions[0], "reconify reconcile --help") {
		t.Fatalf("usage diagnostic = %+v", envelope.Diagnostic)
	}
}

func TestAgentProfileStructuresUsageErrors(t *testing.T) {
	bin := buildTestBinary(t)
	stdout, stderr, exitCode := runTestBinary(t, bin, "--agent", "reconcile", "--bogus")
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", exitCode, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	var envelope schemas.DiagnosticEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &envelope); err != nil {
		t.Fatalf("decode stderr JSON: %v\nstderr=%s", err, stderr)
	}
	if envelope.Code != "usage_error" || envelope.Diagnostic.Code != "USAGE_ERROR" {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestAgentProfileStructuresUnknownCommands(t *testing.T) {
	bin := buildTestBinary(t)
	stdout, stderr, exitCode := runTestBinary(t, bin, "--agent", "no-such-command")
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", exitCode, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	var envelope schemas.DiagnosticEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &envelope); err != nil {
		t.Fatalf("decode stderr JSON: %v\nstderr=%s", err, stderr)
	}
	if envelope.Code != "usage_error" || envelope.Diagnostic.Code != "USAGE_ERROR" {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestAgentUnknownCommandSuggestsClosestMatch(t *testing.T) {
	bin := buildTestBinary(t)
	_, stderr, exitCode := runTestBinary(t, bin, "--agent", "reconsile")
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%s", exitCode, stderr)
	}
	var envelope schemas.DiagnosticEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stderr)), &envelope); err != nil {
		t.Fatalf("decode stderr JSON: %v\nstderr=%s", err, stderr)
	}
	if got := envelope.Diagnostic.Details["did_you_mean"]; got != "reconify reconcile" {
		t.Fatalf("did_you_mean = %v; envelope = %+v", got, envelope)
	}
}

func TestAgentConfigValidateWritesOnlyTheJSONEnvelopeToStderr(t *testing.T) {
	bin := buildTestBinary(t)
	path := filepath.Join(t.TempDir(), "reconify.yaml")
	config := "version: 1\nsources:\n  a:\n    file_pattern: a.csv\n    parser:\n      amount_col: amount\n      multiplier: 100\n      date_layout: \"2006-01-02\"\npairs:\n  p:\n    left: a\n    right: a\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exitCode := runTestBinary(t, bin, "--agent", "config", "validate", "--config", path)
	if exitCode != 2 || stdout != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%s", exitCode, stdout, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) != 1 {
		t.Fatalf("stderr must be exactly one JSON envelope, got %d lines:\n%s", len(lines), stderr)
	}
	var envelope struct {
		Diagnostic struct {
			Code    string `json:"code"`
			Details struct {
				Errors []struct {
					Path    string `json:"path"`
					Message string `json:"message"`
				} `json:"errors"`
			} `json:"details"`
		} `json:"diagnostic"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &envelope); err != nil {
		t.Fatalf("decode stderr JSON: %v\n%s", err, stderr)
	}
	if envelope.Diagnostic.Code != "CONFIG_INVALID" || len(envelope.Diagnostic.Details.Errors) < 2 {
		t.Fatalf("envelope = %+v", envelope)
	}
	first := envelope.Diagnostic.Details.Errors[0]
	if first.Path != "sources.a.parser.date_col" || first.Message != "required field is missing" {
		t.Fatalf("first error = %+v", first)
	}
}
