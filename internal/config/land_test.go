package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestLandDefaults(t *testing.T) {
	parsed, err := Parse([]byte(`{"jackstay_schema":3,"land":{"autonomy":"prepare"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if problems := SectionProblems(parsed); len(problems.Sections()) != 0 {
		t.Fatalf("a minimal land section was refused: %v", problems.Of("land"))
	}
	land, ok := parsed.Land()
	want := Land{Autonomy: AutonomyPrepare, Method: "squash", Checks: []string{}, HumanPaths: []string{}}
	if !ok || !reflect.DeepEqual(land, want) {
		t.Fatalf("land %+v, %v; want %+v", land, ok, want)
	}
	if _, ok := (Config{"jackstay_schema": json.Number("3")}).Land(); ok {
		t.Fatal("a file without a land section has one")
	}
}

func TestLandReadsEveryKey(t *testing.T) {
	parsed, err := Parse([]byte(`{"jackstay_schema":3,"land":{"host":"github","autonomy":"merge-when-green",
		"method":"rebase","checks":["build","lint"],"human_paths":["docs/decisions/**","*.lock"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if problems := SectionProblems(parsed).Of("land"); len(problems) != 0 {
		t.Fatalf("a full land section was refused: %v", problems)
	}
	land, _ := parsed.Land()
	want := Land{Host: "github", Autonomy: AutonomyMergeWhenGreen, Method: "rebase",
		Checks: []string{"build", "lint"}, HumanPaths: []string{"docs/decisions/**", "*.lock"}}
	if !reflect.DeepEqual(land, want) {
		t.Fatalf("land %+v; want %+v", land, want)
	}
}

func TestLandProblems(t *testing.T) {
	for _, testCase := range []struct {
		section string
		want    string
	}{
		{`{}`, "`land.autonomy` is missing"},
		{`{"autonomy":"always"}`, "`land.autonomy` must be"},
		{`{"autonomy":"prepare","host":"gitlab"}`, "`land.host` must be \"github\""},
		{`{"autonomy":"prepare","method":"fast-forward"}`, "`land.method` must be"},
		{`{"autonomy":"prepare","checks":[""]}`, "`land.checks[0]` must be a non-empty string"},
		{`{"autonomy":"prepare","checks":"build"}`, "`land.checks`"},
		{`{"autonomy":"prepare","human_paths":["/etc/x"]}`, "`land.human_paths[0]`"},
		{`{"autonomy":"prepare","human_paths":["docs/**x"]}`, "`**` must be a whole path segment"},
		{`{"autonomy":"prepare","human_paths":["docs/[a"]}`, "`land.human_paths[0]`"},
		{`{"autonomy":"prepare","human_paths":["a/../b"]}`, "path segments are not allowed"},
		{`{"autonomy":"prepare","wait":true}`, "unknown key"},
	} {
		parsed, err := Parse([]byte(`{"jackstay_schema":3,"land":` + testCase.section + `}`))
		if err != nil {
			t.Fatal(err)
		}
		problems := strings.Join(SectionProblems(parsed).Of("land"), "; ")
		if !strings.Contains(problems, testCase.want) {
			t.Errorf("%s: got %q, want %q", testCase.section, problems, testCase.want)
		}
	}
}

func TestLandOf(t *testing.T) {
	land, ok, err := LandOf([]byte(`{"jackstay_schema":3,"land":{"autonomy":"merge-when-green","checks":["ci"]}}`))
	if err != nil || !ok || land.Autonomy != AutonomyMergeWhenGreen || land.Method != "squash" ||
		!reflect.DeepEqual(land.Checks, []string{"ci"}) {
		t.Fatalf("land %+v, %v, %v", land, ok, err)
	}
	if _, ok, err := LandOf([]byte(`{"jackstay_schema":3}`)); ok || err != nil {
		t.Fatalf("a file without land: %v, %v", ok, err)
	}
	for _, document := range []string{
		`{"jackstay_schema":3,`,
		`{"jackstay_schema":3,"land":{"autonomy":"sometimes"}}`,
		`{"jackstay_schema":2,"land":{"autonomy":"prepare"}}`,
	} {
		if _, ok, err := LandOf([]byte(document)); ok || err == nil {
			t.Errorf("%s: no error (ok %v)", document, ok)
		}
	}
	_, _, err = LandOf([]byte(`{"jackstay_schema":3,"land":{"autonomy":"prepare","method":"octopus"}}`))
	if err == nil || !strings.Contains(err.Error(), "`land.method`") {
		t.Fatalf("the problem is not named: %v", err)
	}
}

func TestPathGlobProblem(t *testing.T) {
	for _, usable := range append([]string{"docs/**", "**/*.md", "a/b?c/[xy]"}, FixedHumanPaths...) {
		if problem := PathGlobProblem(usable); problem != "" {
			t.Errorf("%q: %s", usable, problem)
		}
	}
	for _, unusable := range []string{"", "/abs", "dir/", "a//b", "./a", "a**", "[z"} {
		if PathGlobProblem(unusable) == "" {
			t.Errorf("%q was accepted", unusable)
		}
	}
}
