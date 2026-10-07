package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/reconifyhq/reconify/schemas"
)

func TestCapabilitiesCommandDescribesEngineSurface(t *testing.T) {
	root := newRootCmd("test-version", "test-time")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"capabilities"})

	if err := root.Execute(); err != nil {
		t.Fatalf("capabilities: %v", err)
	}

	var got schemas.Capabilities
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if got.Schema != schemas.CapabilitiesSchemaV1 {
		t.Fatalf("schema = %q, want %q", got.Schema, schemas.CapabilitiesSchemaV1)
	}
	if got.Engine.Version != "test-version" {
		t.Fatalf("engine.version = %q, want test-version", got.Engine.Version)
	}
	if got.Commands["reconcile"].Interactive {
		t.Fatalf("reconcile should be non-interactive")
	}
	if !got.Commands["config init"].Interactive {
		t.Fatalf("config init should be marked interactive")
	}
	if got.Formats["reconcile"].Default != "json" {
		t.Fatalf("reconcile default format = %q, want json", got.Formats["reconcile"].Default)
	}
	if len(got.Matching.Passes) != 5 {
		t.Fatalf("matching passes = %d, want 5", len(got.Matching.Passes))
	}
	if got.Schemas["diagnostic"] != schemas.DiagnosticSchemaV1 {
		t.Fatalf("diagnostic schema ID = %q, want %q", got.Schemas["diagnostic"], schemas.DiagnosticSchemaV1)
	}
	if got.Schemas["profile"] != schemas.ProfileSchemaV1 {
		t.Fatalf("profile schema ID = %q, want %q", got.Schemas["profile"], schemas.ProfileSchemaV1)
	}
	if got.Schemas["config_proposal"] != schemas.ConfigProposalSchemaV1 {
		t.Fatalf("config proposal schema ID = %q", got.Schemas["config_proposal"])
	}
	if got.Commands["config infer"].Interactive {
		t.Fatal("config infer should be non-interactive")
	}
	if got.Commands["explain"].Interactive {
		t.Fatal("explain should be non-interactive")
	}
	if got.Schemas["explanation"] != schemas.ExplanationSchemaV1 {
		t.Fatalf("explanation schema ID = %q", got.Schemas["explanation"])
	}
	if got.Commands["inspect"].Interactive {
		t.Fatalf("inspect should be non-interactive")
	}
	if got.Formats["inspect"].Default != "json" {
		t.Fatalf("inspect default format = %q, want json", got.Formats["inspect"].Default)
	}
	if got.ErrorCodes["EXCEPTIONS_FOUND"].ExitCode != ErrCodeExceptions {
		t.Fatalf("EXCEPTIONS_FOUND exit code = %d, want %d", got.ErrorCodes["EXCEPTIONS_FOUND"].ExitCode, ErrCodeExceptions)
	}
	if got.ErrorCodes[diagnosticCodeInteractiveUnsupported].ExitCode != ErrCodeConfig {
		t.Fatalf("INTERACTIVE_UNSUPPORTED exit code = %d, want %d", got.ErrorCodes[diagnosticCodeInteractiveUnsupported].ExitCode, ErrCodeConfig)
	}
	if got.ExitCodes["4"] == "" {
		t.Fatal("capabilities omitted exit code 4")
	}
}

func TestCapabilitiesDescribeVerifyAndUsageErrors(t *testing.T) {
	got := buildCapabilities()
	if _, ok := got.Commands["verify"]; !ok || got.Commands["verify"].Interactive {
		t.Fatalf("verify command missing or interactive: %+v", got.Commands["verify"])
	}
	if _, ok := got.Commands["schema verification"]; !ok {
		t.Fatal("schema verification command missing")
	}
	if got.Formats["verify"].Default != "json" {
		t.Fatalf("verify formats = %+v", got.Formats["verify"])
	}
	if got.Schemas["verification"] != schemas.VerificationSchemaV1 {
		t.Fatalf("verification schema ID = %q", got.Schemas["verification"])
	}
	if code := got.ErrorCodes[diagnosticCodeVerificationFailed]; code.ExitCode != ErrCodeVerification || code.Category != "verification" || code.LegacyCode != "verification_failed" {
		t.Fatalf("VERIFICATION_FAILED = %+v", code)
	}
	if code := got.ErrorCodes[diagnosticCodeUsageError]; code.ExitCode != 2 || code.Category != "usage" || code.LegacyCode != "usage_error" {
		t.Fatalf("USAGE_ERROR = %+v", code)
	}
	if got.ExitCodes["5"] == "" {
		t.Fatal("capabilities omitted exit code 5")
	}
}
