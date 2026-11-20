package land

import (
	"slices"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/hosting"
)

func check(name string, state hosting.State) hosting.Check {
	return hosting.Check{Name: name, State: state, Commit: "head"}
}

func onMerge(name string, state hosting.State) hosting.Check {
	return hosting.Check{Name: name, State: state, Commit: "merge", OnTestMerge: true}
}

func required(names ...string) []hosting.RequiredCheck {
	var out []hosting.RequiredCheck
	for _, name := range names {
		out = append(out, hosting.RequiredCheck{Name: name})
	}
	return out
}

func TestRequiredChecksPassFailPendingAndMissing(t *testing.T) {
	judged := judge([]hosting.Check{check("test", hosting.Pass), check("lint", hosting.Skipped)}, required("test", "lint"))
	if judged.passed != 2 || len(judged.pending) != 0 || len(judged.failed) != 0 {
		t.Fatalf("passed and skipped: %+v", judged)
	}
	judged = judge([]hosting.Check{check("test", hosting.Fail)}, required("test"))
	if len(judged.failed) != 1 {
		t.Fatalf("a failed required check: %+v", judged)
	}
	judged = judge([]hosting.Check{check("test", hosting.Cancelled)}, required("test"))
	if len(judged.failed) != 1 {
		t.Fatalf("a cancelled required check stops: %+v", judged)
	}
	judged = judge([]hosting.Check{check("test", hosting.Pending)}, required("test", "lint"))
	if !slices.Equal(judged.pending, []string{"test", "lint (not registered yet)"}) {
		t.Fatalf("pending and missing: %+v", judged.pending)
	}
}

// Section 4: a check that is not required is never waited for and never blocks (0.5.11 stopped
// on any failing check); its failure is reported.
func TestAFailingCheckThatIsNotRequiredNeverBlocks(t *testing.T) {
	judged := judge([]hosting.Check{check("test", hosting.Pass), check("flaky", hosting.Fail), check("slow", hosting.Pending)},
		required("test"))
	if len(judged.failed) != 0 || len(judged.pending) != 0 || judged.passed != 1 {
		t.Fatalf("an optional check counted: %+v", judged)
	}
	if len(judged.reported) != 2 {
		t.Fatalf("optional failures are reported: %+v", judged.reported)
	}
}

// Required checks are judged on the test merge once it has results, as the host judges them.
func TestRequiredChecksAreJudgedOnTheTestMergeOnceItHasResults(t *testing.T) {
	judged := judge([]hosting.Check{check("test", hosting.Pass), onMerge("other", hosting.Pass)}, required("test"))
	if !judged.onMerge || !slices.Equal(judged.pending, []string{"test (not registered yet)"}) {
		t.Fatalf("the head's pass counted though the test merge has results: %+v", judged)
	}
	judged = judge([]hosting.Check{check("test", hosting.Fail), onMerge("test", hosting.Pass)}, required("test"))
	if judged.passed != 1 || len(judged.failed) != 0 {
		t.Fatalf("judged on the head: %+v", judged)
	}
	judged = judge([]hosting.Check{check("test", hosting.Pass)}, required("test"))
	if judged.onMerge || judged.passed != 1 {
		t.Fatalf("no test merge results: judged on the head: %+v", judged)
	}
}

func TestARequiredCheckNamingAnAppNeedsThatApp(t *testing.T) {
	wanted := []hosting.RequiredCheck{{Name: "test", App: 15368}}
	other := hosting.Check{Name: "test", App: 7, State: hosting.Pass}
	if judged := judge([]hosting.Check{other}, wanted); len(judged.pending) != 1 {
		t.Fatalf("another app's check satisfied it: %+v", judged)
	}
	right := hosting.Check{Name: "test", App: 15368, State: hosting.Pass}
	if judged := judge([]hosting.Check{right}, wanted); judged.passed != 1 {
		t.Fatalf("the app's check did not: %+v", judged)
	}
}

func TestWithNoGateEveryCheckIsWaitedForAndNoneBlocks(t *testing.T) {
	judged := judge([]hosting.Check{check("a", hosting.Fail), check("b", hosting.Pending), check("c", hosting.Pass)}, nil)
	if len(judged.failed) != 0 || !slices.Equal(judged.pending, []string{"b"}) || len(judged.reported) != 1 {
		t.Fatalf("%+v", judged)
	}
}

func TestRequiredChecksFromTheRulesAndTheBasesFile(t *testing.T) {
	rules := hosting.Rules{ChecksRead: true, Required: required("test")}
	got, gate := requiredChecks(rules, config.Land{Checks: []string{"docs", "test"}})
	if gate != "" || len(got) != 2 || got[1].Name != "docs" {
		t.Fatalf("rules plus land.checks, no duplicate: %v %q", got, gate)
	}
	// Rules read and requiring none, checks empty: CI is not a gate.
	if got, gate := requiredChecks(hosting.Rules{ChecksRead: true}, config.Land{}); got != nil || !strings.Contains(gate, "require no check") {
		t.Fatalf("rules requiring none: %v %q", got, gate)
	}
	// Rules unread: the listed checks are required, every other check is only reported.
	unread := hosting.Rules{Unread: "the plan does not include rulesets"}
	if got, gate := requiredChecks(unread, config.Land{Checks: []string{"test"}}); gate != "" || len(got) != 1 {
		t.Fatalf("rules unread with checks: %v %q", got, gate)
	}
	if got, gate := requiredChecks(unread, config.Land{}); got != nil || !strings.Contains(gate, "cannot be read (the plan") {
		t.Fatalf("rules unread without checks: %v %q", got, gate)
	}
}

func TestHumanPathMatches(t *testing.T) {
	cases := []struct {
		pattern, file string
		want          bool
	}{
		{"jackstay.json", "jackstay.json", true},
		{"jackstay.json", "sub/jackstay.json", false},
		{".github/workflows/**", ".github/workflows/ci.yml", true},
		{".github/workflows/**", ".github/workflows/sub/x.yml", true},
		{".github/workflows/**", ".github/dependabot.yml", false},
		{"**/secrets.txt", "a/b/secrets.txt", true},
		{"**/secrets.txt", "secrets.txt", true},
		{"deploy/*.yaml", "deploy/prod.yaml", true},
		{"deploy/*.yaml", "deploy/x/prod.yaml", false},
		{".claude/settings.json", ".claude/settings.local.json", false},
	}
	for _, each := range cases {
		if got := humanPathMatches(each.pattern, each.file); got != each.want {
			t.Errorf("%s ~ %s: %v", each.pattern, each.file, got)
		}
	}
}

func TestOptions(t *testing.T) {
	var stderr strings.Builder
	if _, err := parseOptions([]string{"--prepare", "--confirm"}, config.GitHub{}, &stderr); codeOf(err) != exitInvalid {
		t.Fatalf("--prepare with --confirm: %v", err)
	}
	if _, err := parseOptions([]string{"word"}, config.GitHub{}, &stderr); codeOf(err) != exitInvalid {
		t.Fatalf("a word: %v", err)
	}
	if _, err := parseOptions([]string{"--interval", "0"}, config.GitHub{}, &stderr); codeOf(err) != exitInvalid {
		t.Fatalf("--interval 0: %v", err)
	}
	chosen, err := parseOptions(nil, config.GitHub{Wait: config.GitHubWait{TimeoutSeconds: 60}}, &stderr)
	if err != nil || chosen.timeout.Seconds() != 60 || chosen.lines != 25 {
		t.Fatalf("defaults: %+v %v", chosen, err)
	}
}

func TestCardLine(t *testing.T) {
	if line, err := cardLine([]string{"W-1", "W-2", "W-1"}, ""); err != nil || line != "Card: W-1, W-2" {
		t.Fatalf("%q %v", line, err)
	}
	if line, err := cardLine([]string{"none"}, "a typo"); err != nil || line != "Card: none (a typo)" {
		t.Fatalf("%q %v", line, err)
	}
	if _, err := cardLine([]string{"none"}, ""); codeOf(err) != exitInvalid {
		t.Fatalf("none without a reason: %v", err)
	}
}

// Rulesets and classic protection may require one name twice, once with an app and once without:
// each requirement is judged on every row that satisfies it.
func TestOverlappingRequirementsOfOneNameAreEachSatisfied(t *testing.T) {
	wanted := []hosting.RequiredCheck{{Name: "test"}, {Name: "test", App: 15368}}
	passed := hosting.Check{Name: "test", App: 15368, State: hosting.Pass}
	if judged := judge([]hosting.Check{passed}, wanted); judged.passed != 2 || len(judged.pending) != 0 {
		t.Fatalf("the app's pass satisfies both: %+v", judged)
	}
	other := hosting.Check{Name: "test", App: 7, State: hosting.Pass}
	if judged := judge([]hosting.Check{other}, wanted); judged.passed != 1 || len(judged.pending) != 1 {
		t.Fatalf("another app's pass satisfies only the unrestricted one: %+v", judged)
	}
}
