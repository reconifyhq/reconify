package schemas

import _ "embed"

// VerificationSchemaV1 is the stable identifier emitted by reconify verify.
const VerificationSchemaV1 = "reconify.engine.verification.v1"

// Verification check statuses.
const (
	VerificationPass = "pass"
	VerificationFail = "fail"
	VerificationSkip = "skip"
)

// Verification is the checklist emitted by reconify verify for the agent
// workflow deliverables: reconify.yaml, result.json, and explanation.json.
type Verification struct {
	Schema string `json:"schema"`
	// OK is true when every check passed or was skipped.
	OK bool `json:"ok"`
	// Config is the config path as given to the command.
	Config string `json:"config"`
	// Pair is the pair that was verified. Empty when it could not be resolved.
	Pair   string              `json:"pair,omitempty"`
	Checks []VerificationCheck `json:"checks"`
}

// VerificationCheck is the outcome of one named check. Names are config_valid,
// source_check:<source>, result_present, result_reproducible,
// explanation_present, and explanation_consistent.
type VerificationCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	File    string `json:"file,omitempty"`
	Message string `json:"message,omitempty"`
	// Diagnostic carries the structured diagnostic for a failing check when one exists.
	Diagnostic *Diagnostic `json:"diagnostic,omitempty"`
}

//go:generate go run ../cmd/generate-verification-schema -output reconify.engine.verification.v1.json

// verificationSchemaV1 is the checked-in schema served by the CLI.
//
//go:embed reconify.engine.verification.v1.json
var verificationSchemaV1 []byte

// VerificationV1 returns a copy of the published verification schema document.
func VerificationV1() []byte {
	return append([]byte(nil), verificationSchemaV1...)
}
