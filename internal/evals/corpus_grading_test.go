package evals

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestSemanticGraderDiscriminatesCorpus keeps the classification grader honest
// against the real corpus: every reference config must grade as a pass and
// every counter-example must grade as a fail (or not run at all).
func TestSemanticGraderDiscriminatesCorpus(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the reconify binary")
	}
	binary := filepath.Join(t.TempDir(), "reconify")
	build := exec.Command("go", "build", "-o", binary, "./cmd/reconify") // #nosec G204 -- fixed build of this module.
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build reconify: %v\n%s", err, out)
	}
	items, err := loadScenarios(filepath.Join("..", "..", "evals"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		expected, err := os.ReadFile(filepath.Join(item.Dir, item.ExpectedResult)) // #nosec G304 -- checked-in corpus.
		if err != nil {
			t.Fatal(err)
		}
		run := func(config string) ([]byte, bool) {
			dir := t.TempDir()
			for _, input := range item.Inputs {
				if err := copyFile(filepath.Join(item.Dir, input), filepath.Join(dir, input)); err != nil {
					t.Fatal(err)
				}
			}
			if err := copyFile(config, filepath.Join(dir, "reconify.yaml")); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "reconcile", "--config", "reconify.yaml", "--pair", item.Pair, "--format", "json", "--deterministic", "--out", "result.json") // #nosec G204 -- test binary.
			cmd.Dir = dir
			if cmd.Run() != nil {
				return nil, false
			}
			data, err := os.ReadFile(filepath.Join(dir, "result.json")) // #nosec G304 -- test workspace.
			return data, err == nil
		}
		if actual, ok := run(filepath.Join(item.Dir, "reference", "reconify.yaml")); !ok || !semanticResultEqual(actual, expected) {
			t.Errorf("%s: reference config does not grade as a pass", item.ID)
		}
		counterExamples, _ := filepath.Glob(filepath.Join(item.Dir, "counter_examples", "*.yaml"))
		for _, counter := range counterExamples {
			if actual, ok := run(counter); ok && semanticResultEqual(actual, expected) {
				t.Errorf("%s: counter-example %s grades as a pass", item.ID, filepath.Base(counter))
			}
		}
	}
}
