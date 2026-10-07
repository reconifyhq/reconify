package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/reconifyhq/reconify/config"
	"github.com/reconifyhq/reconify/engine/explain"
	"github.com/reconifyhq/reconify/schemas"
	"github.com/spf13/cobra"
)

const (
	verifyDefaultResult      = "result.json"
	verifyDefaultExplanation = "explanation.json"
	// verifyExplainTop mirrors the default of `reconify explain --top`.
	verifyExplainTop = 10
	// verifySampleRows mirrors the default of `reconify config check-source --rows`.
	verifySampleRows = 10

	verifyFormatJSON = "json"
	verifyFormatText = "text"
)

// verifyOptions carries the resolved flags of one verify invocation.
type verifyOptions struct {
	configPath          string
	pair                string
	resultPath          string
	explanationPath     string
	resultExplicit      bool
	explanationExplicit bool
}

func newVerifyCmd() *cobra.Command {
	opts := verifyOptions{}
	var format string

	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify reconify.yaml, result.json, and explanation.json",
		Long: `Check the deliverables of the agent workflow and print a
reconify.engine.verification.v1 checklist:

  config_valid             reconify.yaml validates and the pair resolves
  source_check:<name>      each source's file_pattern resolves (relative to the
                           config file) and the file matches the source mapping
  result_present          result.json exists and is a readable result
  result_reproducible      a fresh deterministic run of the pair reproduces the
                           summary counters in result.json, whatever --format or
                           --result-mode produced it
  explanation_present      explanation.json exists and is valid JSON
  explanation_consistent   explanation.json equals ` + "`reconify explain result.json`" + `

A missing result.json or explanation.json is skipped unless --result or
--explanation names it, in which case it fails.

Exit codes: 0 when every check passes or is skipped, 2 when the config is
invalid, 5 when any other check fails.

The checklist goes to stdout as JSON (--format json, the default under --agent
and whenever stdout is not a terminal) or as a concise text checklist
(--format text, the default on a terminal).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = args
			flags := cmd.Flags()
			opts.configPath = getConfigPath()
			opts.resultExplicit = flags.Changed("result")
			opts.explanationExplicit = flags.Changed("explanation")
			switch format {
			case "":
				format = defaultVerifyFormat(cmd.OutOrStdout())
			case verifyFormatJSON, verifyFormatText:
			default:
				return usageErr(cmd, fmt.Sprintf("invalid --format %q for verify (valid: json, text)", format), "")
			}

			doc, runErr := runVerify(cmd.Context(), opts)
			if err := writeVerification(cmd.OutOrStdout(), doc, format); err != nil {
				return fmt.Errorf("write verification: %w", err)
			}
			return runErr
		},
	}

	cmd.Flags().StringVar(&opts.pair, "pair", "", "Pair to verify (required when the config defines more than one pair)")
	cmd.Flags().StringVar(&opts.resultPath, "result", verifyDefaultResult, "Reconciliation result to verify; fails when named but missing")
	cmd.Flags().StringVar(&opts.explanationPath, "explanation", verifyDefaultExplanation, "Explanation to verify; fails when named but missing")
	cmd.Flags().StringVar(&format, "format", "", "Checklist format: json or text (default: json under --agent or when stdout is not a terminal, else text)")
	return cmd
}

// defaultVerifyFormat picks json for agents and pipes, text for terminals.
func defaultVerifyFormat(out io.Writer) string {
	if agentMode {
		return verifyFormatJSON
	}
	file, ok := out.(*os.File)
	if !ok {
		return verifyFormatJSON
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return verifyFormatJSON
	}
	return verifyFormatText
}

// verification accumulates checks and tracks which ones failed.
type verification struct {
	doc    schemas.Verification
	failed []string
}

func (v *verification) add(check schemas.VerificationCheck) {
	v.doc.Checks = append(v.doc.Checks, check)
	if check.Status == schemas.VerificationFail {
		v.failed = append(v.failed, check.Name)
	}
}

func (v *verification) anyFailedWithPrefix(prefix string) bool {
	for _, name := range v.failed {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func skipCheck(name, message string) schemas.VerificationCheck {
	return schemas.VerificationCheck{Name: name, Status: schemas.VerificationSkip, Message: message}
}

func failCheck(name, message string, err error) schemas.VerificationCheck {
	check := schemas.VerificationCheck{Name: name, Status: schemas.VerificationFail, Message: message}
	if err != nil {
		diagnostic := DiagnosticEnvelope(err).Diagnostic
		check.Diagnostic = &diagnostic
	}
	return check
}

// runVerify performs every check. It always returns the (possibly partial)
// checklist; the error carries the exit code: configuration problems exit 2,
// any other failing check exits 5.
func runVerify(ctx context.Context, opts verifyOptions) (schemas.Verification, error) {
	v := &verification{doc: schemas.Verification{
		Schema: schemas.VerificationSchemaV1,
		Config: opts.configPath,
		Checks: []schemas.VerificationCheck{},
	}}
	finish := func(err error) (schemas.Verification, error) {
		v.doc.OK = len(v.failed) == 0
		return v.doc, err
	}
	artifactChecks := []string{"result_present", "result_reproducible", "explanation_present", "explanation_consistent"}

	cfg, pairName, configDir, cfgErr := loadVerifyConfig(opts)
	if cfgErr != nil {
		v.add(failCheck("config_valid", cfgErr.Error(), cfgErr))
		for _, name := range artifactChecks {
			v.add(skipCheck(name, "config is invalid"))
		}
		return finish(cfgErr)
	}
	v.doc.Pair = pairName
	v.add(schemas.VerificationCheck{Name: "config_valid", Status: schemas.VerificationPass})

	pair := cfg.Pairs[pairName]
	sourceNames := []string{pair.Left}
	for _, name := range pair.Counterparts() {
		if !containsString(sourceNames, name) {
			sourceNames = append(sourceNames, name)
		}
	}
	for _, name := range sourceNames {
		v.add(checkSource(ctx, cfg, name, configDir))
	}

	result, resultCheck := checkResultPresent(opts)
	v.add(resultCheck)

	switch {
	case result == nil:
		v.add(skipCheck("result_reproducible", "result is not available"))
	case v.anyFailedWithPrefix("source_check:"):
		v.add(skipCheck("result_reproducible", "source checks failed; a fresh run is not possible"))
	default:
		v.add(checkReproducible(ctx, opts, pairName, result))
	}

	explanation, explanationCheck := checkExplanationPresent(opts)
	v.add(explanationCheck)

	switch {
	case explanation == nil:
		v.add(skipCheck("explanation_consistent", "explanation is not available"))
	case result == nil:
		v.add(skipCheck("explanation_consistent", "result is not available"))
	default:
		v.add(checkExplanationConsistent(opts, explanation))
	}

	if len(v.failed) == 0 {
		return finish(nil)
	}
	return finish(verificationErr(
		fmt.Sprintf("verification failed: %s", strings.Join(v.failed, ", ")),
		map[string]any{"failed_checks": v.failed},
	))
}

// loadVerifyConfig loads and validates the config and resolves the pair. Every
// failure is a CONFIG_INVALID diagnostic (exit 2).
func loadVerifyConfig(opts verifyOptions) (cfg *config.Config, pairName, configDir string, err *Error) {
	loaded, loadErr := config.Load(opts.configPath)
	if loadErr != nil {
		return nil, "", "", configErrf("failed to load config: %v", loadErr)
	}
	if errs := loaded.Validate(); len(errs) > 0 {
		return nil, "", "", validationErr(fmt.Sprintf("config validation failed: %v", errs[0]), errs)
	}
	resolved, pairErr := resolvePairName(loaded, opts.pair)
	if pairErr != nil {
		var typed *Error
		if errors.As(pairErr, &typed) {
			return nil, "", "", typed
		}
		return nil, "", "", configErr(pairErr.Error())
	}
	if _, ok := loaded.Pairs[resolved]; !ok {
		names := make([]string, 0, len(loaded.Pairs))
		for name := range loaded.Pairs {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, "", "", configErrDetails(fmt.Sprintf("pair %q not found in config", resolved), map[string]any{"pairs": names})
	}
	abs, absErr := filepath.Abs(opts.configPath)
	if absErr != nil {
		return nil, "", "", configErrf("resolve config path: %v", absErr)
	}
	return loaded, resolved, filepath.Dir(abs), nil
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// checkSource resolves one source's file_pattern the way reconcile does and runs
// the same column and sample-row checks as `config check-source`.
func checkSource(ctx context.Context, cfg *config.Config, name, configDir string) schemas.VerificationCheck {
	checkName := "source_check:" + name
	source := cfg.Sources[name]
	path, err := resolveFile("", source.FilePattern, configDir)
	if err != nil {
		failed := failCheck(checkName, err.Error(), inputErr(ErrCodeConfig, "config_error", err.Error(), diagnosticCodeInputUnreadable))
		failed.File = source.FilePattern
		return failed
	}
	check := schemas.VerificationCheck{Name: checkName, Status: schemas.VerificationPass, File: displayPath(configDir, path)}
	if count := patternMatchCount(source.FilePattern, configDir); count > 1 {
		check.Message = fmt.Sprintf("file_pattern matched %d files; reconcile uses the first", count)
	}

	var failures []string
	logf := func(format string, args ...any) {
		line := strings.TrimSpace(fmt.Sprintf(format, args...))
		if strings.HasPrefix(line, "[x]") {
			failures = append(failures, strings.TrimSpace(strings.TrimPrefix(line, "[x]")))
		}
	}
	if checkErr := runSourceCheck(ctx, name, source, path, verifySampleRows, logf); checkErr != nil {
		check.Status = schemas.VerificationFail
		check.Message = strings.Join(append([]string{checkErr.Error()}, failures...), "; ")
		diagnostic := DiagnosticEnvelope(checkErr).Diagnostic
		check.Diagnostic = &diagnostic
	}
	return check
}

func patternMatchCount(pattern, configDir string) int {
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(configDir, pattern)
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0
	}
	return len(matches)
}

// displayPath reports path relative to the config directory when it lies inside it.
func displayPath(configDir, path string) string {
	rel, err := filepath.Rel(configDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// missingArtifact reports an absent deliverable. A missing file is a skip
// unless the flag named it explicitly.
func missingArtifact(name, path string, explicit bool) schemas.VerificationCheck {
	message := fmt.Sprintf("%s not found", path)
	if explicit {
		return failCheck(name, message, inputErr(ErrCodeConfig, "config_error", message, diagnosticCodeInputUnreadable))
	}
	check := skipCheck(name, message)
	check.File = path
	return check
}

// checkResultPresent returns the parsed explanation of result.json (nil when the
// result is missing or unreadable) alongside the result_present check.
func checkResultPresent(opts verifyOptions) (*schemas.Explanation, schemas.VerificationCheck) {
	const name = "result_present"
	file, err := os.Open(opts.resultPath) // #nosec G304 -- explicit CLI input.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, missingArtifact(name, opts.resultPath, opts.resultExplicit)
		}
		message := fmt.Sprintf("open result %q: %v", opts.resultPath, err)
		return nil, failCheck(name, message, inputErr(ErrCodeConfig, "config_error", message, diagnosticCodeInputUnreadable))
	}
	defer func() { _ = file.Close() }()
	explanation, err := explain.Explain(file, explain.Options{TopN: verifyExplainTop})
	if err != nil {
		message := fmt.Sprintf("%s is not a readable reconciliation result: %v", opts.resultPath, err)
		check := failCheck(name, message, inputErr(ErrCodeConfig, "config_error", message, diagnosticCodeInputMismatch))
		check.File = opts.resultPath
		return nil, check
	}
	return &explanation, schemas.VerificationCheck{Name: name, Status: schemas.VerificationPass, File: opts.resultPath}
}

// checkReproducible reruns the pair deterministically and compares the summary
// counters with those recorded in result.json.
func checkReproducible(ctx context.Context, opts verifyOptions, pairName string, recorded *schemas.Explanation) schemas.VerificationCheck {
	const name = "result_reproducible"
	fresh, err := freshExplanation(ctx, opts.configPath, pairName)
	if err != nil {
		message := fmt.Sprintf("fresh run failed: %v", err)
		return failCheck(name, message, err)
	}
	diffs := summaryDifferences(recorded.Summary, fresh.Summary, opts.resultPath)
	if len(diffs) == 0 {
		return schemas.VerificationCheck{Name: name, Status: schemas.VerificationPass}
	}
	message := diffs[0]
	if len(diffs) > 1 {
		message = fmt.Sprintf("%s (and %d more differing counters)", diffs[0], len(diffs)-1)
	}
	return schemas.VerificationCheck{Name: name, Status: schemas.VerificationFail, Message: message}
}

// freshExplanation runs the pair in-process through the real reconcile command
// (summary-only: counters are order-independent and memory stays flat) and explains its output.
func freshExplanation(ctx context.Context, configPath, pairName string) (schemas.Explanation, error) {
	dir, err := os.MkdirTemp("", "reconify-verify-*")
	if err != nil {
		return schemas.Explanation{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "fresh.json")

	// Building another root command rebinds the package-level flag variables;
	// preserve the caller's values so verify's own error handling is unaffected.
	savedConfig, savedExplicit, savedVerbose, savedAgent, savedFormat := configFile, configExplicit, verbose, agentMode, errorFormat
	defer func() {
		configFile, configExplicit, verbose, agentMode, errorFormat = savedConfig, savedExplicit, savedVerbose, savedAgent, savedFormat
	}()
	root := newRootCmd(cliVersion, "verify")
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"--config", configPath, "reconcile", "--pair", pairName,
		"--format", "json", "--result-mode", "summary_only", "--out", out,
	})
	if err := root.ExecuteContext(ctx); err != nil {
		return schemas.Explanation{}, err
	}
	file, err := os.Open(out) // #nosec G304 -- temp file created above.
	if err != nil {
		return schemas.Explanation{}, fmt.Errorf("open fresh result: %w", err)
	}
	defer func() { _ = file.Close() }()
	return explain.Explain(file, explain.Options{TopN: 0})
}

// summaryDifferences lists the summary counters whose values differ, in JSON
// key order, phrased for the verification message.
func summaryDifferences(recorded, fresh any, resultPath string) []string {
	recordedMap, freshMap := summaryMap(recorded), summaryMap(fresh)
	keys := make([]string, 0, len(recordedMap))
	seen := map[string]bool{}
	for key := range recordedMap {
		keys = append(keys, key)
		seen[key] = true
	}
	for key := range freshMap {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var diffs []string
	for _, key := range keys {
		if !reflect.DeepEqual(recordedMap[key], freshMap[key]) {
			diffs = append(diffs, fmt.Sprintf("summary.%s is %s in %s but %s on a fresh run",
				key, formatJSONValue(recordedMap[key]), resultPath, formatJSONValue(freshMap[key])))
		}
	}
	return diffs
}

// summaryMap converts a summary to its JSON map without the fields that
// describe how a run was emitted rather than what it found.
func summaryMap(summary any) map[string]any {
	data, err := json.Marshal(summary)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{}
	}
	delete(out, "result_mode")
	delete(out, "run_id")
	return out
}

func formatJSONValue(value any) string {
	if value == nil {
		return "absent"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

// checkExplanationPresent returns the decoded explanation document (nil when
// missing or unreadable) alongside the explanation_present check.
func checkExplanationPresent(opts verifyOptions) (any, schemas.VerificationCheck) {
	const name = "explanation_present"
	data, err := os.ReadFile(opts.explanationPath) // #nosec G304 -- explicit CLI input.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, missingArtifact(name, opts.explanationPath, opts.explanationExplicit)
		}
		message := fmt.Sprintf("read explanation %q: %v", opts.explanationPath, err)
		return nil, failCheck(name, message, inputErr(ErrCodeConfig, "config_error", message, diagnosticCodeInputUnreadable))
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		message := fmt.Sprintf("%s is not valid JSON: %v", opts.explanationPath, err)
		check := failCheck(name, message, inputErr(ErrCodeConfig, "config_error", message, diagnosticCodeInputMismatch))
		check.File = opts.explanationPath
		return nil, check
	}
	return document, schemas.VerificationCheck{Name: name, Status: schemas.VerificationPass, File: opts.explanationPath}
}

// checkExplanationConsistent compares explanation.json with the explanation
// `reconify explain` derives from result.json, semantically rather than byte for
// byte. It first uses explain's default --top; if the file was written with a
// different --top it retries with the number of exceptions the file carries.
func checkExplanationConsistent(opts verifyOptions, declared any) schemas.VerificationCheck {
	const name = "explanation_consistent"
	tops := []int{verifyExplainTop}
	if object, ok := declared.(map[string]any); ok {
		if list, ok := object["top_exceptions"].([]any); ok && len(list) != verifyExplainTop {
			tops = append(tops, len(list))
		}
	}
	var lastDiff string
	for _, top := range tops {
		file, err := os.Open(opts.resultPath) // #nosec G304 -- explicit CLI input.
		if err != nil {
			message := fmt.Sprintf("open result %q: %v", opts.resultPath, err)
			return failCheck(name, message, inputErr(ErrCodeConfig, "config_error", message, diagnosticCodeInputUnreadable))
		}
		expected, err := explain.Explain(file, explain.Options{TopN: top})
		_ = file.Close()
		if err != nil {
			message := fmt.Sprintf("explain %q: %v", opts.resultPath, err)
			return failCheck(name, message, inputErr(ErrCodeConfig, "config_error", message, diagnosticCodeInputMismatch))
		}
		data, err := json.Marshal(expected)
		if err != nil {
			return failCheck(name, fmt.Sprintf("encode explanation: %v", err), nil)
		}
		var normalized any
		if err := json.Unmarshal(data, &normalized); err != nil {
			return failCheck(name, fmt.Sprintf("decode explanation: %v", err), nil)
		}
		path, got, want, differs := firstDifference("", declared, normalized)
		if !differs {
			return schemas.VerificationCheck{Name: name, Status: schemas.VerificationPass}
		}
		if lastDiff == "" {
			lastDiff = fmt.Sprintf("%s differs from `reconify explain %s` at %s (%s: %s, explain: %s)",
				opts.explanationPath, opts.resultPath, path, opts.explanationPath, formatJSONValue(got), formatJSONValue(want))
		}
	}
	return schemas.VerificationCheck{Name: name, Status: schemas.VerificationFail, Message: lastDiff}
}

// firstDifference walks two decoded JSON documents and reports the first path
// (object keys in sorted order, array indexes) at which they differ.
func firstDifference(path string, got, want any) (diffPath string, gotValue, wantValue any, differs bool) {
	switch g := got.(type) {
	case map[string]any:
		w, ok := want.(map[string]any)
		if !ok {
			return pathOrRoot(path), got, want, true
		}
		keys := make([]string, 0, len(g)+len(w))
		seen := map[string]bool{}
		for key := range g {
			keys = append(keys, key)
			seen[key] = true
		}
		for key := range w {
			if !seen[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := key
			if path != "" {
				child = path + "." + key
			}
			gv, gok := g[key]
			wv, wok := w[key]
			if gok != wok {
				return child, gv, wv, true
			}
			if p, a, b, d := firstDifference(child, gv, wv); d {
				return p, a, b, true
			}
		}
		return "", nil, nil, false
	case []any:
		w, ok := want.([]any)
		if !ok {
			return pathOrRoot(path), got, want, true
		}
		for i := 0; i < len(g) || i < len(w); i++ {
			child := fmt.Sprintf("%s[%d]", path, i)
			if i >= len(g) || i >= len(w) {
				var gv, wv any
				if i < len(g) {
					gv = g[i]
				}
				if i < len(w) {
					wv = w[i]
				}
				return child, gv, wv, true
			}
			if p, a, b, d := firstDifference(child, g[i], w[i]); d {
				return p, a, b, true
			}
		}
		return "", nil, nil, false
	default:
		if reflect.DeepEqual(got, want) {
			return "", nil, nil, false
		}
		return pathOrRoot(path), got, want, true
	}
}

func pathOrRoot(path string) string {
	if path == "" {
		return "$"
	}
	return path
}

func writeVerification(out io.Writer, doc schemas.Verification, format string) error {
	if format == verifyFormatText {
		_, err := io.WriteString(out, renderVerificationText(doc))
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(doc)
}

func renderVerificationText(doc schemas.Verification) string {
	var b strings.Builder
	verdict := "PASS"
	if !doc.OK {
		verdict = "FAIL"
	}
	header := fmt.Sprintf("verify: %s  %s", verdict, doc.Config)
	if doc.Pair != "" {
		header += "  pair=" + doc.Pair
	}
	b.WriteString(header + "\n")
	for _, check := range doc.Checks {
		line := fmt.Sprintf("  [%s] %s", check.Status, check.Name)
		if check.File != "" {
			line += "  " + check.File
		}
		if check.Message != "" {
			line += "  - " + check.Message
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
