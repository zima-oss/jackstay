package doctor

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/adopt"
	"github.com/zima-oss/jackstay/internal/adopt/templates"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
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

func landProject(t *testing.T, text string, rules hosting.Rules, connectErr error) func() (LandProject, bool) {
	t.Helper()
	return landProjectWith(t, text, adopt.LintWorkflowManaged, rules, connectErr)
}

func landProjectWith(t *testing.T, text string, lintWorkflow string, rules hosting.Rules, connectErr error) func() (LandProject, bool) {
	t.Helper()
	return landProjectRead(t, text, lintWorkflow, "", rules, connectErr)
}

func landProjectRead(t *testing.T, text, lintWorkflow, lintUnread string, rules hosting.Rules, connectErr error) func() (LandProject, bool) {
	t.Helper()
	parsed, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return func() (LandProject, bool) {
		return LandProject{Config: parsed, LintWorkflow: lintWorkflow, LintUnread: lintUnread, Connect: func(context.Context) (hosting.Host, string, error) {
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
		{"healthy", land(""), read, nil, "", "land-lint-forks"},
		{"invalid section", `{"jackstay_schema":3,"land":{"autonomy":"always"}}`, read, nil, "land-invalid", ""},
		{"no account", land(""), read, errors.New("no token"), "land-account", ""},
		{"rules unread, no checks", land(""), unread, nil, "land-rules-unread", "land-lint-unverified,land-method"},
		{"rules unread, checks listed", land(`,"checks":["build"]`), unread, nil, "", "land-lint-unverified,land-method,land-rules-unread"},
		{"rules require none, no checks", land(""), none, nil, "land-lint-not-required,land-no-required-checks", ""},
		{"rules require none, checks listed", land(`,"checks":["build"]`), none, nil, "land-lint-not-required", "land-no-required-checks"},
		{"lint not required", land(""), noLint, nil, "land-lint-not-required", ""},
		{"method not allowed", land(`,"method":"rebase"`), read, nil, "land-method", "land-lint-forks"},
		{"queue required", land(""), queued, nil, "", "land-lint-forks,land-queue"},
		{"guard off", `{"jackstay_schema":3,"guard":{"rules":{"gh-pr-merge":{"level":"off"}}},"land":{"autonomy":"prepare"}}`, read, nil, "land-guard", "land-lint-forks"},
		{"guard warns", `{"jackstay_schema":3,"guard":{"rules":{"gh-pr-merge":{"level":"warn"}}},"land":{"autonomy":"prepare"}}`, read, nil, "", "land-guard,land-lint-forks"},
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

// Rule D0 is confirmed only for the workflow adopt manages: its `card-line` check required.
func TestTheLintCheckIsAdoptsCardLine(t *testing.T) {
	file := `{"jackstay_schema":3,"land":{"autonomy":"prepare"}}`
	requires := func(names ...string) hosting.Rules {
		rules := hosting.Rules{ChecksRead: true, MethodsRead: true, Methods: []hosting.Method{hosting.Squash}}
		for _, name := range names {
			rules.Required = append(rules.Required, hosting.RequiredCheck{Name: name})
		}
		return rules
	}
	for _, testCase := range []struct {
		name, state     string
		rules           hosting.Rules
		problems, notes string
	}{
		{"managed and required: forks need a person", adopt.LintWorkflowManaged, requires("build", "card-line"), "", "land-lint-forks"},
		{"managed, not required", adopt.LintWorkflowManaged, requires("build"), "land-lint-not-required", ""},
		{"managed, rules unread", adopt.LintWorkflowManaged, hosting.Rules{MethodsRead: true, Methods: []hosting.Method{hosting.Squash}},
			"land-rules-unread", "land-lint-unverified"},
		{"edited or the project's own", adopt.LintWorkflowOwn, requires("card-line"), "", "land-lint-unverified"},
		{"missing", adopt.LintWorkflowMissing, requires("card-line"), "land-lint-missing", ""},
		{"the default branch unread", "", requires("card-line"), "", "land-lint-unverified"},
	} {
		report := Report{Facts: map[string]any{}, Problems: []Finding{}, Notes: []Finding{}}
		unread := ""
		if testCase.state == "" {
			unread = "no fetched default branch"
		}
		checkLand(&report, Inputs{Land: landProjectRead(t, file, testCase.state, unread, testCase.rules, nil)})
		if got := kinds(report.Problems); got != testCase.problems {
			t.Errorf("%s: problems %q, want %q (%+v)", testCase.name, got, testCase.problems, report.Problems)
		}
		if got := kinds(report.Notes); got != testCase.notes {
			t.Errorf("%s: notes %q, want %q (%+v)", testCase.name, got, testCase.notes, report.Notes)
		}
	}
}

// gitIn runs git in dir for a test.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	output, err := gitcli.Git{Dir: dir}.Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

// The managed workflow is judged as the fetched default branch has it, not the checkout: a pristine
// local card-line.yml does not vouch for an edited one on the base.
func TestTheLintWorkflowIsReadFromTheFetchedDefaultBranch(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-q", "-b", "main")
	gitIn(t, root, "config", "user.email", "test@example.com")
	gitIn(t, root, "config", "user.name", "Test")
	gitIn(t, root, "config", "commit.gpgsign", "false")
	settings := `{"jackstay_schema":3,"project":"p","land":{"autonomy":"prepare"},"adopt":{"pin":"v0.6.0"}}`
	write(t, filepath.Join(root, "jackstay.json"), settings)
	parsed, err := config.Parse([]byte(settings))
	if err != nil {
		t.Fatal(err)
	}
	var pristine string
	for _, template := range templates.Embedded() {
		if template.Name == "card-line" {
			body, err := templates.Render(template.Text, map[string]string{"jackstay_version": "v0.6.0",
				"jackstay_repository": templates.Repository, "release_base": "main"})
			if err != nil {
				t.Fatal(err)
			}
			pristine = templates.Marked(template.Name, body)
		}
	}
	workflow := filepath.Join(root, ".github", "workflows", "card-line.yml")
	write(t, workflow, pristine+"# edited on the base\n")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "base")
	gitIn(t, root, "remote", "add", "origin", "https://example.com/example/widgets.git")
	gitIn(t, root, "update-ref", "refs/remotes/origin/main", gitIn(t, root, "rev-parse", "HEAD"))
	write(t, workflow, pristine)
	if local := adopt.LintWorkflowState(root, parsed); local != adopt.LintWorkflowManaged {
		t.Fatalf("the checkout's file: %s", local)
	}
	ctx := context.Background()
	if state, unread := lintWorkflowAtDefault(ctx, gitcli.Git{Dir: root}, nil); state != adopt.LintWorkflowOwn || unread != "" {
		t.Fatalf("the base's edited file: %q, %q", state, unread)
	}
	gitIn(t, root, "update-ref", "-d", "refs/remotes/origin/main")
	if state, unread := lintWorkflowAtDefault(ctx, gitcli.Git{Dir: root}, nil); state != "" || unread == "" {
		t.Fatalf("no fetched default branch: %q, %q", state, unread)
	}
}
