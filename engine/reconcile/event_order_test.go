package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/reconifyhq/reconify/config"
	"github.com/reconifyhq/reconify/engine/index"
	"github.com/reconifyhq/reconify/engine/output"
)

// eventOrderRuns is how many times each execution path is repeated. Go
// randomizes map iteration per range loop, so a path that ranges over a map to
// emit events differs between runs with overwhelming probability at this count.
const eventOrderRuns = 30

type orderFixture struct {
	leftPath, rightPath string
	cfg                 config.ParserCfg
}

// newOrderFixture writes inputs with several duplicate groups and several
// unmatched rows on each side. Names are deliberately not in alphabetical order so
// a sorted-by-key emission cannot pass by accident: the contract is input row order.
func newOrderFixture(t *testing.T, policy config.DuplicatePolicy) orderFixture {
	t.Helper()
	dir := t.TempDir()
	f := orderFixture{
		leftPath:  filepath.Join(dir, "left.csv"),
		rightPath: filepath.Join(dir, "right.csv"),
		cfg:       parityParserCfg(),
	}
	f.cfg.DuplicatePolicy = policy
	writeParityCSV(t, f.leftPath, []string{
		"2026-01-01,100,USD,M1,M1",
		"2026-01-01,200,USD,M2,M2",
		"2026-01-01,10,USD,LD-z1,LDUP-Z",
		"2026-01-01,11,USD,LD-z2,LDUP-Z",
		"2026-01-01,20,USD,LD-m1,LDUP-M",
		"2026-01-01,21,USD,LD-m2,LDUP-M",
		"2026-01-01,30,USD,LD-a1,LDUP-A",
		"2026-01-01,31,USD,LD-a2,LDUP-A",
		"2026-01-01,300,USD,M3,M3",
	})
	writeParityCSV(t, f.rightPath, []string{
		"2026-01-01,100,USD,M1,M1",
		"2026-01-01,1,USD,RD-q1,RDUP-Q",
		"2026-01-01,2,USD,RD-q2,RDUP-Q",
		"2026-01-01,200,USD,M2,M2",
		"2026-01-01,3,USD,RD-c1,RDUP-C",
		"2026-01-01,4,USD,RD-c2,RDUP-C",
		"2026-01-01,5,USD,RD-x1,RDUP-X",
		"2026-01-01,6,USD,RD-x2,RDUP-X",
		"2026-01-01,7,USD,RD-b1,RDUP-B",
		"2026-01-01,8,USD,RD-b2,RDUP-B",
		"2026-01-01,300,USD,M3,M3",
		"2026-01-01,40,USD,U-9,U-9",
		"2026-01-01,41,USD,U-2,U-2",
		"2026-01-01,42,USD,U-7,U-7",
		"2026-01-01,43,USD,U-1,U-1",
		"2026-01-01,44,USD,U-5,U-5",
		"2026-01-01,45,USD,U-3,U-3",
	})
	return f
}

type ndjsonEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func decodeEvents(t *testing.T, ndjson []byte) []ndjsonEvent {
	t.Helper()
	var events []ndjsonEvent
	for _, line := range strings.Split(strings.TrimSpace(string(ndjson)), "\n") {
		var e ndjsonEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		events = append(events, e)
	}
	return events
}

// assertEventOrder checks the documented ordering: duplicate groups by first
// occurrence in their source file, and unmatched rows by input row.
func assertEventOrder(t *testing.T, ndjson []byte) {
	t.Helper()
	var leftDups, rightDups []string
	var unmatchedRight []int
	for _, e := range decodeEvents(t, ndjson) {
		switch e.Type {
		case "duplicate":
			var d struct {
				Source    string `json:"source"`
				Reference string `json:"reference"`
			}
			if err := json.Unmarshal(e.Data, &d); err != nil {
				t.Fatal(err)
			}
			if d.Source == "left" {
				leftDups = append(leftDups, d.Reference)
			} else {
				rightDups = append(rightDups, d.Reference)
			}
		case "unmatched_right":
			var tx struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(e.Data, &tx); err != nil {
				t.Fatal(err)
			}
			row, err := strconv.Atoi(strings.TrimPrefix(tx.ID, "right-"))
			if err != nil {
				t.Fatalf("unexpected id %q", tx.ID)
			}
			unmatchedRight = append(unmatchedRight, row)
		}
	}
	if want := []string{"LDUP-Z", "LDUP-M", "LDUP-A"}; !reflect.DeepEqual(leftDups, want) {
		t.Errorf("left duplicate order = %v, want %v", leftDups, want)
	}
	if want := []string{"RDUP-Q", "RDUP-C", "RDUP-X", "RDUP-B"}; !reflect.DeepEqual(rightDups, want) {
		t.Errorf("right duplicate order = %v, want %v", rightDups, want)
	}
	if len(unmatchedRight) < 4 {
		t.Fatalf("fixture produced %d unmatched right rows, want at least 4", len(unmatchedRight))
	}
	for i := 1; i < len(unmatchedRight); i++ {
		if unmatchedRight[i-1] >= unmatchedRight[i] {
			t.Errorf("unmatched_right rows not in input order: %v", unmatchedRight)
			break
		}
	}
}

// assertStableOutput runs produce repeatedly and requires byte-identical output.
func assertStableOutput(t *testing.T, check bool, produce func(t *testing.T) []byte) {
	t.Helper()
	first := produce(t)
	if check {
		assertEventOrder(t, first)
	}
	for i := 1; i < eventOrderRuns; i++ {
		if got := produce(t); !bytes.Equal(first, got) {
			t.Fatalf("run %d differs from run 0:\nfirst:\n%s\ngot:\n%s", i, first, got)
		}
	}
}

func ndjsonRun(t *testing.T, run func(w ResultWriter) error) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := output.NewResultWriter("ndjson", &buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(w); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEventOrderIsStable_Streaming(t *testing.T) {
	pair := config.Pair{DateWindow: "1d"}
	for _, policy := range []config.DuplicatePolicy{config.DuplicatePolicyFlag, config.DuplicatePolicyKeep, config.DuplicatePolicyLatest} {
		f := newOrderFixture(t, policy)
		backends := map[string]func(t *testing.T) RightIndex{
			"memory": func(*testing.T) RightIndex { return NewMemoryIndex() },
			"disk": func(t *testing.T) RightIndex {
				idx, err := index.NewDiskIndex(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				return idx
			},
		}
		for name, newIndex := range backends {
			t.Run(fmt.Sprintf("%s_%s", policy, name), func(t *testing.T) {
				// Duplicate groups are only reported under the flag policy.
				assertStableOutput(t, policy == config.DuplicatePolicyFlag, func(t *testing.T) []byte {
					idx := newIndex(t)
					defer func() { _ = idx.Close() }()
					return ndjsonRun(t, func(w ResultWriter) error {
						return ReconcileStreaming(context.Background(), "order", "left", "right", f.leftPath, f.rightPath, f.cfg, f.cfg, pair, idx, w, 0)
					})
				})
			})
		}
	}
}

func TestEventOrderIsStable_Partitioned(t *testing.T) {
	f := newOrderFixture(t, config.DuplicatePolicyFlag)
	pair := config.Pair{DateWindow: "1d"}
	for _, workers := range []int{0, 3} {
		t.Run(fmt.Sprintf("workers_%d", workers), func(t *testing.T) {
			spill := t.TempDir()
			assertStableOutput(t, false, func(t *testing.T) []byte {
				return ndjsonRun(t, func(w ResultWriter) error {
					return ReconcilePartitionedWithOptions(context.Background(), "order", "left", "right", f.leftPath, f.rightPath, f.cfg, f.cfg, pair, w, PartitionedOptions{
						Partitions: 3, Workers: workers, QueueCapacity: 1, SpillDir: spill,
					})
				})
			})
		})
	}
}

func TestEventOrderIsStable_MultiSourceStreaming(t *testing.T) {
	f := newOrderFixture(t, config.DuplicatePolicyFlag)
	pair := config.Pair{DateWindow: "1d"}
	assertStableOutput(t, false, func(t *testing.T) []byte {
		first, second := NewMemoryIndex(), NewMemoryIndex()
		defer func() { _ = first.Close(); _ = second.Close() }()
		return ndjsonRun(t, func(w ResultWriter) error {
			return ReconcileStreamingMultiSource(context.Background(), "order", "left", f.leftPath, f.cfg, []CounterpartStream{
				{SourceName: "first", RightPath: f.rightPath, RightCfg: f.cfg, Index: first},
				{SourceName: "second", RightPath: f.rightPath, RightCfg: f.cfg, Index: second},
			}, pair, w, 0)
		})
	})
}

// TestEventOrderIsStable_Batch covers the in-memory path for every event family
// the passes can produce, including ambiguous groups, which were emitted by
// ranging over a map.
func TestEventOrderIsStable_Batch(t *testing.T) {
	dir := t.TempDir()
	leftPath := filepath.Join(dir, "left.csv")
	rightPath := filepath.Join(dir, "right.csv")
	writeParityCSV(t, leftPath, []string{
		"2026-01-01,100,USD,AMBIG-Z,AMBIG-Z",
		"2026-01-01,150,USD,AMBIG-Z,AMBIG-Z",
		"2026-01-01,100,USD,AMBIG-M,AMBIG-M",
		"2026-01-01,150,USD,AMBIG-M,AMBIG-M",
		"2026-01-01,100,USD,AMBIG-A,AMBIG-A",
		"2026-01-01,150,USD,AMBIG-A,AMBIG-A",
		"2026-01-01,300,USD,OK,OK",
	})
	writeParityCSV(t, rightPath, []string{
		"2026-01-01,100,USD,AMBIG-Z,AMBIG-Z",
		"2026-01-01,100,USD,AMBIG-M,AMBIG-M",
		"2026-01-01,100,USD,AMBIG-A,AMBIG-A",
		"2026-01-01,100,USD,OK,OK",
		"2026-01-01,200,USD,OK,OK",
		"2026-01-01,5,USD,R-3,R-3",
		"2026-01-01,6,USD,R-1,R-1",
		"2026-01-01,7,USD,R-2,R-2",
	})
	cfg := parityParserCfg()
	cfg.GroupCol = ""
	for _, passType := range []string{config.PassTypeOneToMany, config.PassTypeManyToMany} {
		t.Run(passType, func(t *testing.T) {
			pair := config.Pair{DateWindow: "1d", Passes: []config.PassConfig{{Type: passType}}}
			left := mustParse(t, "left", leftPath, cfg)
			right := mustParse(t, "right", rightPath, cfg)
			var wantAmbiguous []string
			assertStableOutput(t, false, func(t *testing.T) []byte {
				result, err := Reconcile("order", "left", "right", left, right, pair)
				if err != nil {
					t.Fatal(err)
				}
				if passType == config.PassTypeOneToMany {
					var got []string
					for _, g := range result.AmbiguousGroups {
						got = append(got, g.Reference)
					}
					if wantAmbiguous == nil {
						wantAmbiguous = []string{"AMBIG-Z", "AMBIG-M", "AMBIG-A"}
					}
					if !reflect.DeepEqual(got, wantAmbiguous) {
						t.Fatalf("ambiguous group order = %v, want %v", got, wantAmbiguous)
					}
				}
				return ndjsonRun(t, func(w ResultWriter) error { return WriteResult(w, result) })
			})
		})
	}
}
