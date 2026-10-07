package schema

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	engineSchemas "github.com/reconifyhq/reconify/schemas"
)

const diagnosticSchemaID = "urn:reconify:engine:diagnostic:v1"

// diagnosticCategories lists the published diagnostic.category values.
func diagnosticCategories() []any {
	return []any{"config", "input", "inference", "execution", "internal", "usage", "verification"}
}

// GenerateDiagnosticSchema constructs the published schema for structured
// Engine command failures.
func GenerateDiagnosticSchema() (*jsonschema.Schema, error) {
	diagnostic, err := jsonschema.For[engineSchemas.DiagnosticEnvelope](nil)
	if err != nil {
		return nil, fmt.Errorf("reflect diagnostic schema: %w", err)
	}

	diagnostic.ID = ""
	diagnostic.Schema = ""
	diagnostic.Defs = ensureDefs(diagnostic.Defs)
	diagnosticDef := diagnostic
	if diagnostic.Ref != "" {
		diagnosticDef = diagnostic.Defs["DiagnosticEnvelope"]
		if diagnosticDef == nil {
			return nil, fmt.Errorf("reflected diagnostic schema has no DiagnosticEnvelope definition")
		}
	}
	if property := diagnosticDef.Properties["schema"]; property != nil {
		property.Const = jsonschema.Ptr[any](engineSchemas.DiagnosticSchemaV1)
	} else {
		return nil, fmt.Errorf("reflected diagnostic schema has no schema property")
	}
	if property := diagnosticDef.Properties["ok"]; property != nil {
		property.Const = jsonschema.Ptr[any](false)
	} else {
		return nil, fmt.Errorf("reflected diagnostic schema has no ok property")
	}

	diagnosticProperty := diagnosticDef.Properties["diagnostic"]
	if diagnosticProperty == nil {
		return nil, fmt.Errorf("reflected diagnostic schema has no diagnostic property")
	}
	diagnosticPayload := diagnosticProperty
	if diagnosticProperty.Ref != "" {
		name := diagnosticProperty.Ref
		const prefix = "#/$defs/"
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			name = name[len(prefix):]
		}
		diagnosticPayload = diagnostic.Defs[name]
		if diagnosticPayload == nil {
			return nil, fmt.Errorf("reflected diagnostic schema has no %q definition", name)
		}
	}
	if property := diagnosticPayload.Properties["category"]; property != nil {
		property.Enum = diagnosticCategories()
	}
	if property := diagnosticPayload.Properties["suggestions"]; property != nil {
		property.Types = nil
		property.Type = "array"
	}

	return &jsonschema.Schema{
		ID:          diagnosticSchemaID,
		Schema:      draft202012,
		Title:       "Reconify Engine diagnostic envelope v1",
		Description: "Versioned JSON diagnostics emitted by Reconify Engine commands.",
		OneOf: []*jsonschema.Schema{
			diagnostic.CloneSchemas(),
		},
		Defs: diagnostic.Defs,
	}, nil
}

// GenerateDiagnosticSchemaJSON returns the deterministic, pretty-printed
// schema document committed under schemas/.
func GenerateDiagnosticSchemaJSON() ([]byte, error) {
	s, err := GenerateDiagnosticSchema()
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal diagnostic schema: %w", err)
	}
	return append(b, '\n'), nil
}
