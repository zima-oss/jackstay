package guard

import (
	"path/filepath"
	"slices"
	"testing"
)

// The rules gh-pr-merge and glab-mr-merge hold whether or not the project has a `land` section
// (land.md section 3.3): the guard does not read the section.
func TestAMergeOnTheHostIsRefusedWithAndWithoutALandSection(t *testing.T) {
	withLand, without := realFolder(t), realFolder(t)
	write(t, filepath.Join(withLand, "jackstay.json"), `{"jackstay_schema": 2, "land": {"policy": "merge-when-green"}}`)
	write(t, filepath.Join(without, "jackstay.json"), `{"jackstay_schema": 2}`)
	cases := map[string]string{
		"gh pr merge 12 --squash":                  "gh-pr-merge",
		"gh pr merge --auto":                       "gh-pr-merge",
		"gh pr merge":                              "gh-pr-merge",
		"gh -R owner/repo pr merge 3":              "gh-pr-merge",
		"gh --repo x/y pr merge":                   "gh-pr-merge",
		"gh --repo=x/y pr merge 4":                 "gh-pr-merge",
		"gh pr -R x/y merge 4":                     "gh-pr-merge",
		"/opt/homebrew/bin/gh pr merge 5":          "gh-pr-merge",
		"glab mr merge 5":                          "glab-mr-merge",
		"glab mr merge --squash":                   "glab-mr-merge",
		"glab -R x/y mr merge 5":                   "glab-mr-merge",
		`bash -c "gh pr merge 12 --squash"`:        "gh-pr-merge",
		`bash -c 'glab mr merge --squash'`:         "glab-mr-merge",
		"git push && gh pr merge 12":               "gh-pr-merge",
		"gh pr checks 12 | cat && glab mr merge 5": "glab-mr-merge",
		"echo go; gh pr merge 12 --merge":          "gh-pr-merge",
		"env GH_TOKEN=x gh pr merge 12":            "gh-pr-merge",
		"sudo gh pr merge 12":                      "gh-pr-merge",
		"(gh pr merge 12)":                         "gh-pr-merge",
		"xargs gh pr merge":                        "gh-pr-merge",
	}
	for _, folder := range []string{withLand, without} {
		for command, row := range cases {
			expectRules(t, command, folder, []string{row})
		}
	}
}

func TestOtherGhAndGlabCommandsAreNotRefused(t *testing.T) {
	folder := realFolder(t)
	for _, command := range []string{
		"gh pr view 12", "gh pr list", "gh pr checks 12", "gh pr create --fill", "gh pr status", "gh pr comment 3 --body merge",
		"gh -R owner/repo pr view 3", "gh issue list", "gh repo view", "glab mr view 5", "glab mr list", "glab mr create",
		"gh help pr merge", "gh pr merge --help", "gh pr merge -h", "glab mr merge --help", "glab help mr merge",
		"bash -c 'gh pr view 1'", "git merge main", "echo gh pr merge",
	} {
		expectRules(t, command, folder, nil)
	}
}

func TestAHostMergeRuleIsConfigurable(t *testing.T) {
	folder := realFolder(t)
	settings := func(level string) Settings {
		return Settings{Folder: folder, Home: "/Users/someone", ProjectRoot: folder, Git: NoGit{},
			Guard: map[string]any{"rules": map[string]any{"gh-pr-merge": map[string]any{"level": level},
				"glab-mr-merge": map[string]any{"level": level}}}}
	}
	for _, command := range []string{"gh pr merge 1", "glab mr merge 1"} {
		if got := Decide(command, settings("refuse")).Rules(); len(got) != 1 {
			t.Errorf("%q at refuse: %v", command, got)
		}
		if got := Decide(command, settings("off")).Rules(); !slices.Equal(got, nil) && len(got) != 0 {
			t.Errorf("%q at off: %v", command, got)
		}
	}
}
