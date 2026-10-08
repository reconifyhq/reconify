package mcp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type scanned struct {
	typ  string
	data string
}

func scanAll(t *testing.T, content string) (string, []scanned, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "result")
	writeFile(t, path, content)
	var got []scanned
	format, err := scanResult(path, func(typ string, data json.RawMessage) error {
		got = append(got, scanned{typ, string(data)})
		return nil
	})
	return format, got, err
}

func TestScanResultJSONSkipsCleanMatchesAndKeepsExceptions(t *testing.T) {
	doc := `{
  "schema": "reconify.engine.result.v1",
  "pair": "p",
  "summary": {"matched": 2, "result_mode": "all"},
  "matched": [{"left": {"id": "1"}, "right": {"id": "2"}}, {"left": {"id": "3"}}],
  "unmatched_left": [{"id": "L1"}],
  "unmatched_right": null,
  "duplicates": [{"source": "left", "reference": "R"}],
  "ambiguous_groups": [{"reference": "G"}],
  "unknown_future_object": {"nested": [1, {"a": [2]}]},
  "by_source": {"x": {"matched": 1}}
}`
	format, got, err := scanAll(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if format != formatJSON {
		t.Fatalf("format = %s", format)
	}
	var types []string
	for _, g := range got {
		types = append(types, g.typ)
	}
	want := "summary,unmatched_left,duplicate,ambiguous_group,by_source"
	if strings.Join(types, ",") != want {
		t.Fatalf("events = %v, want %s", types, want)
	}
	if got[1].data != `{"id": "L1"}` {
		t.Fatalf("unmatched_left data = %s", got[1].data)
	}
}

func TestScanResultNDJSON(t *testing.T) {
	lines := []string{
		`{"schema":"reconify.engine.result.v1","type":"index_selection","data":{"backend":"memory"}}`,
		`{"schema":"reconify.engine.result.v1","type":"match","data":{"left":{},"right":{}}}`,
		`{"schema":"reconify.engine.result.v1","type":"amount_diff","data":{"diff_minor":5}}`,
		`{"type":"summary","data":{"matched":1}}`,
	}
	format, got, err := scanAll(t, strings.Join(lines, "\n")+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if format != formatNDJSON || len(got) != 4 {
		t.Fatalf("format = %s events = %v", format, got)
	}
	if got[2].typ != "amount_diff" || got[2].data != `{"diff_minor":5}` {
		t.Fatalf("event = %+v", got[2])
	}
}

func TestScanResultHandlesVeryLongNDJSONLines(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	content := fmt.Sprintf(`{"schema":"s","type":"unmatched_left","data":{"id":"a","name":%q}}`, big) + "\n" +
		`{"schema":"s","type":"summary","data":{"matched":0}}` + "\n"
	_, got, err := scanAll(t, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].typ != "summary" {
		t.Fatalf("events = %d", len(got))
	}
}

func TestScanResultErrors(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"not json":         "hello",
		"truncated json":   `{"summary": {"matched": 1}, "unmatched_left": [{"id": "a"}`,
		"array root":       `[1,2,3]`,
		"ndjson no type":   `{"schema":"s","type":"","data":{}}`,
		"ndjson bad line":  `{"schema":"s","type":"summary","data":{}}` + "\n" + `{"schema":"s","type":`,
		"truncated scalar": `{"summary": {"matched": 1}, "pair": `,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := scanAll(t, content); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestResultFormatDetectionAcceptsSchemalessNDJSON(t *testing.T) {
	format, got, err := scanAll(t, `{"type":"summary","data":{"matched":3}}`+"\n")
	if err != nil || format != formatNDJSON || len(got) != 1 {
		t.Fatalf("format=%s got=%v err=%v", format, got, err)
	}
}
