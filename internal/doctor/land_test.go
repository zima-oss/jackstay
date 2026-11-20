package doctor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/adopt/templates"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/hosting"
)

// fakeHost answers the two questions doctor asks; every other operation is not offered.
type fakeHost struct {
	hosting.Host
	rules hosting.Rules
}

func (fakeHost) DefaultBranch(context.Context) (string, error) { return "main", nil }
func (host fakeHost) Rules(context.Context, string) (hosting.Rules, error) {
	return host.rules, nil
}

// adoptedWorkflows are the workflows `jst adopt` writes for a land project: card-line.yml.
func adoptedWorkflows(t *testing.T) map[string]string {
	t.Helper()
	for _, template := range templates.Embedded() {
		if template.Name == "card-line" {
			body, err := templates.Render(template.Text, map[string]string{"jackstay_version": "v0.6.0",
				"jackstay_repository": templates.Repository, "release_base": "main"})
			if err != nil {
				t.Fatal(err)
			}
			return map[string]string{"card-line.yml": templates.Marked(template.Name, body)}
		}
	}
	t.Fatal("no card-line template")
	return nil
}

func landProject(t *testing.T, text string, rules hosting.Rules, connectErr error) func() (LandProject, bool) {
	t.Helper()
	return landProjectWith(t, text, adoptedWorkflows(t), rules, connectErr)
}

func landProjectWith(t *testing.T, text string, workflows map[string]string, rules hosting.Rules, connectErr error) func() (LandProject, bool) {
	t.Helper()
	parsed, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return func() (LandProject, bool) {
		return LandProject{Config: parsed, Workflows: workflows, Connect: func(context.Context) (hosting.Host, string, error) {
			if connectErr != nil {
				return nil, "", connectErr
			}
			return fakeHost{rules: rules}, "example/widgets", nil
		}}, true
	}
}

func landReport(t *testing.T, text string, rules hosting.Rules, connectErr error) Report {
	t.Helper()
	report := Report{Facts: map[string]any{}, Problems: []Finding{}, Notes: []Finding{}}
	checkLand(&report, Inputs{Land: landProject(t, text, rules, connectErr)})
	return report
}

func TestLandChecks(t *testing.T) {
	read := hosting.Rules{ChecksRead: true, Required: []hosting.RequiredCheck{{Name: "build"}, {Name: "card-line"}}, QueueRead: true,
		MethodsRead: true, Methods: []hosting.Method{hosting.Squash, hosting.Merge}}
	unread := hosting.Rules{Unread: "GitHub refused to show the rules of main"}
	none := hosting.Rules{ChecksRead: true, MethodsRead: true, Methods: []hosting.Method{hosting.Squash}}
	noLint := read
	noLint.Required = []hosting.RequiredCheck{{Name: "build"}}
	queued := read
	queued.Queue = true
	land := func(extra string) string {
		return `{"jackstay_schema":3,"land":{"autonomy":"merge-when-green"` + extra + `}}`
	}
	for _, testCase := range []struct {
		name, file      string
		rules           hosting.Rules
		connectErr      error
		problems, notes string
	}{
		{"healthy", land(""), read, nil, "", ""},
		{"invalid section", `{"jackstay_schema":3,"land":{"autonomy":"always"}}`, read, nil, "land-invalid", ""},
		{"no account", land(""), read, errors.New("no token"), "land-account", ""},
		{"rules unread, no checks", land(""), unread, nil, "land-rules-unread", "land-lint-unverified,land-method"},
		{"rules unread, checks listed", land(`,"checks":["build"]`), unread, nil, "", "land-lint-unverified,land-method,land-rules-unread"},
		{"rules require none, no checks", land(""), none, nil, "land-lint-not-required,land-no-required-checks", ""},
		{"rules require none, checks listed", land(`,"checks":["build"]`), none, nil, "land-lint-not-required", "land-no-required-checks"},
		{"lint not required", land(""), noLint, nil, "land-lint-not-required", ""},
		{"method not allowed", land(`,"method":"rebase"`), read, nil, "land-method", ""},
		{"queue required", land(""), queued, nil, "", "land-queue"},
		{"guard off", `{"jackstay_schema":3,"guard":{"rules":{"gh-pr-merge":{"level":"off"}}},"land":{"autonomy":"prepare"}}`, read, nil, "land-guard", ""},
		{"guard warns", `{"jackstay_schema":3,"guard":{"rules":{"gh-pr-merge":{"level":"warn"}}},"land":{"autonomy":"prepare"}}`, read, nil, "", "land-guard"},
	} {
		report := landReport(t, testCase.file, testCase.rules, testCase.connectErr)
		if got := kinds(report.Problems); got != testCase.problems {
			t.Errorf("%s: problems %q, want %q (%+v)", testCase.name, got, testCase.problems, report.Problems)
		}
		if got := kinds(report.Notes); got != testCase.notes {
			t.Errorf("%s: notes %q, want %q (%+v)", testCase.name, got, testCase.notes, report.Notes)
		}
		for _, problem := range report.Problems {
			if problem.Fix == "" {
				t.Errorf("%s: %s names no fix", testCase.name, problem.Kind)
			}
		}
	}
	facts := landReport(t, land(""), read, nil).Facts["land"].(map[string]any)
	if required, _ := facts["required_checks"].([]string); strings.Join(required, ",") != "build,card-line" || facts["base"] != "main" {
		t.Fatalf("facts %v", facts)
	}
}

// A project without a land section, or doctor outside one, asks the host nothing.
func TestLandChecksSkipAProjectWithoutLand(t *testing.T) {
	report := Report{Facts: map[string]any{}}
	checkLand(&report, Inputs{Land: func() (LandProject, bool) { return LandProject{}, false }})
	checkLand(&report, Inputs{})
	if _, present := report.Facts["land"]; present || len(report.Problems)+len(report.Notes) != 0 {
		t.Fatalf("%+v", report)
	}
}

// The check rule D0 needs is the one the job running `jst lint pr` reports, read from the
// workflows, whatever its name.
func TestTheLintCheckIsFoundInTheWorkflows(t *testing.T) {
	file := `{"jackstay_schema":3,"land":{"autonomy":"prepare"}}`
	requires := func(names ...string) hosting.Rules {
		rules := hosting.Rules{ChecksRead: true, MethodsRead: true, Methods: []hosting.Method{hosting.Squash}}
		for _, name := range names {
			rules.Required = append(rules.Required, hosting.RequiredCheck{Name: name})
		}
		return rules
	}
	moved := map[string]string{"card-line.yml": "name: card-line\non: pull_request\njobs:\n  card-line:\n    runs-on: ubuntu-latest\n" +
		"    steps:\n      - run: echo nothing\n", "policy.yml": "name: policy\non: pull_request\njobs:\n  rules:\n" +
		"    name: Policy rules\n    runs-on: ubuntu-latest\n    steps:\n      - name: jst lint pr\n        run: |\n" +
		"          set -e\n          jst lint pr --event \"$GITHUB_EVENT_PATH\"\n"}
	expression := map[string]string{"lint.yml": "jobs:\n  lint:\n    name: ${{ github.workflow }} lint\n    steps:\n      - run: jst lint pr\n"}
	matrix := map[string]string{"lint.yml": "jobs:\n  lint:\n    strategy:\n      matrix:\n        os: [linux]\n    steps:\n      - run: jst lint pr\n"}
	twice := map[string]string{"a.yml": "jobs:\n  one:\n    steps:\n      - run: jst lint pr\n", "b.yml": "jobs:\n  two:\n    steps:\n      - run: jst lint pr\n"}
	for _, testCase := range []struct {
		name            string
		workflows       map[string]string
		rules           hosting.Rules
		problems, notes string
		says            string
	}{
		{"the adopted card-line, required", adoptedWorkflows(t), requires("card-line"), "", "", ""},
		{"the adopted card-line, not required", adoptedWorkflows(t), requires("build"), "land-lint-not-required", "", "`card-line`"},
		{"lint moved to another job, card-line still required", moved, requires("card-line"), "land-lint-not-required", "", "`Policy rules`, the check of policy.yml job rules"},
		{"lint moved, its check required", moved, requires("Policy rules"), "", "", ""},
		{"no workflow", nil, requires("card-line"), "land-lint-missing", "", ""},
		{"an expression name", expression, requires("lint"), "", "land-lint-unverified", "expression"},
		{"a matrix", matrix, requires("lint"), "", "land-lint-unverified", "matrix"},
		{"two jobs", twice, requires("one", "two"), "", "land-lint-unverified", "a.yml job one, b.yml job two"},
	} {
		report := Report{Facts: map[string]any{}, Problems: []Finding{}, Notes: []Finding{}}
		checkLand(&report, Inputs{Land: landProjectWith(t, file, testCase.workflows, testCase.rules, nil)})
		if got := kinds(report.Problems); got != testCase.problems {
			t.Errorf("%s: problems %q, want %q (%+v)", testCase.name, got, testCase.problems, report.Problems)
		}
		if got := kinds(report.Notes); got != testCase.notes {
			t.Errorf("%s: notes %q, want %q (%+v)", testCase.name, got, testCase.notes, report.Notes)
		}
		all := append(append([]Finding{}, report.Problems...), report.Notes...)
		if testCase.says != "" && !slices.ContainsFunc(all, func(finding Finding) bool { return strings.Contains(finding.Text, testCase.says) }) {
			t.Errorf("%s: no finding says %q: %+v", testCase.name, testCase.says, all)
		}
	}
}
