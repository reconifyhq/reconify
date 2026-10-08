package evals

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

const promptVersion = "neutral-v1"

// armCandidateHooks is the candidate skills plus the packaged agent hooks.
const armCandidateHooks = "candidate+hooks"

// coreArms are the variants that decide the release verdict.
var coreArms = []string{"candidate", "released", "no-skill"}

// maxPackageEntryBytes bounds a single extracted skills-package entry so a
// malformed or hostile tarball cannot exhaust local disk during a release run.
const maxPackageEntryBytes = 64 << 20

// ReleaseOptions describes a local, reproducible candidate/released/no-skill matrix.
type ReleaseOptions struct {
	CorpusDir, ReconifyPath, BaselineVersion, OutDir string
	Models                                           []string
	Seed                                             int64
	Trials, MaxParallel                              int
	Resume                                           bool
	Timeout                                          time.Duration
	ScenarioIDs                                      []string
	Tags                                             []string
	// ExtraArms adds diagnostic arms beyond candidate/released/no-skill, for
	// example "candidate+hooks". They are reported and compared against
	// "candidate" but never change the release verdict.
	ExtraArms []string
}

// Release packs both skill versions, evaluates identical scenarios, and returns a report.
func Release(ctx context.Context, options ReleaseOptions) (Report, error) {
	if strings.TrimSpace(options.BaselineVersion) == "" {
		return Report{}, errors.New("--baseline-version is required")
	}
	if options.Trials == 0 {
		options.Trials = 3
	}
	if options.MaxParallel == 0 {
		options.MaxParallel = 1
	}
	if options.Seed == 0 {
		options.Seed = time.Now().UnixNano()
	}
	if options.OutDir == "" {
		options.OutDir = filepath.Join(".context", "evals", fmt.Sprintf("release-%d", options.Seed))
	}
	if len(options.Models) == 0 {
		return Report{}, errors.New("qualifying release runs require explicit --model agent=id selections")
	}
	if len(options.Models) != len(supportedAgents()) || !allModelAgents(options.Models) {
		return Report{}, errors.New("qualifying release runs require all four supported agents")
	}
	if options.Trials != 3 {
		return Report{}, errors.New("qualifying release runs require exactly three trials")
	}
	extraArms, err := resolveExtraArms(options.ExtraArms)
	if err != nil {
		return Report{}, err
	}
	if err := os.MkdirAll(options.OutDir, 0o750); err != nil {
		return Report{}, err
	}
	var resumed Report
	if options.Resume {
		if data, readErr := os.ReadFile(filepath.Join(options.OutDir, "report.json")); readErr == nil {
			_ = json.Unmarshal(data, &resumed)
		}
	}
	root, err := os.Getwd()
	if err != nil {
		return Report{}, err
	}
	work, err := os.MkdirTemp("", "reconify-release-")
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	candidateTar, err := npmPack(ctx, root, work)
	if err != nil {
		return Report{}, fmt.Errorf("pack candidate: %w", err)
	}
	baselineTar, err := npmPack(ctx, work, work, "@reconifyhq/skills@"+options.BaselineVersion)
	if err != nil {
		return Report{}, fmt.Errorf("pack baseline: %w", err)
	}
	candidate, err := extractPackage(candidateTar, filepath.Join(work, "candidate"))
	if err != nil {
		return Report{}, err
	}
	baseline, err := extractPackage(baselineTar, filepath.Join(work, "baseline"))
	if err != nil {
		return Report{}, err
	}
	noSkills := filepath.Join(work, "no-skills")
	if err := os.MkdirAll(noSkills, 0o750); err != nil {
		return Report{}, err
	}
	if options.ReconifyPath == "" {
		options.ReconifyPath = filepath.Join(work, "reconify")
		// #nosec G204 -- fixed argv building this repository's own CLI into the temporary work directory.
		cmd := exec.CommandContext(ctx, "go", "build", "-o", options.ReconifyPath, "./cmd/reconify")
		cmd.Dir = root
		if out, e := cmd.CombinedOutput(); e != nil {
			return Report{}, fmt.Errorf("build reconify: %w: %s", e, out)
		}
	}
	variants := []releaseArm{{name: "candidate", skills: filepath.Join(candidate, "skills")}, {name: "released", skills: filepath.Join(baseline, "skills")}, {name: "no-skill", skills: noSkills}}
	for _, name := range extraArms {
		variants = append(variants, releaseArm{name: name, skills: filepath.Join(candidate, "skills"), hooks: true})
	}
	// #nosec G404 -- arm ordering must be reproducible from the recorded seed, not unpredictable.
	rng := rand.New(rand.NewSource(options.Seed))
	rng.Shuffle(len(variants), func(i, j int) { variants[i], variants[j] = variants[j], variants[i] })
	modelArguments := map[string]string{}
	for _, value := range options.Models {
		parts := strings.SplitN(value, "=", 2)
		modelArguments[parts[0]] = parts[1]
	}
	report := Report{Schema: reportSchemaV1, Experiment: &Experiment{Seed: options.Seed, PromptVersion: promptVersion, BaselineVersion: options.BaselineVersion, MaxParallel: options.MaxParallel, CandidateDigest: digest(candidateTar)}}
	for _, variant := range variants {
		resumedVariant := false
		if options.Resume {
			for _, previous := range resumed.Variants {
				if previous.Name == variant.name {
					report.Variants = append(report.Variants, previous)
					resumedVariant = true
					break
				}
			}
		}
		if resumedVariant {
			continue
		}
		v, e := Run(ctx, Options{CorpusDir: options.CorpusDir, SkillsDir: variant.skills, ReconifyPath: options.ReconifyPath, Agents: agentNames(options.Models), Trials: options.Trials, Timeout: options.Timeout, ArtifactDir: filepath.Join(options.OutDir, variant.name), MaxParallel: options.MaxParallel, ModelArguments: modelArguments, ScenarioIDs: options.ScenarioIDs, Tags: options.Tags, Hooks: variant.hooks})
		if e != nil {
			return Report{}, fmt.Errorf("run %s: %w", variant.name, e)
		}
		report.Variants = append(report.Variants, VariantReport{Name: variant.name, Agents: v.Agents})
		if len(report.Skipped) == 0 {
			report.Skipped = v.Skipped
		}
	}
	report.Experiment.CorpusDigest = corpusDigest(options.CorpusDir)
	report.Experiment.ModelArguments = append([]string(nil), options.Models...)
	report.Experiment.OS = runtime.GOOS
	report.Experiment.Arch = runtime.GOARCH
	report.Comparisons, report.Verdict = releaseVerdict(report.Variants, len(scenariosOrZero(options.CorpusDir, options.ScenarioIDs, options.Tags))*len(supportedAgents())*options.Trials)
	if err := WriteReport(report, filepath.Join(options.OutDir, "report.json"), io.Discard); err != nil {
		return Report{}, err
	}
	return report, nil
}

// releaseArm is one cell of the release matrix: a skills tree and whether the
// packaged agent hooks are installed alongside it.
type releaseArm struct {
	name, skills string
	hooks        bool
}

// resolveExtraArms validates diagnostic arm names. Only "candidate+hooks" exists.
func resolveExtraArms(names []string) ([]string, error) {
	var arms []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != armCandidateHooks {
			return nil, fmt.Errorf("unknown extra arm %q (supported: %s)", name, armCandidateHooks)
		}
		if !slices.Contains(arms, name) {
			arms = append(arms, name)
		}
	}
	return arms, nil
}

func scenariosOrZero(corpus string, only, tags []string) []scenario {
	result, err := loadScenarios(corpus, only)
	if err != nil {
		return nil
	}
	return filterByTags(result, tags)
}

func releaseVerdict(variants []VariantReport, expected int) ([]Comparison, *Verdict) {
	metricsByName := map[string]Metrics{}
	for _, variant := range variants {
		var m Metrics
		for _, agent := range variant.Agents {
			for _, scenario := range agent.Scenarios {
				for _, trial := range scenario.Trials {
					m.Total++
					if trial.Classification {
						m.Passed++
					}
				}
			}
		}
		m.PassRate = rate(m.Passed, m.Total)
		metricsByName[variant.Name] = m
	}
	for _, name := range coreArms {
		if _, ok := metricsByName[name]; !ok {
			expected = 0
		}
	}
	if expected == 0 {
		return nil, &Verdict{Status: "inconclusive", Reason: "required variants or scenarios are missing"}
	}
	for _, name := range []string{"candidate", "no-skill"} {
		if metricsByName[name].Total != expected {
			return nil, &Verdict{Status: "inconclusive", Reason: "agents, trials, or artifacts are missing"}
		}
	}
	released := metricsByName["released"]
	comparisons := make([]Comparison, 0, 2+len(variants))
	status, reason := "pass", "release matrix completed"
	for _, name := range []string{"candidate", "no-skill"} {
		current := metricsByName[name]
		delta := current.PassRate - released.PassRate
		comparisons = append(comparisons, Comparison{Variant: name, Against: "released", Passed: current.Passed, Total: current.Total, PassRate: current.PassRate, Delta: delta})
		if delta <= -0.10 {
			status, reason = "fail", fmt.Sprintf("%s regressed by %.1f points versus released", name, -delta*100)
		} else if delta < 0 || (name == "candidate" && delta <= 0.05) {
			if status != "fail" {
				status, reason = "warn", fmt.Sprintf("%s has a small or concentrated delta versus released", name)
			}
		}
	}
	// Extra arms are diagnostic: compared with the candidate, never in the verdict.
	candidate := metricsByName["candidate"]
	var extras []string
	for name := range metricsByName {
		if !slices.Contains(coreArms, name) {
			extras = append(extras, name)
		}
	}
	sort.Strings(extras)
	for _, name := range extras {
		current := metricsByName[name]
		comparisons = append(comparisons, Comparison{Variant: name, Against: "candidate", Passed: current.Passed, Total: current.Total, PassRate: current.PassRate, Delta: current.PassRate - candidate.PassRate})
	}
	return comparisons, &Verdict{Status: status, Reason: reason}
}

func allModelAgents(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		parts := strings.SplitN(value, "=", 2)
		if len(parts) != 2 || !containsAgent(supportedAgents(), Agent(parts[0])) {
			return false
		}
		seen[parts[0]] = true
	}
	return len(seen) == len(supportedAgents())
}
func agentNames(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, strings.SplitN(value, "=", 2)[0])
	}
	sort.Strings(result)
	return result
}

func npmPack(ctx context.Context, dir, destination string, packageArgs ...string) (string, error) {
	args := append([]string{"pack"}, packageArgs...)
	args = append(args, "--json", "--pack-destination", destination)
	cmd := exec.CommandContext(ctx, "npm", args...) // #nosec G204 -- npm subcommand and package name are constructed by this package.
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	var result []struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(out, &result); err != nil || len(result) == 0 {
		return "", fmt.Errorf("npm pack returned invalid metadata")
	}
	return filepath.Join(destination, result[0].Filename), nil
}

func extractPackage(archivePath, destination string) (string, error) {
	file, err := os.Open(archivePath) // #nosec G304 -- archive path is produced by npm pack into the temporary work directory.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	for {
		header, e := reader.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return "", e
		}
		name := filepath.Clean(header.Name)
		if name == "." || strings.HasPrefix(name, "../") || filepath.IsAbs(name) {
			return "", fmt.Errorf("unsafe package entry %q", header.Name)
		}
		target := filepath.Join(destination, name)
		if !strings.HasPrefix(target, destination+string(os.PathSeparator)) {
			return "", fmt.Errorf("unsafe package entry %q", header.Name)
		}
		if header.FileInfo().IsDir() {
			if e := os.MkdirAll(target, 0o750); e != nil {
				return "", e
			}
			continue
		}
		if e := os.MkdirAll(filepath.Dir(target), 0o750); e != nil {
			return "", e
		}
		output, e := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- target is confined to destination by the checks above.
		if e != nil {
			return "", e
		}
		written, e := io.Copy(output, io.LimitReader(reader, maxPackageEntryBytes+1))
		if closeErr := output.Close(); e == nil {
			e = closeErr
		}
		if e != nil {
			return "", e
		}
		if written > maxPackageEntryBytes {
			return "", fmt.Errorf("package entry %q exceeds %d bytes", header.Name, int64(maxPackageEntryBytes))
		}
	}
	return filepath.Join(destination, "package"), nil
}

func digest(path string) string {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an archive this package just wrote.
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func corpusDigest(root string) string {
	if root == "" {
		root = "evals"
	}
	h := sha256.New()
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		data, e := os.ReadFile(path) // #nosec G304 G122 -- digests the local, checked-in corpus fixture tree; no untrusted path source.
		if e == nil {
			_, _ = h.Write([]byte(path))
			_, _ = h.Write(data)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}
