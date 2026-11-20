package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

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

func landProject(t *testing.T, text string, rules hosting.Rules, connectErr error) func() (LandProject, bool) {
	t.Helper()
	return landProjectWith(t, text, false, rules, connectErr)
}

func landProjectWith(t *testing.T, text string, lintMissing bool, rules hosting.Rules, connectErr error) func() (LandProject, bool) {
	t.Helper()
	parsed, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return func() (LandProject, bool) {
		return LandProject{Config: parsed, LintMissing: lintMissing, Connect: func(context.Context) (hosting.Host, string, error) {
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
		{"healthy", land(""), read, nil, "", "land-lint-confirm"},
		{"invalid section", `{"jackstay_schema":3,"land":{"autonomy":"always"}}`, read, nil, "land-invalid", ""},
		{"no account", land(""), read, errors.New("no token"), "land-account", "land-lint-confirm"},
		{"rules unread, no checks", land(""), unread, nil, "land-rules-unread", "land-lint-confirm,land-method"},
		{"rules unread, checks listed", land(`,"checks":["build"]`), unread, nil, "", "land-lint-confirm,land-method,land-rules-unread"},
		{"rules require none, no checks", land(""), none, nil, "land-lint-not-required,land-no-required-checks", ""},
		{"rules require none, checks listed", land(`,"checks":["build"]`), none, nil, "land-lint-not-required", "land-no-required-checks"},
		{"lint not required", land(""), noLint, nil, "land-lint-not-required", ""},
		{"method not allowed", land(`,"method":"rebase"`), read, nil, "land-method", "land-lint-confirm"},
		{"queue required", land(""), queued, nil, "", "land-lint-confirm,land-queue"},
		{"guard off", `{"jackstay_schema":3,"guard":{"rules":{"gh-pr-merge":{"level":"off"}}},"land":{"autonomy":"prepare"}}`, read, nil, "land-guard", "land-lint-confirm"},
		{"guard warns", `{"jackstay_schema":3,"guard":{"rules":{"gh-pr-merge":{"level":"warn"}}},"land":{"autonomy":"prepare"}}`, read, nil, "", "land-guard,land-lint-confirm"},
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

// Doctor never reports D0 as enforced: a missing lint workflow and readable rules that require no
// `card-line` check are problems; otherwise one note leaves the confirmation to a person.
func TestDoctorNeverConfirmsD0(t *testing.T) {
	file := `{"jackstay_schema":3,"land":{"autonomy":"prepare"}}`
	requires := func(names ...string) hosting.Rules {
		rules := hosting.Rules{ChecksRead: true, MethodsRead: true, Methods: []hosting.Method{hosting.Squash}}
		for _, name := range names {
			rules.Required = append(rules.Required, hosting.RequiredCheck{Name: name})
		}
		return rules
	}
	unread := hosting.Rules{MethodsRead: true, Methods: []hosting.Method{hosting.Squash}, Unread: "refused"}
	for _, testCase := range []struct {
		name            string
		missing         bool
		rules           hosting.Rules
		problems, notes string
	}{
		{"the workflow missing", true, requires("build", "card-line"), "land-lint-missing", ""},
		{"readable rules without card-line", false, requires("build"), "land-lint-not-required", ""},
		{"rules that require card-line", false, requires("build", "card-line"), "", "land-lint-confirm"},
		{"a card-line check of another app", false, hosting.Rules{ChecksRead: true, Required: []hosting.RequiredCheck{{Name: "card-line", App: 7}},
			MethodsRead: true, Methods: []hosting.Method{hosting.Squash}}, "", "land-lint-confirm"},
		{"rules unreadable", false, unread, "land-rules-unread", "land-lint-confirm"},
	} {
		report := Report{Facts: map[string]any{}, Problems: []Finding{}, Notes: []Finding{}}
		checkLand(&report, Inputs{Land: landProjectWith(t, file, testCase.missing, testCase.rules, nil)})
		if got := kinds(report.Problems); got != testCase.problems {
			t.Errorf("%s: problems %q, want %q (%+v)", testCase.name, got, testCase.problems, report.Problems)
		}
		if got := kinds(report.Notes); got != testCase.notes {
			t.Errorf("%s: notes %q, want %q (%+v)", testCase.name, got, testCase.notes, report.Notes)
		}
		for _, finding := range append(append([]Finding{}, report.Problems...), report.Notes...) {
			if strings.Contains(finding.Text, "is enforced\n") || strings.HasPrefix(finding.Text, "D0 is enforced") {
				t.Errorf("%s: doctor vouches for D0: %s", testCase.name, finding.Text)
			}
		}
	}
}
