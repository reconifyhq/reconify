package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reconifyhq/reconify/schemas"
)

// runCLI executes one in-process invocation and returns stdout, stderr, and the
// command error.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRootCmd("test", "test")
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// verifyWorkspace materializes the 003-settlement-fee reference scenario
// (inputs/ plus reconify.yaml) in a temp directory and makes it the working
// directory, so default artifact paths resolve inside it.
func verifyWorkspace(t *testing.T) string {
	t.Helper()
	scenarioDir := filepath.Join(evalsDir, "003-settlement-fee")
	dir := materialize(t, scenarioDir, filepath.Join(scenarioDir, "reference", "reconify.yaml"))
	t.Chdir(dir)
	return dir
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil { // #nosec G703 -- path is under t.TempDir().
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, err := runCLI(t, args...)
	if err != nil {
		t.Fatalf("reconify %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return stdout
}

// writeArtifacts produces result.json (in the given format) and explanation.json.
func writeArtifacts(t *testing.T, dir string, reconcileArgs ...string) {
	t.Helper()
	args := append([]string{"reconcile", "--pair", "left_vs_right", "--out", filepath.Join(dir, "result.json")}, reconcileArgs...)
	mustRun(t, args...)
	explanation := mustRun(t, "explain", filepath.Join(dir, "result.json"))
	if err := os.WriteFile(filepath.Join(dir, "explanation.json"), []byte(explanation), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runVerifyJSON(t *testing.T, args ...string) (schemas.Verification, error) {
	t.Helper()
	stdout, _, err := runCLI(t, append([]string{"verify", "--format", "json"}, args...)...)
	var doc schemas.Verification
	if jsonErr := json.Unmarshal([]byte(stdout), &doc); jsonErr != nil {
		t.Fatalf("verify stdout is not a verification document: %v\n%s", jsonErr, stdout)
	}
	return doc, err
}

func checkByName(t *testing.T, doc schemas.Verification, name string) schemas.VerificationCheck {
	t.Helper()
	for _, check := range doc.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("verification has no %q check: %+v", name, doc.Checks)
	return schemas.VerificationCheck{}
}

func assertStatuses(t *testing.T, doc schemas.Verification, want map[string]string) {
	t.Helper()
	for name, status := range want {
		if got := checkByName(t, doc, name); got.Status != status {
			t.Errorf("%s status = %q (%s), want %q", name, got.Status, got.Message, status)
		}
	}
}

func TestVerifyPassesForEveryResultFormat(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"json", []string{"--format", "json"}},
		{"json-stream", []string{"--format", "json-stream"}},
		{"ndjson", []string{"--format", "ndjson"}},
		{"ndjson exceptions_only", []string{"--format", "ndjson", "--result-mode", "exceptions_only"}},
		{"json summary_only", []string{"--format", "json", "--result-mode", "summary_only"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := verifyWorkspace(t)
			writeArtifacts(t, dir, tc.args...)
			doc, err := runVerifyJSON(t)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if doc.Schema != schemas.VerificationSchemaV1 || !doc.OK || doc.Pair != "left_vs_right" || doc.Config != "reconify.yaml" {
				t.Fatalf("unexpected document header: %+v", doc)
			}
			assertStatuses(t, doc, map[string]string{
				"config_valid":           schemas.VerificationPass,
				"source_check:left":      schemas.VerificationPass,
				"source_check:right":     schemas.VerificationPass,
				"result_present":         schemas.VerificationPass,
				"result_reproducible":    schemas.VerificationPass,
				"explanation_present":    schemas.VerificationPass,
				"explanation_consistent": schemas.VerificationPass,
			})
			if got := checkByName(t, doc, "source_check:left").File; got != "inputs/left.csv" {
				t.Errorf("source_check:left file = %q, want inputs/left.csv", got)
			}
		})
	}
}

func TestVerifySkipsMissingOptionalArtifacts(t *testing.T) {
	verifyWorkspace(t)
	doc, err := runVerifyJSON(t)
	if err != nil {
		t.Fatalf("verify with no artifacts must succeed: %v", err)
	}
	if !doc.OK {
		t.Fatalf("ok = false: %+v", doc)
	}
	assertStatuses(t, doc, map[string]string{
		"config_valid":           schemas.VerificationPass,
		"result_present":         schemas.VerificationSkip,
		"result_reproducible":    schemas.VerificationSkip,
		"explanation_present":    schemas.VerificationSkip,
		"explanation_consistent": schemas.VerificationSkip,
	})
}

func TestVerifyFailsWhenNamedArtifactIsMissing(t *testing.T) {
	verifyWorkspace(t)
	doc, err := runVerifyJSON(t, "--result", "nope.json", "--explanation", "nope-explanation.json")
	if ExitCode(err) != ErrCodeVerification {
		t.Fatalf("exit code = %d, want %d (err=%v)", ExitCode(err), ErrCodeVerification, err)
	}
	if doc.OK {
		t.Fatal("ok = true for missing named artifacts")
	}
	assertStatuses(t, doc, map[string]string{
		"result_present":         schemas.VerificationFail,
		"result_reproducible":    schemas.VerificationSkip,
		"explanation_present":    schemas.VerificationFail,
		"explanation_consistent": schemas.VerificationSkip,
	})
}

func TestVerifyDetectsStaleResult(t *testing.T) {
	dir := verifyWorkspace(t)
	writeArtifacts(t, dir, "--format", "json")
	path := filepath.Join(dir, "result.json")
	var result map[string]any
	data, err := os.ReadFile(path) // #nosec G304 -- t.TempDir test file.
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	summary := result["summary"].(map[string]any)
	summary["matched"] = summary["matched"].(float64) + 2
	if data, err = json.Marshal(result); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, string(data))

	doc, verifyErr := runVerifyJSON(t)
	if ExitCode(verifyErr) != ErrCodeVerification {
		t.Fatalf("exit code = %d, want %d", ExitCode(verifyErr), ErrCodeVerification)
	}
	check := checkByName(t, doc, "result_reproducible")
	if check.Status != schemas.VerificationFail || !strings.Contains(check.Message, "summary.matched is 3 in result.json but 1 on a fresh run") {
		t.Fatalf("result_reproducible = %+v", check)
	}
	if envelope := DiagnosticEnvelope(verifyErr); envelope.Diagnostic.Code != diagnosticCodeVerificationFailed ||
		envelope.Diagnostic.Category != diagnosticCategoryVerification || envelope.Code != "config_error" {
		t.Fatalf("diagnostic = %+v", envelope)
	}
}

func TestVerifyDetectsInconsistentExplanation(t *testing.T) {
	dir := verifyWorkspace(t)
	writeArtifacts(t, dir, "--format", "json")
	path := filepath.Join(dir, "explanation.json")
	data, err := os.ReadFile(path) // #nosec G304 -- t.TempDir test file.
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"matched": 1`, `"matched": 42`, 1)
	if tampered == string(data) {
		t.Fatal("fixture explanation has no matched counter to tamper with")
	}
	writeTestFile(t, path, tampered)
	doc, verifyErr := runVerifyJSON(t)
	if ExitCode(verifyErr) != ErrCodeVerification {
		t.Fatalf("exit code = %d, want %d", ExitCode(verifyErr), ErrCodeVerification)
	}
	check := checkByName(t, doc, "explanation_consistent")
	if check.Status != schemas.VerificationFail || !strings.Contains(check.Message, "summary.matched") {
		t.Fatalf("explanation_consistent = %+v", check)
	}
}

func TestVerifyAcceptsExplanationWithCustomTop(t *testing.T) {
	dir := verifyWorkspace(t)
	writeArtifacts(t, dir, "--format", "json")
	custom := mustRun(t, "explain", filepath.Join(dir, "result.json"), "--top", "1")
	if err := os.WriteFile(filepath.Join(dir, "explanation.json"), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := runVerifyJSON(t)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	assertStatuses(t, doc, map[string]string{"explanation_consistent": schemas.VerificationPass})
}

func TestVerifyFlagsStaleFilePattern(t *testing.T) {
	dir := verifyWorkspace(t)
	writeArtifacts(t, dir, "--format", "json")
	configPath := filepath.Join(dir, "reconify.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 -- t.TempDir test file.
	if err != nil {
		t.Fatal(err)
	}
	stale := strings.Replace(string(data), "inputs/left.csv", "inputs/renamed.csv", 1)
	writeTestFile(t, configPath, stale)
	doc, verifyErr := runVerifyJSON(t)
	if ExitCode(verifyErr) != ErrCodeVerification {
		t.Fatalf("exit code = %d, want %d", ExitCode(verifyErr), ErrCodeVerification)
	}
	assertStatuses(t, doc, map[string]string{
		"config_valid":        schemas.VerificationPass,
		"source_check:left":   schemas.VerificationFail,
		"source_check:right":  schemas.VerificationPass,
		"result_reproducible": schemas.VerificationSkip,
	})
	if check := checkByName(t, doc, "source_check:left"); check.Diagnostic == nil || check.Diagnostic.Code != diagnosticCodeInputUnreadable {
		t.Fatalf("source_check:left diagnostic = %+v", check.Diagnostic)
	}
}

func TestVerifyFlagsSourceMappingMismatch(t *testing.T) {
	dir := verifyWorkspace(t)
	configPath := filepath.Join(dir, "reconify.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 -- t.TempDir test file.
	if err != nil {
		t.Fatal(err)
	}
	wrong := strings.Replace(string(data), "amount_col: amount", "amount_col: total", 1)
	writeTestFile(t, configPath, wrong)
	doc, verifyErr := runVerifyJSON(t)
	if ExitCode(verifyErr) != ErrCodeVerification {
		t.Fatalf("exit code = %d, want %d", ExitCode(verifyErr), ErrCodeVerification)
	}
	check := checkByName(t, doc, "source_check:left")
	if check.Status != schemas.VerificationFail || !strings.Contains(check.Message, `amount_col "total" not found`) {
		t.Fatalf("source_check:left = %+v", check)
	}
	if check.Diagnostic == nil || check.Diagnostic.Code != diagnosticCodeInputMismatch {
		t.Fatalf("diagnostic = %+v", check.Diagnostic)
	}
}

func TestVerifyInvalidConfigExitsWithConfigCode(t *testing.T) {
	dir := verifyWorkspace(t)
	configPath := filepath.Join(dir, "reconify.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 -- t.TempDir test file.
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(data), "date_col: date", `date_col: ""`, 1)
	writeTestFile(t, configPath, broken)
	doc, verifyErr := runVerifyJSON(t)
	if ExitCode(verifyErr) != ErrCodeConfig {
		t.Fatalf("exit code = %d, want %d", ExitCode(verifyErr), ErrCodeConfig)
	}
	check := checkByName(t, doc, "config_valid")
	if check.Status != schemas.VerificationFail || check.Diagnostic == nil || check.Diagnostic.Code != diagnosticCodeConfigInvalid {
		t.Fatalf("config_valid = %+v", check)
	}
	if _, ok := check.Diagnostic.Details["errors"]; !ok {
		t.Fatalf("config_valid diagnostic lacks details.errors: %+v", check.Diagnostic.Details)
	}
	assertStatuses(t, doc, map[string]string{"result_present": schemas.VerificationSkip})
}

func TestVerifyDefaultsSinglePairAndRequiresPairForSeveral(t *testing.T) {
	dir := verifyWorkspace(t)
	doc, err := runVerifyJSON(t)
	if err != nil || doc.Pair != "left_vs_right" {
		t.Fatalf("single pair not defaulted: pair=%q err=%v", doc.Pair, err)
	}

	configPath := filepath.Join(dir, "reconify.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 -- t.TempDir test file.
	if err != nil {
		t.Fatal(err)
	}
	multi := string(data) + "  another_pair:\n    left: left\n    right: right\n"
	writeTestFile(t, configPath, multi)
	_, verifyErr := runVerifyJSON(t)
	if ExitCode(verifyErr) != ErrCodeConfig {
		t.Fatalf("exit code = %d, want %d (err=%v)", ExitCode(verifyErr), ErrCodeConfig, verifyErr)
	}
	pairs := DiagnosticEnvelope(verifyErr).Diagnostic.Details["pairs"]
	if !jsonEqual(pairs, []string{"another_pair", "left_vs_right"}) {
		t.Fatalf("details.pairs = %#v", pairs)
	}
	doc, err = runVerifyJSON(t, "--pair", "left_vs_right")
	if err != nil || !doc.OK {
		t.Fatalf("explicit pair failed: %+v %v", doc, err)
	}
}

func TestVerifyTextFormatAndFormatValidation(t *testing.T) {
	dir := verifyWorkspace(t)
	writeArtifacts(t, dir, "--format", "json")
	stdout, _, err := runCLI(t, "verify", "--format", "text")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	for _, want := range []string{"verify: PASS", "[pass] config_valid", "[pass] source_check:left  inputs/left.csv", "[pass] explanation_consistent"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("text checklist lacks %q:\n%s", want, stdout)
		}
	}
	_, _, err = runCLI(t, "verify", "--format", "yaml")
	var typed *Error
	if !errors.As(err, &typed) || typed.Diagnostic.Code != diagnosticCodeUsageError {
		t.Fatalf("invalid --format error = %v", err)
	}
}

func TestVerifyDefaultFormatFollowsAgentMode(t *testing.T) {
	verifyWorkspace(t)
	// A buffer is not a terminal, so JSON is the default in both profiles.
	for _, args := range [][]string{{"verify"}, {"--agent", "verify"}} {
		stdout, _, err := runCLI(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		var doc schemas.Verification
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("%v default output is not JSON: %v\n%s", args, err, stdout)
		}
	}
}

func TestVerifyRestoresCLIStateAfterFreshRun(t *testing.T) {
	dir := verifyWorkspace(t)
	writeArtifacts(t, dir, "--format", "json")
	root := newRootCmd("test", "test")
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--agent", "verify"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !agentMode || ErrorFormat() != "json" {
		t.Fatalf("verify's fresh run leaked CLI state: agent=%v error-format=%q", agentMode, ErrorFormat())
	}
}
