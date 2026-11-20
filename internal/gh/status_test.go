package gh

import (
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/githubapi"
)

// statusBench is a checkout on feat at A, pushed, with #7 open from it and its checks.
func statusBench(t *testing.T, checks ...githubapi.Check) *harness {
	t.Helper()
	bench := newHarness(t, checkout(shaA))
	bench.hub.OnDefaultBranch(answer("main"))
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{pull(7, "feat", shaA)}))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, checks)
	return bench
}

func TestLandStatus(t *testing.T) {
	git := checkout(shaA)
	git.on("rev-list --count origin/feat..feat", exit(128))
	bench := newHarness(t, git)
	bench.hub.OnDefaultBranch(answer("main"))
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}))
	stdout, _ := bench.expect(exitFine, "land-status")
	for _, line := range []string{"repo    owner/checkout", "branch  feat  (base main)", "tree    clean", "ahead   no upstream yet",
		"pr      none open for this branch"} {
		contains(t, stdout, line)
	}

	bench = statusBench(t, pending("test"))
	bench.hub.mergeStates = []string{"blocked"}
	stdout, _ = bench.expect(exitPending, "land-status")
	contains(t, stdout, "ahead   0 commit(s) unpushed")
	contains(t, stdout, "pr      #7 BLOCKED — t")
	if bench.clock.slept() != time.Duration(0) || len(bench.hub.Merges()) != 0 {
		t.Fatal("land status only reads, once")
	}
}

func TestImplicitPullRequestSelectionRefusesMultipleBases(t *testing.T) {
	for _, operation := range []string{"wait", "status", "land-status"} {
		t.Run(operation, func(t *testing.T) {
			bench := newHarness(t, checkout(shaA))
			bench.hub.OnDefaultBranch(answer("main"))
			main := pull(7, "feat", shaA)
			alternate := pull(8, "feat", shaA)
			alternate.Base = "release"
			bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{alternate, main}))
			_, stderr := bench.expect(exitRequest, operation)
			contains(t, stderr, "#7 into main, #8 into release")
			contains(t, stderr, "use --change with jst land or jst land status")
		})
	}
}

func TestLandStatusChangeSelectsAnOpenPullRequestWithItsBase(t *testing.T) {
	bench := statusBench(t, passed("test"))
	selected := pull(8, "feat", shaA)
	selected.Base = "release"
	bench.hub.OnPullRequest(8, answer(selected))
	stdout, stderr := bench.expect(exitFine, "land-status", "--change", "https://github.com/owner/checkout/pull/8")
	contains(t, stdout, "branch  feat  (base release)")
	contains(t, stdout, "pr      #8 CLEAN — t")
	lacks(t, stderr, "jst gh")
}

func TestLandStatusRefusesAnUnrelatedChange(t *testing.T) {
	for _, test := range []struct {
		selection string
		update    func(*githubapi.PullRequest)
		want      string
	}{
		{"https://github.com/other/checkout/pull/8", nil, "--change names other/checkout"},
		{"999999999999999999999999999999", nil, "--change takes a positive"},
		{"8", func(pull *githubapi.PullRequest) { pull.Head = "different" }, "#8 is not an open pull request from feat"},
		{"8", func(pull *githubapi.PullRequest) { pull.HeadRepository = "other/checkout" }, "#8 is not an open pull request"},
		{"8", func(pull *githubapi.PullRequest) { pull.State = "closed" }, "#8 is not an open pull request"},
	} {
		bench := statusBench(t)
		selected := pull(8, "feat", shaA)
		if test.update != nil {
			test.update(&selected)
		}
		bench.hub.OnPullRequest(8, answer(selected))
		_, stderr := bench.expect(exitRequest, "land-status", "--change", test.selection)
		contains(t, stderr, "jst land status: ")
		contains(t, stderr, test.want)
	}
}

func TestLandStatusTakesNoPullRequestFlag(t *testing.T) {
	bench := statusBench(t)
	_, stderr := bench.expect(exitRequest, "land-status", "--pr", "7")
	contains(t, stderr, "flag provided but not defined: -pr")
}

func TestLandFindNamesItsCommand(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	_, stderr := bench.expect(exitRequest, "land-find")
	contains(t, stderr, "jst land find: jst land find needs --head or --search")
}

func TestParsePullURL(t *testing.T) {
	for text, want := range map[string]struct {
		repository string
		number     int
	}{
		"https://github.com/owner/name/pull/12":        {"owner/name", 12},
		"github.com/owner/name/pull/3/files":           {"owner/name", 3},
		" https://www.github.com/owner/name/pull/4#x ": {"owner/name", 4},
		"http://github.com/Owner/Name.js/pull/5?w=1":   {"Owner/Name.js", 5},
	} {
		repository, number, err := ParsePullURL(text)
		if err != nil || repository != want.repository || number != want.number {
			t.Errorf("%q: %s #%d %v", text, repository, number, err)
		}
	}
	for _, text := range []string{"7", "feat", "https://github.com/owner/name", "https://github.com/owner/name/pull/0",
		"https://gitlab.com/owner/name/pull/1", "https://github.com/owner/name/issues/1", ""} {
		if repository, number, err := ParsePullURL(text); err == nil {
			t.Errorf("%q: %s #%d, want an error", text, repository, number)
		}
	}
}
