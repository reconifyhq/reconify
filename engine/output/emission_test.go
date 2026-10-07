//nolint:staticcheck // Domain aliases keep package-internal signatures readable.
package output

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/reconifyhq/reconify/config"
	. "github.com/reconifyhq/reconify/engine/domain"
)

// TestWriteResultEvents_SourceSummaryOrderIsStable pins the per-counterpart
// source_summary order, which is derived from the BySource map.
func TestWriteResultEvents_SourceSummaryOrderIsStable(t *testing.T) {
	res := &Result{
		RightSource: "zeta,alpha,mid",
		BySource: map[string]Summary{
			"alpha": {TotalRight: 1}, "mid": {TotalRight: 2}, "zeta": {TotalRight: 3},
			"extra": {TotalRight: 4}, "also": {TotalRight: 5},
		},
	}
	want := []string{"zeta", "alpha", "mid", "also", "extra"}
	for i := 0; i < 30; i++ {
		var buf bytes.Buffer
		w, err := NewResultWriter("ndjson", &buf)
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteResultEvents(w, res, true); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			var event struct {
				Type string `json:"type"`
				Data struct {
					Source string `json:"source"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "source_summary" {
				got = append(got, event.Data.Source)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("source_summary order = %v, want %v", got, want)
		}
	}
}

// TestWrappedJSONWriter_DeterministicIsForwarded is the CLI wiring: the writer is
// wrapped for result-mode filtering before --deterministic is applied, so the
// wrapper has to forward the setter or the flag is silently ignored.
func TestWrappedJSONWriter_DeterministicIsForwarded(t *testing.T) {
	var buf bytes.Buffer
	inner, err := NewResultWriter("json", &buf)
	if err != nil {
		t.Fatal(err)
	}
	obs := &recordingObserver{}
	w := WrapWithResultModeAndWarnings(inner, config.ResultModeAll, "", obs)

	setter, ok := w.(interface{ SetDeterministic(bool) })
	if !ok {
		t.Fatal("wrapped writer does not expose SetDeterministic")
	}
	setter.SetDeterministic(true)
	if meta, ok := w.(interface{ SetMeta(string, string, string) }); ok {
		meta.SetMeta("pair", "left", "right")
	} else {
		t.Fatal("wrapped writer does not expose SetMeta")
	}

	// Written out of order; deterministic mode must sort by numeric row.
	for _, id := range []string{"left-10", "left-2", "left-1"} {
		if err := w.WriteMatch(MatchedPair{Left: Transaction{ID: id}, Right: Transaction{ID: "right-" + id}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"right-12", "right-3"} {
		if err := w.WriteUnmatched(Transaction{ID: id}, "right"); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.WriteSummary(Summary{}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	if len(obs.warnings) != 0 {
		t.Errorf("unexpected warnings for json: %v", obs.warnings)
	}
	out := buf.String()
	for _, banned := range []string{"run_id", "run_info", "timestamp"} {
		if strings.Contains(out, banned) {
			t.Errorf("deterministic output contains %q:\n%s", banned, out)
		}
	}
	var doc struct {
		Matched []struct {
			Left struct {
				ID string `json:"id"`
			} `json:"left"`
		} `json:"matched"`
		UnmatchedRight []struct {
			ID string `json:"id"`
		} `json:"unmatched_right"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	var matched, unmatched []string
	for _, m := range doc.Matched {
		matched = append(matched, m.Left.ID)
	}
	for _, u := range doc.UnmatchedRight {
		unmatched = append(unmatched, u.ID)
	}
	if want := []string{"left-1", "left-2", "left-10"}; !reflect.DeepEqual(matched, want) {
		t.Errorf("matched order = %v, want %v", matched, want)
	}
	if want := []string{"right-3", "right-12"}; !reflect.DeepEqual(unmatched, want) {
		t.Errorf("unmatched_right order = %v, want %v", unmatched, want)
	}
}

// TestWrappedWriter_DeterministicUnsupportedWarns keeps the pre-existing user
// feedback for formats that cannot honor --deterministic, now that the wrapper
// always exposes the setter. The warning goes to the observer, never the output.
func TestWrappedWriter_DeterministicUnsupportedWarns(t *testing.T) {
	for _, format := range []string{"ndjson", "csv", "table", "json-stream"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			inner, err := NewResultWriter(format, &buf)
			if err != nil {
				t.Fatal(err)
			}
			obs := &recordingObserver{}
			w := WrapWithResultModeAndWarnings(inner, config.ResultModeAll, "", obs)
			w.(interface{ SetDeterministic(bool) }).SetDeterministic(true)
			if len(obs.warnings) != 1 || obs.warnings[0].Code != WarningDeterministicUnsupported {
				t.Fatalf("warnings = %v, want one %s", obs.warnings, WarningDeterministicUnsupported)
			}
			if !strings.Contains(obs.warnings[0].Message, "no effect") {
				t.Errorf("message = %q", obs.warnings[0].Message)
			}
			if buf.Len() != 0 {
				t.Errorf("warning leaked into output: %q", buf.String())
			}
		})
	}
}

func TestIDLess_NaturalRowOrder(t *testing.T) {
	if !idLess("left-2", "left-10") || idLess("left-10", "left-2") {
		t.Error("left-2 must sort before left-10")
	}
	if !idLess("left-9", "right-1") {
		t.Error("source name must dominate the row number")
	}
}

type recordingObserver struct{ warnings []Warning }

func (r *recordingObserver) ObserveWarning(w Warning) { r.warnings = append(r.warnings, w) }
