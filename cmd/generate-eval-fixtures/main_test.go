package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// corpusDir is the committed evaluation corpus, relative to this package.
const corpusDir = "../../evals"

// maxFixtureBytes keeps generated inputs small enough to review in a diff.
const maxFixtureBytes = 20 * 1024

// TestGeneratedFixturesMatchCommittedCorpus regenerates every fixture into a
// temporary directory and requires it to be byte-identical to the committed
// file, so neither a hand edit nor a generator change can drift unnoticed.
func TestGeneratedFixturesMatchCommittedCorpus(t *testing.T) {
	out := t.TempDir()
	if err := writeFixtures(out); err != nil {
		t.Fatalf("writeFixtures: %v", err)
	}
	files, err := fixtures()
	if err != nil {
		t.Fatalf("fixtures: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("generator produced no fixtures")
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		t.Run(rel, func(t *testing.T) {
			regenerated, err := os.ReadFile(filepath.Join(out, rel)) // #nosec G304 -- test-owned temp dir.
			if err != nil {
				t.Fatalf("read regenerated fixture: %v", err)
			}
			committed, err := os.ReadFile(filepath.Join(corpusDir, rel)) // #nosec G304 -- checked-in corpus fixture.
			if err != nil {
				t.Fatalf("read committed fixture (run `go run ./cmd/generate-eval-fixtures -corpus evals`): %v", err)
			}
			if !bytes.Equal(regenerated, committed) {
				t.Errorf("%s drifted from the generator; run `go run ./cmd/generate-eval-fixtures -corpus evals` and review the diff", rel)
			}
			if len(committed) > maxFixtureBytes {
				t.Errorf("%s is %d bytes, over the %d byte fixture budget", rel, len(committed), maxFixtureBytes)
			}
		})
	}
}

// TestFixturesAreDeterministic guards against hidden nondeterminism such as map
// iteration order or an unseeded random source.
func TestFixturesAreDeterministic(t *testing.T) {
	first, err := fixtures()
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixtures()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("fixture count changed between runs: %d vs %d", len(first), len(second))
	}
	for path, data := range first {
		if !bytes.Equal(data, second[path]) {
			t.Errorf("%s differs between two generator runs", path)
		}
	}
}
