package tidy

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/githubapi/githubfake"
)

// fakeAncestry answers git's questions from tables: commits ahead of each default ref, the refs
// the branch adds nothing to, and which heads hold which tips.
type fakeAncestry struct {
	ahead0      map[string]bool // ref → tip has no commit that ref lacks
	addsNothing map[string]bool // ref → merging tip into it changes no file
	holds       map[string]bool // "head tip"
}

func (fake fakeAncestry) aheadOf(ref string) int {
	if fake.ahead0[ref] {
		return 0
	}
	return 1
}

type ancestryAdapter struct{ fake fakeAncestry }

func (adapter ancestryAdapter) ahead(_ context.Context, ref, _ string) (int, bool) {
	return adapter.fake.aheadOf(ref), true
}

func (adapter ancestryAdapter) addsNothing(_ context.Context, base, _ string) bool {
	return adapter.fake.addsNothing[base]
}

func (adapter ancestryAdapter) holds(_ context.Context, head, tip string) bool {
	return adapter.fake.holds[head+" "+tip]
}

const (
	tip   = "1111111111111111111111111111111111111111"
	older = "2222222222222222222222222222222222222222"
	newer = "3333333333333333333333333333333333333333"
)

func pullMerged(number int, head, name string) githubapi.PullRequest {
	return githubapi.PullRequest{Number: number, State: "closed", Merged: true, HeadSHA: head, Head: name}
}

func pullOpen(number int) githubapi.PullRequest {
	return githubapi.PullRequest{Number: number, State: "open", HeadSHA: "x"}
}

type pulls = []githubapi.PullRequest

func TestBranchVerdicts(t *testing.T) {
	remoteMain, localMain := "refs/remotes/origin/main", "refs/heads/main"
	tests := []struct {
		name      string
		branch    string
		tip       string
		git       fakeAncestry
		noGitHub  bool
		byHead    githubfake.Result[pulls]
		search    *githubfake.Result[pulls]
		want      string
		finished  bool
		detail    string
		noteID    string
		searchHit bool
	}{
		{name: "the default branch", branch: "main", tip: tip, want: whyDefault},
		{name: "no such branch", branch: "gone", want: whyNoBranch},
		{name: "merged on the remote's main", branch: "f", tip: tip, git: fakeAncestry{ahead0: map[string]bool{remoteMain: true}},
			byHead: githubfake.Ok(pulls{}), search: &githubfake.Result[pulls]{}, want: whyMerged, finished: true, detail: remoteMain,
			searchHit: true},
		// An open pull request found only by the tip keeps a
		// branch git calls merged or adding nothing (docs/design/implementation.md section 8.4).
		{name: "an open pull request found by the tip keeps a merged branch", branch: "pr-9", tip: tip,
			git: fakeAncestry{ahead0: map[string]bool{remoteMain: true}}, byHead: githubfake.Ok(pulls{}),
			search: &githubfake.Result[pulls]{Value: pulls{pullOpen(9)}}, want: whySearchOpen, detail: "#9 holds this tip", searchHit: true},
		{name: "an open pull request found by the tip keeps a branch that adds nothing", branch: "renamed", tip: tip,
			git: fakeAncestry{addsNothing: map[string]bool{remoteMain: true}}, byHead: githubfake.Ok(pulls{}),
			search: &githubfake.Result[pulls]{Value: pulls{pullOpen(9)}}, want: whySearchOpen, detail: "#9 holds this tip", searchHit: true},
		{name: "a failed search keeps even a merged branch", branch: "pr-9", tip: tip,
			git: fakeAncestry{ahead0: map[string]bool{remoteMain: true}}, byHead: githubfake.Ok(pulls{}),
			search: &githubfake.Result[pulls]{Err: errors.New("502")}, want: whySearchUnknown, noteID: noteSearchFailed, searchHit: true},
		{name: "merged into the local main before the push", branch: "f", tip: tip, git: fakeAncestry{ahead0: map[string]bool{localMain: true}},
			noGitHub: true, want: whyMerged, finished: true, detail: localMain},
		{name: "squash-merged adds nothing", branch: "f", tip: tip, git: fakeAncestry{addsNothing: map[string]bool{remoteMain: true}},
			noGitHub: true, want: whyAddsNothing, finished: true},
		{name: "an open pull request keeps even a merged branch", branch: "f", tip: tip,
			git: fakeAncestry{ahead0: map[string]bool{remoteMain: true}}, byHead: githubfake.Ok(pulls{pullOpen(3)}), want: whyPullOpen, detail: "#3 is open"},
		{name: "GitHub failing keeps it and is said", branch: "f", tip: tip, git: fakeAncestry{ahead0: map[string]bool{remoteMain: true}},
			byHead: githubfake.Fail[pulls](errors.New("502")), want: whyGitHubUnknown, noteID: noteGitHubFailed},
		{name: "a merged pull request with this tip", branch: "f", tip: tip, byHead: githubfake.Ok(pulls{pullMerged(7, tip, "")}),
			want: whyPullMerged, finished: true, detail: "#7 was merged with this very tip"},
		{name: "a merged pull request whose head holds the tip", branch: "f", tip: tip,
			git: fakeAncestry{holds: map[string]bool{newer + " " + tip: true}}, byHead: githubfake.Ok(pulls{pullMerged(7, newer, "")}),
			want: whyPullMerged, finished: true, detail: "which holds this tip"},
		{name: "commits after the merged pull request", branch: "f", tip: tip, byHead: githubfake.Ok(pulls{pullMerged(8, older, "")}),
			want: whyAfterMerged, detail: "#8 was merged at 22222222"},
		{name: "a branch of another name holding a merged tip", branch: "pr-7", tip: tip, byHead: githubfake.Ok(pulls{}),
			search: &githubfake.Result[pulls]{Value: pulls{pullMerged(7, tip, "feature")}}, want: whyPullMerged, finished: true,
			detail: "#7 (from feature) was merged with this very tip", searchHit: true},
		{name: "a tip held by an open pull request", branch: "copy", tip: tip, byHead: githubfake.Ok(pulls{}),
			search: &githubfake.Result[pulls]{Value: pulls{pullMerged(7, tip, ""), pullOpen(8)}}, want: whySearchOpen,
			detail: "#8 holds this tip and is open", searchHit: true},
		{name: "a tip merged under another head with more here", branch: "grown", tip: tip, byHead: githubfake.Ok(pulls{}),
			search: &githubfake.Result[pulls]{Value: pulls{}}, want: whyOwnWork, searchHit: true},
		{name: "a failed search keeps it and is said", branch: "lonely", tip: tip, byHead: githubfake.Ok(pulls{}),
			search: &githubfake.Result[pulls]{Err: errors.New("502")}, want: whySearchUnknown, noteID: noteSearchFailed, searchHit: true},
		{name: "a refused search keeps it and is said", branch: "lonely", tip: tip, byHead: githubfake.Ok(pulls{}),
			search: &githubfake.Result[pulls]{Err: &githubapi.RateLimitedError{Until: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}},
			want:   whySearchUnknown, noteID: noteSearchRefused, searchHit: true},
		{name: "own work without GitHub", branch: "own", tip: tip, noGitHub: true, want: whyOwnWork},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := githubfake.New()
			fake.OnPullRequestsByHead("o:"+test.branch, "all", test.byHead)
			if test.search != nil {
				fake.OnSearchPullRequestsBySHA(test.tip, *test.search)
			}
			judging := &judge{git: ancestryAdapter{test.git}, defaultRef: remoteMain, defaultShort: "main", defaults: []string{remoteMain, localMain}}
			if !test.noGitHub {
				judging.hub = &hub{client: fake, owner: "o"}
			}
			got := judging.branch(context.Background(), test.branch, test.tip)
			if got.why.ID != test.want || got.finished != test.finished || !strings.Contains(got.why.Detail, test.detail) {
				t.Errorf("got %+v, want %s (finished %v, %q)", got, test.want, test.finished, test.detail)
			}
			if test.noteID != "" && !slices.ContainsFunc(judging.notes, func(said note) bool {
				return said.ID == test.noteID && strings.Contains(said.Detail, test.branch)
			}) {
				t.Errorf("notes %+v, want %s", judging.notes, test.noteID)
			}
			searched := slices.ContainsFunc(fake.Calls(), func(call githubfake.Call) bool { return call.Op == "SearchPullRequestsBySHA" })
			if searched != test.searchHit {
				t.Errorf("searched %v, want %v: %+v", searched, test.searchHit, fake.Calls())
			}
		})
	}
}

func TestParseWorktrees(t *testing.T) {
	listed := "worktree /r\x00HEAD aaa\x00branch refs/heads/main\x00\x00" +
		"worktree /w one\x00HEAD bbb\x00detached\x00locked because\x00\x00" +
		"worktree /gone\x00HEAD ccc\x00branch refs/heads/feature/x\x00prunable gitdir file points to non-existent location\x00\x00"
	got := parseWorktrees([]byte(listed))
	want := []tree{
		{path: "/r", head: "aaa", branch: "main", main: true},
		{path: "/w one", head: "bbb", locked: true},
		{path: "/gone", head: "ccc", branch: "feature/x", prunable: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestStatusReasons(t *testing.T) {
	tests := []struct {
		name   string
		status string
		listed string
		want   []string
	}{
		{"clean", "", "H a.txt\x00", nil},
		{"regenerable ignored only", "!! .build/\x00", "H a.txt\x00", nil},
		{"untracked and modified", "?? new.txt\x00 M a.txt\x00", "H a.txt\x00", []string{whyUncommitted}},
		{"a rename's source is not a path of its own", "R  b.txt\x00a.txt\x00", "", []string{whyUncommitted}},
		{"an ignored .env", "!! .env\x00", "", []string{whyIgnoredKept}},
		{"skip-worktree", "", "S a.txt\x00H b.txt\x00", []string{whyHiddenEdits}},
		{"assume-unchanged", "", "h a.txt\x00", []string{whyHiddenEdits}},
		{"everything", "?? n\x00!! .env\x00", "s a.txt\x00", []string{whyHiddenEdits, whyUncommitted, whyIgnoredKept}},
	}
	patterns := slices.Concat(regenerableNames, sessionStateNames)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := statusReasons([]byte(test.status), []byte(test.listed), func(name string) bool { return !regenerable(name, patterns) })
			var ids []string
			for _, one := range got {
				ids = append(ids, one.ID)
			}
			if !slices.Equal(ids, test.want) {
				t.Errorf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestRegenerable(t *testing.T) {
	patterns := slices.Concat(regenerableNames, sessionStateNames, []string{".build-*"})
	for path, want := range map[string]bool{
		".build/":                    true,
		"Packages/Core/.build/x":     true,
		"pkg/thing.egg-info/":        true,
		"Pkg/.build-graph-check/x":   true,
		".claude/.cc-writes/x":       true,
		".omc/state/s.json":          true,
		".omc/logs":                  true,
		".omc/plans/next.md":         false,
		".omc/":                      false,
		".env":                       false,
		"config/settings.local.json": false,
	} {
		if got := regenerable(path, patterns); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}

func TestProcessReasons(t *testing.T) {
	tests := []struct {
		name  string
		found seen
		err   error
		want  []string
	}{
		{"nobody, the view complete", seen{complete: true}, nil, nil},
		{"a process works there", seen{holders: []holder{{PID: 123, Name: "claude"}}, complete: true}, nil, []string{whyInUse}},
		{"lsof's view is partial (the sandbox)", seen{}, nil, []string{whyProcessPartial}},
		{"partial, and one seen", seen{holders: []holder{{PID: 9}}}, nil, []string{whyProcessPartial, whyInUse}},
		{"one process never named", seen{unseen: []holder{{PID: 77, Name: "top"}}}, nil, []string{whyProcessPartial}},
		{"the table cannot be read", seen{}, errors.New("sysctl"), []string{whyProcessUnusable}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var ids []string
			for _, one := range processReasons(test.found, test.err) {
				ids = append(ids, one.ID)
			}
			if !slices.Equal(ids, test.want) {
				t.Errorf("got %v, want %v", ids, test.want)
			}
		})
	}
	if got := processReasons(seen{holders: []holder{{PID: 123, Name: "claude"}}, complete: true}, nil)[0].Detail; got != "in use: claude (123)" {
		t.Errorf("detail %q", got)
	}
	// The processes a view could not see are named, so the owner can tell which one kept it.
	got := processReasons(seen{unseen: []holder{{PID: 77, Name: "top"}, {PID: 78}}}, nil)[0].Detail
	if !strings.Contains(got, "top (77), ? (78)") {
		t.Errorf("detail %q", got)
	}
	many := make([]holder, 9)
	if got := processReasons(seen{unseen: many}, nil)[0].Detail; !strings.Contains(got, "agent sandbox") {
		t.Errorf("many unseen: detail %q", got)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		args []string
		want request
		bad  bool
	}{
		{args: nil, want: request{dir: "."}},
		{args: []string{"--fix", "--branch", "a", "--branch=b", "--worktree", "/w", "--no-fetch", "--no-github", "--json", "/repo"},
			want: request{fix: true, noFetch: true, noGitHub: true, json: true, branches: []string{"a", "b"}, worktrees: []string{"/w"}, dir: "/repo"}},
		{args: []string{"--", "-odd"}, want: request{dir: "-odd"}},
		{args: []string{"--branch"}, bad: true},
		{args: []string{"--branch="}, bad: true},
		{args: []string{"--force"}, bad: true},
		{args: []string{"--fix=yes"}, bad: true},
		{args: []string{"a", "b"}, bad: true},
	}
	for _, test := range tests {
		got, err := parse(test.args)
		if test.bad != (err != nil) {
			t.Errorf("%v: error %v", test.args, err)
			continue
		}
		if !test.bad && (got.fix != test.want.fix || got.noFetch != test.want.noFetch || got.noGitHub != test.want.noGitHub ||
			got.json != test.want.json || got.dir != test.want.dir || !slices.Equal(got.branches, test.want.branches) ||
			!slices.Equal(got.worktrees, test.want.worktrees)) {
			t.Errorf("%v: got %+v, want %+v", test.args, got, test.want)
		}
	}
	if _, err := parse([]string{"-h"}); !errors.Is(err, errHelp) {
		t.Errorf("-h: %v", err)
	}
}

func TestGitHubIsConnectedOnceAndWhyNotIsSaid(t *testing.T) {
	connected := &hub{client: githubfake.New(), owner: "o"}
	tests := []struct {
		name     string
		noGitHub bool
		problem  string
		remote   string
		result   error
		want     string
		hub      bool
	}{
		{name: "--no-github", noGitHub: true, remote: "origin", want: noteGitHubOff},
		{name: "an unusable github section", problem: "jackstay.json: `github.account` is missing", remote: "origin", want: noteGitHubMissing},
		{name: "no remote", want: noteGitHubMissing},
		{name: "no account", remote: "origin", result: &githubapi.AccountMissingError{}, want: noteGitHubMissing},
		{name: "not signed in", remote: "origin", result: &githubapi.NotSignedInError{Account: "a"}, want: noteGitHubMissing},
		{name: "connected", remote: "origin", hub: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			tidy := &tidier{noGitHub: test.noGitHub, connect: func(context.Context, string, config.Config) (*hub, string, error) {
				calls++
				if test.result != nil {
					return nil, "", test.result
				}
				return connected, "", nil
			}}
			for range 2 {
				found := &report{}
				got := tidy.github(context.Background(), "/r", config.Config{}, test.remote, test.problem, found)
				if (got != nil) != test.hub {
					t.Errorf("hub %v", got)
				}
				if test.want != "" && !slices.ContainsFunc(found.Notes, func(said note) bool { return said.ID == test.want }) {
					t.Errorf("notes %+v, want %s", found.Notes, test.want)
				}
			}
			if calls > 1 {
				t.Errorf("connected %d times", calls)
			}
		})
	}
}

func TestReportJSONShape(t *testing.T) {
	failures := 1
	found := report{Repository: "/r", Default: "refs/remotes/origin/main", Notes: []note{{ID: noteNoRemote, Detail: "no remote"}},
		Worktrees: []treeItem{{Path: "/w", Branch: "f", Head: tip, Finished: true, Why: reason{ID: whyMerged}, Keep: []reason{{ID: whyInUse}},
			Holders: []holder{{PID: 5, Name: "sh"}}}},
		Branches: []branchItem{{Name: "f", Tip: tip, Why: reason{ID: whyCheckedOut}, Keep: []reason{{ID: whyCheckedOut}}}},
		Prunable: []string{}, Done: []action{{ID: didKeepBranch, Target: "f"}}, Failures: &failures}
	text, err := json.Marshal(found)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(text, &shape); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"repository", "default", "scoped", "notes", "worktrees", "branches", "prunable", "done", "failures"} {
		if _, ok := shape[key]; !ok {
			t.Errorf("no %s in %s", key, text)
		}
	}
	tree, _ := shape["worktrees"].([]any)[0].(map[string]any)
	for _, key := range []string{"path", "branch", "head", "finished", "why", "remove", "keep", "holders"} {
		if _, ok := tree[key]; !ok {
			t.Errorf("no worktrees[0].%s in %s", key, text)
		}
	}
	branch, _ := shape["branches"].([]any)[0].(map[string]any)
	for _, key := range []string{"name", "tip", "finished", "why", "remove", "keep"} {
		if _, ok := branch[key]; !ok {
			t.Errorf("no branches[0].%s in %s", key, text)
		}
	}
	if why, _ := branch["why"].(map[string]any); why["id"] != whyCheckedOut {
		t.Errorf("why %v", branch["why"])
	}
	if found.waiting() != 0 {
		t.Errorf("waiting %d", found.waiting())
	}
}
