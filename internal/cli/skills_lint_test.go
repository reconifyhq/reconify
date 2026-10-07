package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Skill/CLI drift lint.
//
// The skills under skills/ are documentation for agents that hold an installed
// reconify binary. Every `reconify ...` command they show must resolve against
// the real Cobra command tree: each subcommand must exist and every --flag must
// be defined on that command (local, persistent, or inherited). A renamed flag
// or command then fails `go test` instead of silently misleading an agent.
//
// Fenced code blocks and inline code spans are checked. Placeholders (ALLCAPS
// words and <angle> words) are skipped, and everything after a shell pipe,
// redirection, `&&`, or `;` belongs to another command.

var (
	skillPlaceholderRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	skillAngleRe       = regexp.MustCompile(`<[A-Za-z0-9_.\-/]+>`)
	skillInlineCodeRe  = regexp.MustCompile("`([^`\n]+)`")
	skillNumberRe      = regexp.MustCompile(`^-[0-9.]+$`)
	skillAssignmentRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// skillInvocation is one `reconify ...` command found in a markdown file.
type skillInvocation struct {
	line  int
	words []string // everything after the leading "reconify"
}

func TestSkillsLint(t *testing.T) {
	skillsDir := filepath.Join("..", "..", "skills")
	var files []string
	for _, dir := range []string{".agents", ".claude", ".codex"} {
		matches, err := filepath.Glob(filepath.Join(skillsDir, dir, "*", "SKILL.md"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		files = append(files, matches...)
	}
	if len(files) < 7 {
		t.Fatalf("found only %d SKILL.md files under %s; the lint must not pass vacuously", len(files), skillsDir)
	}

	root := newRootCmd("test", "test")
	checked := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, inv := range extractSkillInvocations(string(data)) {
			checked++
			for _, problem := range checkSkillInvocation(root, inv.words) {
				t.Errorf("%s:%d: %s\n\tcommand: reconify %s", file, inv.line, problem, strings.Join(inv.words, " "))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no reconify commands found in skills; the extractor is broken")
	}
}

func TestSkillsLintDetectsDrift(t *testing.T) {
	root := newRootCmd("test", "test")
	doc := "```bash\n" +
		"reconify config validate --config reconify.yaml\n" + // ok
		"reconify --agent config validate --config reconify.yaml\n" + // ok: persistent flags before the subcommand
		"reconify reconcile --config reconify.yaml --pair PAIR \\\n" + // ok: continuation joined
		"  --format json --out result.json\n" +
		"reconify explain result.json > explanation.json\n" + // ok: redirection ignored
		"reconify inspect <FILE> | head\n" + // ok: pipe ignored
		"reconify config validate --bogus\n" + // line 8: unknown flag
		"reconify reconcile --pair PAIR \\\n" + // line 9: unknown flag on a continued line
		"  --no-such-flag x\n" +
		"reconify config frobnicate\n" + // line 11: unknown subcommand
		"reconify frobnicate --help\n" + // line 12: unknown command
		"```\n" +
		"Prose with `reconify config validate --nope` inline.\n" // line 14: inline span
	got := map[int]string{}
	for _, inv := range extractSkillInvocations(doc) {
		for _, problem := range checkSkillInvocation(root, inv.words) {
			got[inv.line] = problem
		}
	}
	want := map[int]string{
		8:  "unknown flag --bogus",
		9:  "unknown flag --no-such-flag",
		11: "unknown subcommand",
		12: "unknown subcommand",
		14: "unknown flag --nope",
	}
	for line, substr := range want {
		if !strings.Contains(got[line], substr) {
			t.Errorf("line %d: got problem %q, want it to contain %q", line, got[line], substr)
		}
	}
	for line, problem := range got {
		if _, ok := want[line]; !ok {
			t.Errorf("line %d: unexpected problem %q", line, problem)
		}
	}
}

// extractSkillInvocations returns every `reconify ...` command found in code
// fences and inline code spans of a markdown document.
func extractSkillInvocations(markdown string) []skillInvocation {
	var out []skillInvocation
	lines := strings.Split(markdown, "\n")
	inFence := false
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			for _, m := range skillInlineCodeRe.FindAllStringSubmatch(lines[i], -1) {
				out = append(out, reconifyInvocations(i+1, m[1])...)
			}
			continue
		}
		// Join shell continuations; the invocation is reported at its first line.
		start := i
		logical := trimmed
		for strings.HasSuffix(logical, `\`) && i+1 < len(lines) {
			i++
			logical = strings.TrimSuffix(logical, `\`) + " " + strings.TrimSpace(lines[i])
		}
		out = append(out, reconifyInvocations(start+1, logical)...)
	}
	return out
}

// reconifyInvocations splits a shell line into command segments and returns the
// ones that invoke reconify.
func reconifyInvocations(line int, text string) []skillInvocation {
	text = strings.TrimPrefix(strings.TrimSpace(text), "$ ")
	text = skillAngleRe.ReplaceAllString(text, "PLACEHOLDER")
	var out []skillInvocation
	for _, seg := range splitShellSegments(text) {
		for len(seg) > 0 && skillAssignmentRe.MatchString(seg[0]) {
			seg = seg[1:]
		}
		if len(seg) > 0 && seg[0] == "reconify" {
			out = append(out, skillInvocation{line: line, words: seg[1:]})
		}
	}
	return out
}

// splitShellSegments tokenizes a shell line (quotes honored) into command
// segments. Segments end at |, ||, &&, &, or ;. A redirection or comment ends
// the words of its segment.
func splitShellSegments(text string) [][]string {
	var segments [][]string
	var words []string
	var cur strings.Builder
	inWord := false
	truncated := false
	var quote rune

	flushWord := func() {
		if inWord && !truncated {
			words = append(words, cur.String())
		}
		cur.Reset()
		inWord = false
	}
	endSegment := func() {
		flushWord()
		if len(words) > 0 {
			segments = append(segments, words)
		}
		words = nil
		truncated = false
	}

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			inWord = true
		case r == ' ' || r == '\t':
			flushWord()
		case r == '#' && !inWord:
			flushWord()
			truncated = true
			i = len(runes)
		case r == '|' || r == ';' || r == '&':
			endSegment()
		case r == '>' || r == '<':
			flushWord()
			truncated = true
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	endSegment()
	return segments
}

// checkSkillInvocation resolves the command path of `reconify <words...>` against
// root and returns one message per problem found.
func checkSkillInvocation(root *cobra.Command, words []string) []string {
	var problems []string
	cmd := root
	prepareSkillLintCommand(cmd)
	for i := 0; i < len(words); i++ {
		w := strings.Trim(words[i], "[]")
		switch {
		case w == "":
			continue
		case w == "--":
			return problems
		case strings.HasPrefix(w, "--"):
			name, _, hasValue := strings.Cut(w[2:], "=")
			flag := lookupSkillLintFlag(cmd, name, false)
			if flag == nil {
				if !skillPlaceholderRe.MatchString(name) {
					problems = append(problems, fmt.Sprintf("unknown flag --%s for `%s`", name, cmd.CommandPath()))
				}
				continue
			}
			if !hasValue && flag.NoOptDefVal == "" {
				i++ // the next word is this flag's value
			}
		case strings.HasPrefix(w, "-") && len(w) > 1 && !skillNumberRe.MatchString(w):
			flag := lookupSkillLintFlag(cmd, w[1:2], true)
			if flag == nil {
				problems = append(problems, fmt.Sprintf("unknown flag -%s for `%s`", w[1:2], cmd.CommandPath()))
				continue
			}
			if len(w) == 2 && flag.NoOptDefVal == "" {
				i++
			}
		case skillPlaceholderRe.MatchString(w):
			continue
		default:
			if child := findSkillLintChild(cmd, w); child != nil {
				cmd = child
				prepareSkillLintCommand(cmd)
				continue
			}
			if cmd.HasSubCommands() {
				problems = append(problems, fmt.Sprintf("unknown subcommand %q for `%s`", w, cmd.CommandPath()))
				return problems
			}
			// Otherwise w is a positional argument of a leaf command.
		}
	}
	return problems
}

func prepareSkillLintCommand(cmd *cobra.Command) {
	cmd.InitDefaultHelpFlag()
	if !cmd.HasParent() {
		cmd.InitDefaultHelpCmd()
		cmd.InitDefaultVersionFlag()
	}
}

func findSkillLintChild(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return c
		}
	}
	return nil
}

// lookupSkillLintFlag finds a flag by name (or shorthand) among the command's
// own flags and the persistent flags of its ancestors.
func lookupSkillLintFlag(cmd *cobra.Command, name string, shorthand bool) *pflag.Flag {
	find := func(fs *pflag.FlagSet) *pflag.Flag {
		if shorthand {
			return fs.ShorthandLookup(name)
		}
		return fs.Lookup(name)
	}
	if f := find(cmd.Flags()); f != nil {
		return f
	}
	if f := find(cmd.InheritedFlags()); f != nil {
		return f
	}
	for c := cmd.Parent(); c != nil; c = c.Parent() {
		if f := find(c.PersistentFlags()); f != nil {
			return f
		}
	}
	return nil
}
