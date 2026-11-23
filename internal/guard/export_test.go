package guard

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestWordsAreTheSimpleCommandsAsWritten(t *testing.T) {
	cases := map[string][][]string{
		"make test":                          {{"make", "test"}},
		"cd x && make test 2>&1 | tail -5":   {{"cd", "x"}, {"make", "test"}, {"tail", "-5"}},
		"CI=1 npm test":                      {{"npm", "test"}},
		"for i in 1 2; do make test; done":   {{"make", "test"}},
		"if true; then swift test; fi":       {{"true"}, {"swift", "test"}},
		"time make test":                     {{"make", "test"}},
		"! make test":                        {{"make", "test"}},
		"(cd y && just gate) > log":          {{"cd", "y"}, {"just", "gate"}},
		`git commit -m 'make test is green'`: {{"git", "commit", "-m", "make test is green"}},
		`echo "a $X b" \"c\"`:                {{"echo", "a $X b", `"c"`}},
		`echo "x\$y \q"`:                     {{"echo", `x$y \q`}},
		`echo $(make test) ${HOME}/a`:        {{"echo", "$(make test)", "${HOME}/a"}},
		`printf $'a\tb'`:                     {{"printf", "a\tb"}},
		"cat <<EOF\nmake test\nEOF":          {{"cat"}},
		"just gate # make test":              {{"just", "gate"}},
	}
	for line, want := range cases {
		got, err := Words(line)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q, want %q", line, got, want)
		}
	}
	if _, err := Words("echo 'open"); !errors.Is(err, ErrUnreadable) {
		t.Errorf("an open quote: %v", err)
	}
}

func TestNoGitCountsForcedBranchDeletesAndNotSideTaking(t *testing.T) {
	decide := func(line string) []string {
		return Decide(line, Settings{Folder: "/nowhere/project", Environ: map[string]string{}, Home: "/Users/someone", Git: NoGit{},
			Deadline: testBudget}).Rules()
	}
	if rules := decide("git branch -D feature"); len(rules) == 0 {
		t.Errorf("a forced branch delete: nothing found")
	}
	if rules := decide("git checkout --theirs -- a.txt"); len(rules) != 0 {
		t.Errorf("a side-taking checkout: %v", rules)
	}
	if rules := decide("git reset --hard"); !slices.Contains(rules, "git-reset-hard") {
		t.Errorf("git reset --hard: %v", rules)
	}
	for _, line := range []string{"git restore -- go.mod", "git checkout -- a.txt", "git checkout .", "git restore a.txt", "git checkout main"} {
		if rules := decide(line); len(rules) != 0 {
			t.Errorf("%s needs that day's tree, so it is not counted: %v", line, rules)
		}
	}
	if rules := decide("git clean -fdx"); len(rules) == 0 {
		t.Errorf("git clean -fdx: nothing found")
	}
	if rules := decide("git status"); len(rules) != 0 {
		t.Errorf("git status: %v", rules)
	}
}
