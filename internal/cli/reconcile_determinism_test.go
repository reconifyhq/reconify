package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const determinismConfig = `version: 1
timezone: UTC
sources:
  left:
    file_pattern: inputs/left.csv
    parser:
      type: csv
      date_col: date
      date_layout: "2006-01-02"
      amount_col: amount
      currency_col: currency
      ref_col: reference
      multiplier: 100
  right:
    file_pattern: inputs/right.csv
    parser:
      type: csv
      date_col: date
      date_layout: "2006-01-02"
      amount_col: amount
      currency_col: currency
      ref_col: reference
      multiplier: 100
pairs:
  left_vs_right:
    left: left
    right: right
    date_window: 1d
    amount_tolerance_minor: 0
`

const determinismLeft = `date,amount,currency,reference
2024-04-01,100.00,USD,M-1
2024-04-02,200.00,USD,M-2
2024-04-03,10.00,USD,LD-Z
2024-04-03,11.00,USD,LD-Z
2024-04-03,20.00,USD,LD-A
2024-04-03,21.00,USD,LD-A
`

const determinismRight = `date,amount,currency,reference
2024-04-01,100.00,USD,M-1
2024-04-01,1.00,USD,RD-Q
2024-04-01,2.00,USD,RD-Q
2024-04-02,200.00,USD,M-2
2024-04-01,3.00,USD,RD-C
2024-04-01,4.00,USD,RD-C
2024-04-01,5.00,USD,RD-X
2024-04-01,6.00,USD,RD-X
2024-04-05,40.00,USD,U-9
2024-04-05,41.00,USD,U-2
2024-04-05,42.00,USD,U-7
2024-04-05,43.00,USD,U-1
2024-04-05,44.00,USD,U-5
`

// runDeterminismCLI reconciles the fixture through the real command and returns
// the result file and everything the command wrote to stderr.
func runDeterminismCLI(t *testing.T, dir string, extraArgs ...string) (result, stderr string) {
	t.Helper()
	outPath := filepath.Join(dir, "out.txt")
	cmd := newRootCmd("test", "test")
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{
		"reconcile", "-c", filepath.Join(dir, "reconify.yaml"), "--pair", "left_vs_right",
		"--result-mode", "all", "--out", outPath,
	}, extraArgs...))

	// Warnings are printed straight to os.Stderr, so capture the process stream.
	readErr, writeErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = writeErr
	runErr := cmd.Execute()
	os.Stderr = origStderr
	_ = writeErr.Close()
	var errOut bytes.Buffer
	_, _ = errOut.ReadFrom(readErr)
	_ = readErr.Close()
	if runErr != nil {
		t.Fatalf("reconcile %v: %v\nstderr: %s", extraArgs, runErr, errOut.String())
	}
	data, err := os.ReadFile(outPath) // #nosec G304 -- test-owned temp file.
	if err != nil {
		t.Fatal(err)
	}
	return string(data), errOut.String()
}

func writeDeterminismFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "inputs"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"reconify.yaml":    determinismConfig,
		"inputs/left.csv":  determinismLeft,
		"inputs/right.csv": determinismRight,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestReconcileOutputIsByteStable runs the real command repeatedly and
// requires identical bytes for every format that ships a result stream, over a
// fixture with several duplicate groups and several unmatched right rows.
func TestReconcileOutputIsByteStable(t *testing.T) {
	dir := writeDeterminismFixture(t)
	for _, format := range []string{"json", "ndjson", "csv", "json-stream"} {
		t.Run(format, func(t *testing.T) {
			first, _ := runDeterminismCLI(t, dir, "--format", format, "--deterministic")
			if strings.Count(first, "RD-") < 3 || strings.Count(first, "U-") < 4 {
				t.Fatalf("fixture did not produce the expected duplicate and unmatched rows:\n%s", first)
			}
			for i := 1; i < 20; i++ {
				got, _ := runDeterminismCLI(t, dir, "--format", format, "--deterministic")
				if got != first {
					t.Fatalf("run %d differs from run 0 for --format=%s", i, format)
				}
			}
		})
	}
}

func TestReconcileDeterministicWarnsOnlyWhenIgnored(t *testing.T) {
	dir := writeDeterminismFixture(t)

	out, stderr := runDeterminismCLI(t, dir, "--format", "json", "--deterministic")
	if strings.Contains(stderr, "no effect") {
		t.Errorf("--format=json --deterministic warned that it has no effect: %q", stderr)
	}
	for _, banned := range []string{"run_id", "run_info", "timestamp"} {
		if strings.Contains(out, banned) {
			t.Errorf("deterministic json output contains %q", banned)
		}
	}

	out, stderr = runDeterminismCLI(t, dir, "--format", "ndjson", "--deterministic")
	if !strings.Contains(stderr, "--deterministic has no effect") {
		t.Errorf("--format=ndjson --deterministic should warn; stderr = %q", stderr)
	}
	if strings.Contains(out, "no effect") {
		t.Error("the warning must not appear in the result stream")
	}
}
