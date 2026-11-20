package doctor

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestEveryProblemNamesItsFix: each place that reports a problem gives it a fix that is not empty
// (W-078), and every kind of problem is one of these.
func TestEveryProblemNamesItsFix(t *testing.T) {
	want := []string{
		"adoption", "binary-not-signed", "checkout-moved", "codesign-identity-invalid",
		"codesign-setting-unreadable", "codesign-uninspected", "config-error", "expected-never-called",
		"expected-unreadable", "failed-open", "folder-incomplete", "guard-does-not-refuse",
		"guard-never-called", "land-account", "land-guard", "land-invalid", "land-lint-missing", "land-lint-not-required", "land-method",
		"land-no-required-checks", "land-rules", "land-rules-unread", "launcher-missing", "launcher-not-fixed", "layout-unreadable",
		"link-missing", "lock-unwritable", "path-not-first", "sandbox-blocks-state", "version-mismatch",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	var found []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}
			selector, isSelector := call.Fun.(*ast.SelectorExpr)
			if !isSelector || selector.Sel.Name != "problem" {
				return true
			}
			where := fileSet.Position(call.Pos())
			if len(call.Args) != 3 {
				t.Errorf("%s: a problem without its fix", where)
				return true
			}
			kind, isLiteral := call.Args[0].(*ast.BasicLit)
			if !isLiteral {
				t.Errorf("%s: a problem's kind is not a literal", where)
				return true
			}
			name, _ := strconv.Unquote(kind.Value)
			if literal, isLiteral := call.Args[2].(*ast.BasicLit); isLiteral && strings.TrimSpace(literal.Value) == `""` {
				t.Errorf("%s: %s has an empty fix", where, name)
			}
			found = append(found, name)
			return true
		})
	}
	slices.Sort(found)
	found = slices.Compact(found)
	if !slices.Equal(found, want) {
		t.Errorf("the kinds of problem are %v, the list here is %v: give a new kind its fix and list it", found, want)
	}
}

// TestTheReportPrintsEachFix: in the text report, every problem is followed by its fix; in JSON, each
// has a "fix".
func TestTheReportPrintsEachFix(t *testing.T) {
	given := inputs(t, t.TempDir()) // an empty prefix: nothing is installed
	given.ExpectedErr = errors.New("unreadable")
	given.Expected = []string{"never-seen"}
	report := Diagnose(given)
	if len(report.Problems) < 4 {
		t.Fatalf("too few problems to judge: %v", report.Problems)
	}
	for _, problem := range report.Problems {
		if strings.TrimSpace(problem.Fix) == "" {
			t.Errorf("%s has no fix: %s", problem.Kind, problem.Text)
		}
	}
	var text bytes.Buffer
	writeText(&text, report)
	lines := strings.Split(text.String(), "\n")
	for index, line := range lines {
		if strings.HasPrefix(line, "PROBLEM: ") &&
			(index+1 >= len(lines) || !strings.HasPrefix(lines[index+1], "  fix: ") || len(lines[index+1]) <= len("  fix: ")) {
			t.Errorf("no fix line after %q:\n%s", line, text.String())
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Problems []map[string]string `json:"problems"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, problem := range decoded.Problems {
		if problem["fix"] == "" {
			t.Errorf("JSON problem %s has no fix", problem["kind"])
		}
	}
}

// TestTheInstallAFixAdvisesKeepsThePrefix: the one-liner in a fix hands the prefix, spaces and
// quotes included, to the shell that runs the install script, so the install goes where the hooks
// point.
func TestTheInstallAFixAdvisesKeepsThePrefix(t *testing.T) {
	for _, prefix := range []string{"/home/you/.local", "/Volumes/My Disk/tools", "/it's $HOME"} {
		for _, fix := range []string{installAgain(prefix), replaceLive(prefix)} {
			command := installInto(prefix)
			if !strings.Contains(fix, "`"+command+"`") {
				t.Fatalf("the fix does not give the whole command: %s", fix)
			}
			// The download stands in for a script that prints the prefix it was given.
			stand := strings.Replace(command, quickInstall, `printf '%s' 'printf %s "$JACKSTAY_PREFIX"'`, 1)
			out, err := exec.Command("sh", "-c", stand).Output()
			if err != nil || string(out) != prefix {
				t.Errorf("%s: the script is given %q (%v), want %q", command, out, err, prefix)
			}
		}
		fix := replaceLive(prefix)
		if !strings.Contains(fix, "`jst doctor --prefix "+shellWord(prefix)+"`") {
			t.Errorf("the check after the repair does not name %s: %s", shellWord(prefix), fix)
		}
		if strings.Contains(fix, "uninstall") {
			t.Errorf("a fix advises an uninstall, which would remove the version before too: %s", fix)
		}
	}
}

// TestASandboxFixNeverRemovesADeny: a denyWrite entry may protect more than jackstay's state, so the
// fix never tells the owner to take it out.
func TestASandboxFixNeverRemovesADeny(t *testing.T) {
	fix := denied("/Users/*/.jackstay/.all/**", "/Users/you/.jackstay", "/Users/you/.claude/settings.json")
	for _, word := range []string{"take", "remove", "delete"} {
		if strings.Contains(strings.ToLower(fix), word) {
			t.Errorf("the fix says %q: %s", word, fix)
		}
	}
	if !strings.Contains(fix, "JACKSTAY_HOME") || !strings.Contains(fix, "still protects") {
		t.Errorf("the fix names neither the narrowing nor the move: %s", fix)
	}
}
