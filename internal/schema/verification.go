package schema

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	engineSchemas "github.com/reconifyhq/reconify/schemas"
)

const verificationSchemaID = "urn:reconify:engine:verification:v1"

// GenerateVerificationSchema constructs the published schema for the
// reconify verify checklist.
func GenerateVerificationSchema() (*jsonschema.Schema, error) {
	verification, err := jsonschema.For[engineSchemas.Verification](nil)
	if err != nil {
		return nil, fmt.Errorf("reflect verification schema: %w", err)
	}
	verification.ID, verification.Schema = "", ""
	verification.Defs = ensureDefs(verification.Defs)
	definition := verification
	if verification.Ref != "" {
		definition = verification.Defs["Verification"]
	}
	if definition == nil || definition.Properties["schema"] == nil {
		return nil, fmt.Errorf("reflected Verification schema has no schema property")
	}
	definition.Properties["schema"].Const = jsonschema.Ptr[any](engineSchemas.VerificationSchemaV1)
	if property := definition.Properties["checks"]; property != nil {
		property.Types = nil
		property.Type = "array"
	}
	checks := definition.Properties["checks"]
	if checks == nil || checks.Items == nil {
		return nil, fmt.Errorf("reflected Verification schema has no checks items")
	}
	check := checks.Items
	if check.Ref != "" {
		check = verification.Defs[strings.TrimPrefix(check.Ref, "#/$defs/")]
	}
	if check == nil || check.Properties["status"] == nil {
		return nil, fmt.Errorf("reflected Verification schema has no check status property")
	}
	check.Properties["status"].Enum = []any{
		engineSchemas.VerificationPass,
		engineSchemas.VerificationFail,
		engineSchemas.VerificationSkip,
	}
	if diagnostic := check.Properties["diagnostic"]; diagnostic != nil {
		diagnostic.Types = nil
		diagnostic.Type = "object"
		if property := diagnostic.Properties["category"]; property != nil {
			property.Enum = diagnosticCategories()
		}
		if property := diagnostic.Properties["suggestions"]; property != nil {
			property.Types = nil
			property.Type = "array"
		}
	}
	return &jsonschema.Schema{
		ID:          verificationSchemaID,
		Schema:      draft202012,
		Title:       "Reconify Engine verification v1",
		Description: "Versioned checklist emitted by reconify verify for the agent workflow deliverables.",
		OneOf:       []*jsonschema.Schema{verification.CloneSchemas()},
		Defs:        verification.Defs,
	}, nil
}

// GenerateVerificationSchemaJSON returns the deterministic schema artifact.
func GenerateVerificationSchemaJSON() ([]byte, error) {
	schema, err := GenerateVerificationSchema()
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal verification schema: %w", err)
	}
	return append(data, '\n'), nil
}
