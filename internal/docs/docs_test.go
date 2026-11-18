package docs

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func markdownFiles(t *testing.T, root string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "*.md").Output()
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, f := range strings.Fields(string(out)) {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			files = append(files, f)
		}
	}
	return files
}

var link = regexp.MustCompile(`\]\(([^)\s]+)\)`)

func TestRelativeLinksLead(t *testing.T) {
	root := repoRoot(t)
	for _, file := range markdownFiles(t, root) {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		inFence := false
		for n, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
			}
			if inFence {
				continue
			}
			for _, m := range link.FindAllStringSubmatch(line, -1) {
				target := m[1]
				if strings.Contains(target, "://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				target, _, _ = strings.Cut(target, "#")
				target, _, _ = strings.Cut(target, ":") // file.go:42
				if target == "" {
					continue
				}
				if _, err := os.Stat(filepath.Join(root, filepath.Dir(file), target)); err != nil {
					t.Errorf("%s:%d: %s leads nowhere", file, n+1, m[1])
				}
			}
		}
	}
}

var (
	heading      = regexp.MustCompile(`^#{1,6} +(.+?)[ #]*$`)
	markdownLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	notInAnchor  = regexp.MustCompile(`[^\p{L}\p{N}\- _]`)
)

// anchors are the ids GitHub gives the headings of a Markdown file: a heading's text without the
// Markdown of its links and code, lower-cased, every character but letters, digits, hyphens,
// underscores and spaces dropped, spaces made hyphens; a repeated one gets -1, -2. A line in a
// fenced code block is no heading.
func anchors(markdown string) map[string]bool {
	ids := map[string]bool{}
	seen := map[string]int{}
	fence := ""
	for line := range strings.Lines(markdown) {
		line = strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) <= 3 {
			for _, marker := range []string{"```", "~~~"} {
				if !strings.HasPrefix(trimmed, marker) {
					continue
				}
				run := trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, marker[:1]))]
				switch {
				case fence == "":
					fence = run
				case strings.HasPrefix(run, fence) && strings.TrimSpace(trimmed[len(run):]) == "":
					fence = ""
				}
				line = ""
			}
		}
		if fence != "" || line == "" {
			continue
		}
		m := heading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text := strings.ReplaceAll(markdownLink.ReplaceAllString(m[1], "$1"), "`", "")
		id := strings.ReplaceAll(notInAnchor.ReplaceAllString(strings.ToLower(text), ""), " ", "-")
		if n := seen[id]; n > 0 {
			seen[id]++
			id = fmt.Sprintf("%s-%d", id, n)
		} else {
			seen[id] = 1
		}
		ids[id] = true
	}
	return ids
}

func TestAnchorsAreGitHubs(t *testing.T) {
	ids := anchors("# 7.4 Signing at install ([W-070](../decisions/W-070.md))\n" +
		"## `jst trash`\n## Notes\n## Notes\n" +
		"```markdown\n## jst: the tools for working on this project\n```\n" +
		"~~~\n# in a tilde fence\n```\n# still in it\n~~~\n## After the fences ##\n")
	for _, want := range []string{"74-signing-at-install-w-070", "jst-trash", "notes", "notes-1", "after-the-fences"} {
		if !ids[want] {
			t.Errorf("no anchor %q in %v", want, ids)
		}
	}
	for _, unwanted := range []string{"jst-the-tools-for-working-on-this-project", "in-a-tilde-fence", "still-in-it"} {
		if ids[unwanted] {
			t.Errorf("a heading in a code block gave the anchor %q", unwanted)
		}
	}
}

// TestFragmentsLeadToHeadings: a relative link to a heading of another Markdown file (which the
// site turns into a link to that file on GitHub) names a heading the file has, as GitHub names it.
func TestFragmentsLeadToHeadings(t *testing.T) {
	root := repoRoot(t)
	cache := map[string]map[string]bool{}
	for _, file := range markdownFiles(t, root) {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		inFence := false
		for n, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
			}
			if inFence {
				continue
			}
			for _, m := range link.FindAllStringSubmatch(line, -1) {
				target, fragment, found := strings.Cut(m[1], "#")
				if !found || target == "" || strings.Contains(target, "://") || !strings.HasSuffix(target, ".md") {
					continue
				}
				path := filepath.Join(root, filepath.Dir(file), target)
				ids, read := cache[path]
				if !read {
					text, err := os.ReadFile(path)
					if err != nil {
						continue // TestRelativeLinksLead names it
					}
					ids = anchors(string(text))
					cache[path] = ids
				}
				if !ids[fragment] {
					t.Errorf("%s:%d: %s names no heading of %s", file, n+1, m[1], target)
				}
			}
		}
	}
}

func TestDecisionsAreListed(t *testing.T) {
	root := repoRoot(t)
	index, err := os.ReadFile(filepath.Join(root, "docs/decisions/README.md"))
	if err != nil {
		t.Fatal(err)
	}
	decisions, _ := filepath.Glob(filepath.Join(root, "docs/decisions/W-*.md"))
	if len(decisions) == 0 {
		t.Fatal("no decisions found")
	}
	for _, path := range decisions {
		if name := filepath.Base(path); !strings.Contains(string(index), "("+name+")") {
			t.Errorf("docs/decisions/README.md does not list %s", name)
		}
	}
}

// requireLine is one module go.mod requires, in a block or on a `require` line: its path and version.
var requireLine = regexp.MustCompile(`^\s*(?:require\s+)?([a-z0-9.\-]+\.[a-z]{2,}/\S+)\s+(v\S+)`)

func TestEveryModuleHasItsLicenceListed(t *testing.T) {
	root := repoRoot(t)
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	listing, err := os.ReadFile(filepath.Join(root, "docs/licences.md"))
	if err != nil {
		t.Fatal(err)
	}
	modules := 0
	block := "" // the directive of the block a line is in: require, replace, exclude …
	for _, line := range strings.Split(string(goMod), "\n") {
		trimmed := strings.TrimSpace(line)
		if directive, opens := strings.CutSuffix(trimmed, "("); opens {
			block = strings.TrimSpace(directive)
			continue
		}
		if trimmed == ")" {
			block = ""
			continue
		}
		if block != "" && block != "require" {
			continue
		}
		if block == "" && !strings.HasPrefix(trimmed, "require ") {
			continue
		}
		found := requireLine.FindStringSubmatch(line)
		if found == nil {
			continue
		}
		modules++
		row := "| `" + found[1] + "` | " + found[2] + " |"
		if !strings.Contains(string(listing), row) {
			t.Errorf("docs/licences.md has no row for %s %s: read its licence and add it", found[1], found[2])
		}
	}
	if modules == 0 {
		t.Fatal("no module read from go.mod")
	}
}

// notShipped names the files and folders (one a line, a folder ending in /) a repository keeps
// beside the code without shipping them: notes, which the checks of what is shipped leave out.
const notShipped = ".not-shipped"

// notShippedPaths are .not-shipped's paths; english keeps only those whose line also says
// `english`: notes that are not shipped but must still be written in English (decision records).
func notShippedPaths(t *testing.T, root string, english bool) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, notShipped))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if english && slices.Contains(fields[1:], "english") {
			continue
		}
		paths = append(paths, fields[0])
	}
	return paths
}

// leftOut says whether file is one of paths or under one of them (a path ending in /).
func leftOut(file string, paths []string) bool {
	for _, path := range paths {
		if file == path || strings.HasSuffix(path, "/") && strings.HasPrefix(file, path) {
			return true
		}
	}
	return false
}

// cyrillicTestData is non-ASCII input the tests check on purpose: folder names in Cyrillic (escaped
// here, so this file passes its own check).
var cyrillicTestData = map[string][]string{
	"internal/lint/lint_integration_test.go": {"\u0438\u0441\u0442\u043e\u0440\u0438\u044f"},
	"internal/owner/owner_test.go":           {"\u043f\u0440\u043e\u0435\u043a\u0442"},
}

var cyrillic = regexp.MustCompile(`\p{Cyrillic}+`)

func TestEverythingIsEnglish(t *testing.T) {
	root := repoRoot(t)
	// -z: names unquoted, as they are on disk, whatever characters they hold
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatal(err)
	}
	allowed := notShippedPaths(t, root, true)
	checked := 0
	for _, file := range strings.Split(string(out), "\x00") {
		if file == "" || leftOut(file, allowed) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, file))
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted in the working tree, not yet committed
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
			continue // binary
		}
		checked++
		for n, line := range strings.Split(string(data), "\n") {
			for _, word := range cyrillic.FindAllString(line, -1) {
				if !slices.Contains(cyrillicTestData[file], word) {
					t.Errorf("%s:%d: %q is not English (only the paths %s lists are left out)", file, n+1, word, notShipped)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no file checked")
	}
}

// apacheLicence is the sha256 of the Apache License 2.0 as apache.org publishes it
// (https://www.apache.org/licenses/LICENSE-2.0.txt).
const apacheLicence = "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30"

// TestTheLicenceIsApacheWithItsNoticeFilledIn: LICENSE is the published text, word for word, but
// for its appendix's example notice, filled in with the year and the copyright owner.
func TestTheLicenceIsApacheWithItsNoticeFilledIn(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	const filled, template = "   Copyright 2026 Max Zima\n", "   Copyright [yyyy] [name of copyright owner]\n"
	if strings.Count(string(data), filled) != 1 {
		t.Fatalf("LICENSE does not carry the notice %q once", strings.TrimSpace(filled))
	}
	published := strings.Replace(string(data), filled, template, 1)
	if sum := fmt.Sprintf("%x", sha256.Sum256([]byte(published))); sum != apacheLicence {
		t.Fatalf("LICENSE, its notice put back to the template, is not the published Apache-2.0 text (sha256 %s)", sum)
	}
}

// leftToFill are the marks of text not finished: a note to come back, an unfilled template field,
// filler. The pattern list lives in this file, which the check therefore leaves out.
var leftToFill = []*regexp.Regexp{
	regexp.MustCompile(`\b(TODO|TBD|FIXME|XXX)\b`),
	regexp.MustCompile(`(?i)\blorem ipsum\b`),
	regexp.MustCompile(`\[yyyy\]|\[name of copyright owner\]`),
	regexp.MustCompile(`(?i)<your[- ][a-z][a-z -]*>|\bCHANGE ?ME\b|\bREPLACE[_ ]ME\b`),
}

// exampleDomain is a stand-in host: right in tests, and wrong where a real value belongs.
var exampleDomain = regexp.MustCompile(`\bexample\.(com|org|net)\b`)

// TestNothingIsLeftToFillIn: no shipped file holds a placeholder or a stub, no Markdown section
// is empty, and example.com appears only in tests and their data.
func TestNothingIsLeftToFillIn(t *testing.T) {
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatal(err)
	}
	notes := notShippedPaths(t, root, false)
	for _, file := range strings.Split(string(out), "\x00") {
		if file == "" || leftOut(file, notes) || file == "internal/docs/docs_test.go" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, file))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
			continue // binary
		}
		inTests := strings.HasSuffix(file, "_test.go") || strings.Contains(file, "/testdata/") || strings.HasPrefix(file, "tests/")
		lines := strings.Split(string(data), "\n")
		for n, line := range lines {
			for _, mark := range leftToFill {
				if found := mark.FindString(line); found != "" {
					t.Errorf("%s:%d: %q is left to fill in", file, n+1, found)
				}
			}
			if !inTests && exampleDomain.MatchString(line) {
				t.Errorf("%s:%d: %q stands in for a real host", file, n+1, exampleDomain.FindString(line))
			}
		}
		if strings.HasSuffix(file, ".md") {
			for _, empty := range emptySections(lines) {
				t.Errorf("%s:%d: the section %q is empty", file, empty.line, empty.heading)
			}
		}
	}
}

type section struct {
	line    int
	heading string
}

// emptySections are the Markdown headings followed by nothing but blank lines before the next heading
// of the same or a higher level (or the end), outside code fences.
func emptySections(lines []string) []section {
	var empty []section
	inFence := false
	var open *section
	openLevel := 0
	content := false
	for n, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			content = true
			continue
		}
		level := 0
		if !inFence {
			for level < len(line) && line[level] == '#' {
				level++
			}
			if level == 0 || level > 6 || len(line) == level || line[level] != ' ' {
				level = 0
			}
		}
		if level > 0 {
			if open != nil && !content && level <= openLevel {
				empty = append(empty, *open)
			}
			open, openLevel, content = &section{line: n + 1, heading: strings.TrimSpace(line[level:])}, level, false
			continue
		}
		if trimmed != "" {
			content = true
		}
	}
	if open != nil && !content {
		empty = append(empty, *open)
	}
	return empty
}
