package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reconifyhq/reconify/schemas"
)

func writeBrokenConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reconify.yaml")
	body := `version: 1
sources:
  left:
    file_pattern: left.csv
    parser:
      type: csv
      date_layout: "2006-01-02"
      amount_col: amount
      multiplier: 0
  right:
    file_pattern: right.csv
    parser:
      type: csv
      date_col: date
      date_layout: "2006-01-02"
      amount_col: amount
      multiplier: 100
pairs:
  p:
    left: left
    right: right
`
	writeTestFile(t, path, body)
	return path
}

type validationDetails struct {
	Errors []struct {
		Path    string `json:"path"`
		Message string `json:"message"`
	} `json:"errors"`
}

func decodeDetails(t *testing.T, err error, into any) schemas.Diagnostic {
	t.Helper()
	envelope := DiagnosticEnvelope(err)
	raw, marshalErr := json.Marshal(envelope.Diagnostic.Details)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if unmarshalErr := json.Unmarshal(raw, into); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	return envelope.Diagnostic
}

func TestConfigValidateCarriesStructuredErrors(t *testing.T) {
	path := writeBrokenConfig(t)
	_, stderr, err := runCLI(t, "--agent", "config", "validate", "-c", path)
	if err == nil {
		t.Fatal("expected validation failure")
	}
	if ExitCode(err) != ErrCodeConfig || LegacyErrorCode(err) != "config_error" {
		t.Fatalf("compatibility fields = (%d, %q)", ExitCode(err), LegacyErrorCode(err))
	}
	var details validationDetails
	diagnostic := decodeDetails(t, err, &details)
	if diagnostic.Code != diagnosticCodeConfigInvalid {
		t.Fatalf("diagnostic code = %q", diagnostic.Code)
	}
	want := map[string]string{
		"sources.left.parser.date_col":   "required field is missing",
		"sources.left.parser.multiplier": "must be > 0 (got 0)",
	}
	if len(details.Errors) != len(want) {
		t.Fatalf("details.errors = %+v, want %d entries", details.Errors, len(want))
	}
	for _, entry := range details.Errors {
		if want[entry.Path] != entry.Message {
			t.Errorf("unexpected entry %+v", entry)
		}
	}
	if strings.Contains(stderr, "is invalid") || strings.Contains(stderr, "  - ") {
		t.Errorf("--agent must suppress the human listing, got stderr:\n%s", stderr)
	}
}

func TestConfigValidateKeepsHumanListingInTextMode(t *testing.T) {
	path := writeBrokenConfig(t)
	_, stderr, err := runCLI(t, "config", "validate", "-c", path)
	if err == nil {
		t.Fatal("expected validation failure")
	}
	if err.Error() != "validation failed" {
		t.Fatalf("message changed: %q", err.Error())
	}
	for _, want := range []string{"is invalid:", "  - sources.left.parser.date_col: required field is missing"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("text-mode listing lacks %q:\n%s", want, stderr)
		}
	}
	// Structured details are present in text mode too; only the stderr listing differs.
	var details validationDetails
	decodeDetails(t, err, &details)
	if len(details.Errors) != 2 {
		t.Fatalf("details.errors = %+v", details.Errors)
	}

	_, stderr, err = runCLI(t, "config", "validate", "--error-format", "json", "-c", path)
	if err == nil || strings.Contains(stderr, "is invalid") {
		t.Fatalf("--error-format json must suppress the listing (err=%v):\n%s", err, stderr)
	}
}

func TestOtherCommandsAttachStructuredValidationErrors(t *testing.T) {
	path := writeBrokenConfig(t)
	for _, args := range [][]string{
		{"reconcile", "-c", path, "--pair", "p"},
		{"parse", "-c", path, "--source", "left", "--file", path},
	} {
		_, _, err := runCLI(t, args...)
		if err == nil {
			t.Fatalf("%v: expected failure", args)
		}
		var details validationDetails
		diagnostic := decodeDetails(t, err, &details)
		if diagnostic.Code != diagnosticCodeConfigInvalid || len(details.Errors) != 2 || details.Errors[0].Path == "" {
			t.Errorf("%v: diagnostic = %+v details = %+v", args, diagnostic, details)
		}
		if !strings.HasPrefix(err.Error(), "config validation failed: ") {
			t.Errorf("%v: message changed: %q", args, err.Error())
		}
	}
}

func TestUsageErrors(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantMessage string
		wantUsage   string
		wantDidYou  string
	}{
		{
			name: "unknown command", args: []string{"confg", "validate"},
			wantMessage: `unknown command "confg" for "reconify"`, wantUsage: "reconify [command]", wantDidYou: "reconify config",
		},
		{
			name: "unknown subcommand", args: []string{"config", "valdate"},
			wantMessage: `unknown command "valdate" for "reconify config"`, wantUsage: "reconify config [command]", wantDidYou: "reconify config validate",
		},
		{
			name: "unknown flag", args: []string{"reconcile", "--bogus"},
			wantMessage: "unknown flag: --bogus", wantUsage: "reconify reconcile [LEFT RIGHT] [flags]",
		},
		{
			name: "unknown flag with suggestion", args: []string{"reconcile", "--formt", "json"},
			wantMessage: "unknown flag: --formt", wantUsage: "reconify reconcile [LEFT RIGHT] [flags]", wantDidYou: "--format",
		},
		{
			name: "unknown flag on explain", args: []string{"explain", "result.json", "--tpo", "3"},
			wantMessage: "unknown flag: --tpo", wantUsage: "reconify explain FILE [flags]", wantDidYou: "--top",
		},
		{
			name: "missing argument", args: []string{"explain"},
			wantMessage: "reconify explain: accepts 1 arg(s), received 0", wantUsage: "reconify explain FILE [flags]",
		},
		{
			name: "too many arguments", args: []string{"explain", "a", "b"},
			wantMessage: "reconify explain: accepts 1 arg(s), received 2", wantUsage: "reconify explain FILE [flags]",
		},
		{
			name: "unexpected argument", args: []string{"config", "validate", "extra"},
			wantMessage: `reconify config validate takes no positional arguments (got "extra")`, wantUsage: "reconify config validate [flags]",
		},
		{
			name: "invalid flag value", args: []string{"explain", "result.json", "--top", "many"},
			wantMessage: `invalid argument "many" for "--top" flag`, wantUsage: "reconify explain FILE [flags]",
		},
		{
			name: "flag needs value", args: []string{"explain", "result.json", "--top"},
			wantMessage: "flag needs an argument: --top", wantUsage: "reconify explain FILE [flags]",
		},
		{
			name: "unknown shorthand", args: []string{"reconcile", "-z"},
			wantMessage: "unknown shorthand flag: 'z' in -z", wantUsage: "reconify reconcile [LEFT RIGHT] [flags]",
		},
		{
			name: "unknown schema", args: []string{"schema", "verificaton"},
			wantMessage: `unknown command "verificaton" for "reconify schema"`, wantUsage: "reconify schema [command]", wantDidYou: "reconify schema verification",
		},
		{
			name: "missing required parse flag", args: []string{"parse"},
			wantMessage: "--source is required", wantUsage: "reconify parse [flags]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runCLI(t, tc.args...)
			if err == nil {
				t.Fatal("expected a usage error")
			}
			if ExitCode(err) != 2 || LegacyErrorCode(err) != "usage_error" {
				t.Fatalf("compatibility fields = (%d, %q), want (2, usage_error)", ExitCode(err), LegacyErrorCode(err))
			}
			envelope := DiagnosticEnvelope(err)
			diagnostic := envelope.Diagnostic
			if diagnostic.Code != diagnosticCodeUsageError || diagnostic.Category != diagnosticCategoryUsage {
				t.Fatalf("diagnostic = %+v", diagnostic)
			}
			if !strings.Contains(diagnostic.Message, tc.wantMessage) {
				t.Errorf("message = %q, want it to contain %q", diagnostic.Message, tc.wantMessage)
			}
			if got := diagnostic.Details["usage"]; got != tc.wantUsage {
				t.Errorf("details.usage = %v, want %q", got, tc.wantUsage)
			}
			got, hasSuggestion := diagnostic.Details["did_you_mean"]
			switch {
			case tc.wantDidYou == "" && hasSuggestion:
				t.Errorf("unexpected did_you_mean %v", got)
			case tc.wantDidYou != "" && got != tc.wantDidYou:
				t.Errorf("did_you_mean = %v, want %q", got, tc.wantDidYou)
			}
			if len(diagnostic.Suggestions) != 1 || !strings.Contains(diagnostic.Suggestions[0], "reconify ") ||
				!strings.Contains(diagnostic.Suggestions[0], "--help") {
				t.Errorf("suggestions = %v, want one naming `reconify <command> --help`", diagnostic.Suggestions)
			}
		})
	}
}

func TestUsageErrorNamesTheFailingCommandInItsSuggestion(t *testing.T) {
	_, _, err := runCLI(t, "config", "infer", "only-one")
	envelope := DiagnosticEnvelope(err)
	if envelope.Diagnostic.Code != diagnosticCodeUsageError ||
		envelope.Diagnostic.Suggestions[0] != "Run `reconify config infer --help` to see valid usage." {
		t.Fatalf("diagnostic = %+v", envelope.Diagnostic)
	}
}

func TestUsageErrorMarshalsAsDiagnosticEnvelope(t *testing.T) {
	_, _, err := runCLI(t, "--agent", "reconcile", "--bogus")
	if err == nil {
		t.Fatal("expected usage error")
	}
	payload, marshalErr := MarshalDiagnosticEnvelope(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var envelope schemas.DiagnosticEnvelope
	if unmarshalErr := json.Unmarshal(payload, &envelope); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if envelope.Diagnostic.Code != diagnosticCodeUsageError || envelope.Code != "usage_error" {
		t.Fatalf("envelope = %+v", envelope)
	}
}

func TestTextErrorShowsDidYouMean(t *testing.T) {
	_, _, err := runCLI(t, "reconcile", "--formt", "json")
	text := TextError(err)
	if !strings.HasPrefix(text, "Error: unknown flag: --formt") || !strings.Contains(text, "Did you mean `--format`?") {
		t.Fatalf("TextError = %q", text)
	}
	if got := TextError(errors.New("plain")); got != "Error: plain" {
		t.Fatalf("TextError(plain) = %q", got)
	}
}

func TestBareGroupCommandsStillPrintHelp(t *testing.T) {
	for _, args := range [][]string{{}, {"config"}, {"schema"}} {
		stdout, _, err := runCLI(t, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(stdout, "Available Commands:") {
			t.Errorf("%v: expected help output, got:\n%s", args, stdout)
		}
	}
}

func TestConfigInferAcceptsPositionalFiles(t *testing.T) {
	left, right := writeInferInput(t, "reference"), writeInferInput(t, "reference")
	positional, _, err := runCLI(t, "config", "infer", left, right)
	if err != nil {
		t.Fatalf("positional infer: %v", err)
	}
	flagged, _, err := runCLI(t, "config", "infer", "--left", left, "--right", right)
	if err != nil {
		t.Fatalf("flag infer: %v", err)
	}
	if positional != flagged {
		t.Fatalf("positional and flag forms differ:\n%s\n%s", positional, flagged)
	}
	var proposal schemas.ConfigProposal
	if err := json.Unmarshal([]byte(positional), &proposal); err != nil || proposal.Status != "ready" {
		t.Fatalf("proposal = %+v (%v)", proposal, err)
	}
}

func TestConfigInferRejectsMixedAndMiscountedInputs(t *testing.T) {
	left, right := writeInferInput(t, "reference"), writeInferInput(t, "reference")
	cases := map[string][]string{
		"positional with --left":  {"config", "infer", left, right, "--left", left},
		"positional with --right": {"config", "infer", left, right, "--right", right},
		"one positional":          {"config", "infer", left},
		"three positionals":       {"config", "infer", left, right, left},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := runCLI(t, args...)
			if err == nil {
				t.Fatal("expected usage error")
			}
			diagnostic := DiagnosticEnvelope(err).Diagnostic
			if diagnostic.Code != diagnosticCodeUsageError || ExitCode(err) != 2 {
				t.Fatalf("diagnostic = %+v exit=%d", diagnostic, ExitCode(err))
			}
		})
	}
}

func TestReconcileDefaultsTheOnlyPair(t *testing.T) {
	dir := verifyWorkspace(t)
	out := filepath.Join(dir, "result.json")
	if _, stderr, err := runCLI(t, "reconcile", "--out", out); err != nil {
		t.Fatalf("reconcile without --pair: %v\n%s", err, stderr)
	}
	data, err := os.ReadFile(out) // #nosec G304 -- t.TempDir test file.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"pair": "left_vs_right"`) && !strings.Contains(string(data), `"matched"`) {
		t.Fatalf("unexpected result: %s", data)
	}
}

func TestReconcileRequiresPairWhenSeveralAreConfigured(t *testing.T) {
	dir := verifyWorkspace(t)
	configPath := filepath.Join(dir, "reconify.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 -- t.TempDir test file.
	if err != nil {
		t.Fatal(err)
	}
	multi := string(data) + "  b_pair:\n    left: left\n    right: right\n"
	writeTestFile(t, configPath, multi)
	_, _, err = runCLI(t, "reconcile")
	if err == nil {
		t.Fatal("expected --pair error")
	}
	if ExitCode(err) != ErrCodeConfig {
		t.Fatalf("exit code = %d", ExitCode(err))
	}
	var details struct {
		Pairs []string `json:"pairs"`
	}
	diagnostic := decodeDetails(t, err, &details)
	if diagnostic.Code != diagnosticCodeConfigInvalid || strings.Join(details.Pairs, ",") != "b_pair,left_vs_right" {
		t.Fatalf("diagnostic = %+v pairs = %v", diagnostic, details.Pairs)
	}
	if !strings.Contains(err.Error(), "--pair is required") {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestPairFlagHelpIsAccurate(t *testing.T) {
	root := newRootCmd("test", "test")
	for _, name := range []string{"reconcile", "verify"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		usage := cmd.Flags().Lookup("pair").Usage
		if !strings.Contains(usage, "required when the config defines more than one pair") {
			t.Errorf("%s --pair usage = %q", name, usage)
		}
	}
}
