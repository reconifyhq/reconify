package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
)

// exceptionKey maps a top-level key of a json/json-stream result to the NDJSON
// event type of the exception events it holds. The order is the order used by
// the explain command, and is what list_exceptions reports.
var exceptionKeys = []struct{ key, typ string }{
	{"unmatched_left", "unmatched_left"},
	{"unmatched_right", "unmatched_right"},
	{"amount_diff", "amount_diff"},
	{"timing_diff", "timing_diff"},
	{"duplicates", "duplicate"},
	{"grouped_amount_diff", "grouped_amount_diff"},
	{"grouped_timing_diff", "grouped_timing_diff"},
	{"many_to_many_amount_diff", "many_to_many_amount_diff"},
	{"many_to_many_timing_diff", "many_to_many_timing_diff"},
	{"ambiguous_groups", "ambiguous_group"},
	{"financial_effect_diff", "financial_effect_diff"},
	{"settlement_diff", "settlement_diff"},
}

func exceptionTypeNames() []string {
	names := make([]string, 0, len(exceptionKeys))
	for _, k := range exceptionKeys {
		names = append(names, k.typ)
	}
	return names
}

func exceptionTypeForKey(key string) (string, bool) {
	for _, k := range exceptionKeys {
		if k.key == key {
			return k.typ, true
		}
	}
	return "", false
}

func isExceptionType(typ string) bool {
	for _, k := range exceptionKeys {
		if k.typ == typ {
			return true
		}
	}
	return false
}

// Result file formats reported by scanResult.
const (
	formatJSON   = "json"
	formatNDJSON = "ndjson"
)

// ndjsonPrefix matches the start of an NDJSON result line: an object whose
// first keys are the optional schema id followed by the event type.
var ndjsonPrefix = regexp.MustCompile(`^\s*\{\s*(?:"schema"\s*:\s*"[^"]*"\s*,\s*)?"type"\s*:`)

// scanResult streams a reconciliation result file (json, json-stream, or
// NDJSON) and calls visit for the events a caller can care about without ever
// holding more than one event in memory:
//
//   - NDJSON: every line, as (type, data).
//   - json/json-stream: "run_info", "summary", and "by_source" as single
//     events, plus each element of the exception arrays as (event type, element).
//
// Non-exception arrays (matched, ...) are skipped without being retained.
func scanResult(path string, visit func(typ string, data json.RawMessage) error) (format string, err error) {
	f, err := os.Open(path) // #nosec G304 -- path is an explicit tool argument for a local file.
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	br := bufio.NewReaderSize(f, 64*1024)
	head, peekErr := br.Peek(4096)
	if peekErr != nil && !errors.Is(peekErr, io.EOF) && !errors.Is(peekErr, bufio.ErrBufferFull) {
		return "", peekErr
	}
	if len(head) == 0 {
		return "", errors.New("result file is empty")
	}
	dec := json.NewDecoder(br)
	if ndjsonPrefix.Match(head) {
		return formatNDJSON, scanNDJSON(dec, visit)
	}
	return formatJSON, scanJSONObject(dec, visit)
}

func scanNDJSON(dec *json.Decoder, visit func(string, json.RawMessage) error) error {
	for line := 1; ; line++ {
		var env struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := dec.Decode(&env); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode NDJSON event %d: %w", line, err)
		}
		if env.Type == "" {
			return fmt.Errorf("decode NDJSON event %d: missing event type", line)
		}
		if err := visit(env.Type, env.Data); err != nil {
			return err
		}
	}
}

func scanJSONObject(dec *json.Decoder, visit func(string, json.RawMessage) error) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("decode result: expected a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
		key, _ := keyTok.(string)
		switch key {
		case "summary", "by_source", "run_info":
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return fmt.Errorf("decode %s: %w", key, err)
			}
			if err := visit(key, raw); err != nil {
				return err
			}
			continue
		}
		typ, isException := exceptionTypeForKey(key)
		if err := scanValue(dec, typ, isException, visit); err != nil {
			return fmt.Errorf("decode %s: %w", key, err)
		}
	}
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	return nil
}

// scanValue consumes one value. Arrays are walked element by element; elements
// are reported through visit only for exception arrays.
func scanValue(dec *json.Decoder, typ string, report bool, visit func(string, json.RawMessage) error) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '[' {
		for dec.More() {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return err
			}
			if report {
				if err := visit(typ, raw); err != nil {
					return err
				}
			}
		}
		_, err := dec.Token()
		return err
	}
	// Object value: skip balanced tokens.
	for depth := 1; depth > 0; {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}
