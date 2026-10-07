package schema

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/reconifyhq/reconify/schemas"
)

func TestPublishedVerificationSchemaHasNoDrift(t *testing.T) {
	generated, err := GenerateVerificationSchemaJSON()
	if err != nil {
		t.Fatal(err)
	}
	if got := schemas.VerificationV1(); !reflect.DeepEqual(generated, got) {
		t.Fatal("published verification schema drifted; run go generate ./schemas")
	}
}

func resolvePublished(t *testing.T, raw []byte) *jsonschema.Resolved {
	t.Helper()
	var document jsonschema.Schema
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	resolved, err := document.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestPublishedVerificationSchemaValidatesDocuments(t *testing.T) {
	resolved := resolvePublished(t, schemas.VerificationV1())
	validateJSON(t, resolved, schemas.Verification{
		Schema: schemas.VerificationSchemaV1,
		OK:     false,
		Config: "reconify.yaml",
		Pair:   "left_vs_right",
		Checks: []schemas.VerificationCheck{
			{Name: "config_valid", Status: schemas.VerificationPass},
			{Name: "source_check:left", Status: schemas.VerificationPass, File: "left.csv"},
			{Name: "result_reproducible", Status: schemas.VerificationFail, Message: "summary.matched is 3 in result.json but 4 on a fresh run"},
			{Name: "explanation_present", Status: schemas.VerificationSkip, Message: "explanation.json not found"},
			{
				Name: "source_check:right", Status: schemas.VerificationFail, Message: "no files match",
				Diagnostic: &schemas.Diagnostic{
					Code: "INPUT_UNREADABLE", Category: "input", Message: "no files match",
					Details: map[string]any{"exit_code": 2}, Suggestions: []string{"fix"},
				},
			},
		},
	})

	bad := map[string]any{
		"schema": schemas.VerificationSchemaV1, "ok": true, "config": "c",
		"checks": []any{map[string]any{"name": "x", "status": "maybe"}},
	}
	raw, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(instance); err == nil {
		t.Fatal("schema accepted an unknown check status")
	}
}

func TestPublishedDiagnosticSchemaAcceptsUsageAndVerificationCategories(t *testing.T) {
	resolved := resolvePublished(t, schemas.DiagnosticV1())
	for _, category := range []string{"usage", "verification"} {
		validateJSON(t, resolved, schemas.DiagnosticEnvelope{
			Error: "x", Code: "usage_error", OK: false, Schema: schemas.DiagnosticSchemaV1,
			Diagnostic: schemas.Diagnostic{
				Code: "USAGE_ERROR", Category: category, Message: "x",
				Details: map[string]any{}, Suggestions: []string{"y"},
			},
		})
	}
}
