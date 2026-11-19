package scopes

import (
	"slices"
	"strings"
	"testing"
)

func TestGoSelectionUsesLiteralCaseNames(t *testing.T) {
	selected, err := parseGoSelection([]string{"example.invalid/parser::TestParse/[a]+x"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := goSelectionArgs(selected), []string{`-run=^TestParse$/^\[a\]\+x$`, "example.invalid/parser"}; !slices.Equal(got, want) {
		t.Fatalf("arguments = %q, want %q", got, want)
	}
	for _, words := range [][]string{
		nil, {"example.invalid/parser::TestParse", "example.invalid/parser::TestOther"},
		{"-flags::TestParse"}, {"example.invalid/...::TestParse"}, {"std::TestParse"},
		{"example.invalid/parser::BenchmarkParse"}, {"example.invalid/parser::TestParse//leaf"},
		{"example.invalid/parser::TestParse/leaf name"},
	} {
		if _, err := parseGoSelection(words); err == nil {
			t.Errorf("accepted invalid ID: %q", words)
		}
	}
}

func TestGoScopeRefusesUnsupportedCapabilitiesBeforeLaunch(t *testing.T) {
	for _, scenario := range []struct {
		name, problem string
		scope         map[string]any
		args          []string
	}{
		{"wrong judge", "requires a scope with selector", map[string]any{
			"judge": "exit", "selector": "go-test", "run": argv("go", "test", "-json", "-count=1", "{test_args}"),
		}, nil},
		{"embedded placeholder", "go-test selector requires", map[string]any{
			"judge": "go-json", "selector": "go-test", "run": argv("go", "test", "-json", "-count=1", "flag={test_args}"),
		}, nil},
		{"duration support", "does not support durations", map[string]any{
			"judge": "exit", "durations": true, "run": argv("true"),
		}, nil},
		{"missing ID", "needs exactly one Go test ID", map[string]any{
			"judge": "go-json", "selector": "go-test", "run": argv("go", "test", "-json", "-count=1", "{test_args}"),
		}, nil},
		{"unsupported selector", "requires a scope with selector", map[string]any{
			"judge": "go-json", "selector": "junit", "run": argv("go", "test", "-json", "-count=1", "{test_args}"),
		}, []string{"example.invalid/parser::TestParse"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			world := newWorld(t, map[string]any{"scopes": map[string]any{"x-pick": scenario.scope}}, nil)
			world.expectStatus(world.jst(append([]string{"x-pick"}, scenario.args...)...), statusRefused)
			world.expectSaid(scenario.problem)
			if len(world.launches) != 0 {
				t.Fatal("an unsupported scope launched a process")
			}
		})
	}
}

func TestPathSelectionRefusesScopeRequiringGoID(t *testing.T) {
	section := map[string]any{
		"scopes": map[string]any{
			"x-pick": map[string]any{
				"judge": "go-json", "selector": "go-test", "run": argv("go", "test", "-json", "-count=1", "{test_args}"),
			},
			"gate": map[string]any{"run": argv("true")},
		},
		"selection": map[string]any{
			"order": argv("x-pick", "gate"),
			"rules": []any{map[string]any{"glob": "*.go", "scope": "x-pick"}},
		},
	}
	if problems := selectionProblems(section); len(problems) != 1 || !strings.Contains(problems[0], "needs a test ID") {
		t.Fatalf("problems = %q", problems)
	}
}
