package tidy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/project"
	"github.com/zima-oss/jackstay/internal/remote"
)

// treeItem is the report on one worktree.
type treeItem struct {
	Path     string   `json:"path"`
	Branch   string   `json:"branch"` // "" when detached
	Head     string   `json:"head"`
	Finished bool     `json:"finished"`
	Why      reason   `json:"why"`
	Remove   bool     `json:"remove"`
	Keep     []reason `json:"keep"`
	Holders  []holder `json:"holders,omitempty"`
}

// branchItem is the report on one local branch.
type branchItem struct {
	Name         string   `json:"name"`
	Tip          string   `json:"tip"`
	Finished     bool     `json:"finished"`
	Why          reason   `json:"why"`
	Remove       bool     `json:"remove"`
	Keep         []reason `json:"keep"`
	WithWorktree string   `json:"with_worktree,omitempty"`
}

// action is one thing --fix did or called off.
type action struct {
	ID     string `json:"id"`
	Target string `json:"target"`
	Detail string `json:"detail"`
}

// report is everything `jst tidy` says about a repository (its --json).
type report struct {
	Repository string       `json:"repository"`
	Default    string       `json:"default"`
	Scoped     bool         `json:"scoped"`
	Notes      []note       `json:"notes"`
	Worktrees  []treeItem   `json:"worktrees"`
	Branches   []branchItem `json:"branches"`
	Prunable   []string     `json:"prunable"`
	Done       []action     `json:"done,omitempty"`
	Failures   *int         `json:"failures,omitempty"`
}

// waiting is how many finished items the report lists.
func (found *report) waiting() int {
	count := len(found.Prunable)
	for _, item := range found.Worktrees {
		if item.Remove {
			count++
		}
	}
	for _, item := range found.Branches {
		if item.Remove {
			count++
		}
	}
	return count
}

// connector connects to the project's GitHub repository: the client, a note on the token (a
// GH_TOKEN ignored), or why GitHub cannot be asked.
type connector func(ctx context.Context, main string, settings config.Config) (*hub, string, error)

// connectGitHub is the project's account and repository (docs/design/implementation.md section 8.1).
func connectGitHub(ctx context.Context, main string, settings config.Config) (*hub, string, error) {
	section := settings.Section("github")
	repository, err := remote.RepositoryOf(ctx, gitcli.Git{Dir: main}, section)
	if err != nil {
		return nil, "", err
	}
	github, _ := settings.GitHub()
	client, token, err := githubapi.Connect(ctx, githubapi.OSEnvironment(), github.Account, repository.String())
	if err != nil {
		return nil, "", err
	}
	return &hub{client: client, owner: repository.Owner}, token.Note, nil
}

// tidier is one run of `jst tidy`: what it reads the world through, and GitHub once connected.
type tidier struct {
	connect   connector
	processes processView
	// workDir is this command's own real working folder: a session does not remove the worktree
	// it runs in.
	workDir  string
	pid      int
	noGitHub bool

	// connected says GitHub was asked for once; hub and hubNotes are what that gave.
	connected bool
	hub       *hub
	hubNotes  []note
}

// scope is what one survey looks at.
type scope struct {
	start     string
	branches  []string
	worktrees []string
	fetch     bool
}

// survey is what `jst tidy` would say about the repository around the scope's start. A
// *project.Problem (exit 2) when the request is wrong.
func (tidy *tidier) survey(ctx context.Context, wanted scope) (*report, error) {
	root, err := repo{dir: wanted.start}.line(ctx, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return nil, project.Fail("%s is not in a git repository", wanted.start)
	}
	listed, err := repo{dir: root}.run(ctx, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, project.Fail("git worktree list failed in %s: %s", root, gitSaid(err, 300))
	}
	trees := parseWorktrees(listed)
	if len(trees) == 0 {
		return nil, project.Fail("git worktree list gave no main checkout")
	}
	main := trees[0].path
	here := repo{dir: main}
	found := &report{Repository: main, Notes: []note{}, Worktrees: []treeItem{}, Branches: []branchItem{}, Prunable: []string{},
		Scoped: len(wanted.branches) > 0 || len(wanted.worktrees) > 0}

	settings, githubProblem := tidy.settings(main, found)
	remoteName, err := remote.Name(ctx, here.git(), remote.ConfiguredRemote(settings.Section("github")))
	var unclear *remote.UnclearError
	switch {
	case errors.Is(err, remote.ErrNoRemote):
		remoteName = ""
	case errors.As(err, &unclear):
		remoteName = ""
		found.Notes = append(found.Notes, note{ID: noteRemoteUnclear, Detail: err.Error() + ": nothing fetched, pr-merged not tried"})
	case err != nil:
		return nil, project.Fail("%v", err)
	}
	switch {
	case wanted.fetch && remoteName != "":
		if _, err := here.run(ctx, "fetch", "--prune", "--quiet", remoteName); err != nil {
			found.Notes = append(found.Notes, note{ID: noteFetchFailed,
				Detail: fmt.Sprintf("git fetch %s failed (%s); judged on the remote refs as they were", remoteName, gitSaid(err, 160))})
		}
	case wanted.fetch && unclear == nil:
		found.Notes = append(found.Notes, note{ID: noteNoRemote, Detail: "no remote: nothing fetched"})
	}
	judge := &judge{git: here}
	if named, err := remote.DefaultBranch(ctx, here.git(), remoteName); err == nil {
		judge.defaultRef, judge.defaultShort = named.Ref, named.Short
		found.Default = named.Ref
		judge.defaults = []string{named.Ref}
		if local := "refs/heads/" + named.Short; local != named.Ref && here.succeeds(ctx, "rev-parse", "--verify", "--quiet", local+"^{commit}") {
			judge.defaults = append(judge.defaults, local)
		}
	} else {
		found.Notes = append(found.Notes, note{ID: noteNoDefault,
			Detail: err.Error() + ": merged and adds-nothing not tried"})
	}
	judge.hub = tidy.github(ctx, main, settings, remoteName, githubProblem, found)

	namedTrees := map[string]bool{}
	for _, one := range wanted.worktrees {
		namedTrees[realPath(one)] = true
	}
	var unknown []string
	for place := range namedTrees {
		if !slices.ContainsFunc(trees, func(each tree) bool { return realPath(each.path) == place }) {
			unknown = append(unknown, place)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, project.Fail("not a worktree of this repository: %s", strings.Join(unknown, ", "))
	}
	heads, err := here.run(ctx, "for-each-ref", "--format=%(refname)", "refs/heads")
	if err != nil {
		return nil, project.Fail("git for-each-ref failed: %s", gitSaid(err, 300))
	}
	var local []string
	for _, ref := range strings.Split(string(heads), "\n") {
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok && name != "" {
			local = append(local, name)
		}
	}
	var missing []string
	for _, name := range wanted.branches {
		if !slices.Contains(local, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, project.Fail("no such local branch: %s", strings.Join(missing, ", "))
	}

	patterns := regenerablePatterns(main)
	var chosen []tree
	for _, each := range trees {
		named := namedTrees[realPath(each.path)]
		if each.bare || (len(namedTrees) > 0 && !named) ||
			(len(wanted.branches) > 0 && !slices.Contains(wanted.branches, each.branch) && !named) {
			continue
		}
		chosen = append(chosen, each)
	}
	checkedOut := map[string]string{}
	for _, each := range trees {
		if each.branch != "" {
			checkedOut[each.branch] = each.path
		}
	}
	for _, each := range chosen {
		switch {
		case each.prunable:
			found.Prunable = append(found.Prunable, each.path)
		case !each.main:
			found.Worktrees = append(found.Worktrees, tidy.judgeTree(ctx, judge, here, each, patterns))
		}
	}

	removable := map[string]bool{}
	for _, item := range found.Worktrees {
		if item.Remove {
			removable[item.Path] = true
		}
	}
	namedBranches := slices.Clone(wanted.branches)
	for _, each := range chosen {
		if each.branch != "" && namedTrees[realPath(each.path)] {
			namedBranches = append(namedBranches, each.branch)
		}
	}
	for _, name := range local {
		if (found.Scoped && !slices.Contains(namedBranches, name)) || name == judge.defaultShort {
			continue
		}
		if place, ok := checkedOut[name]; ok && !removable[place] {
			found.Branches = append(found.Branches, checkedOutItem(name, place, found.Worktrees))
			continue
		}
		tip := here.tipOf(ctx, name)
		decided := judge.branch(ctx, name, tip)
		item := branchItem{Name: name, Tip: tip, Finished: decided.finished, Why: decided.why, Remove: decided.finished,
			Keep: []reason{}, WithWorktree: checkedOut[name]}
		if !decided.finished {
			item.Keep = []reason{decided.why}
		}
		found.Branches = append(found.Branches, item)
	}
	found.Notes = append(found.Notes, judge.notes...)
	return found, nil
}

// checkedOutItem is a branch kept because a worktree that stays has it checked out.
func checkedOutItem(name, place string, items []treeItem) branchItem {
	detail := "checked out in " + place
	finishedThere := false
	for _, item := range items {
		if item.Path == place {
			finishedThere = item.Finished
			detail += " (kept: " + details(item.Keep) + ")"
		}
	}
	why := reason{ID: whyCheckedOut, Detail: detail}
	return branchItem{Name: name, Finished: finishedThere, Why: why, Keep: []reason{why}}
}

// judgeTree is the report on a worktree that is neither the main checkout nor gone.
func (tidy *tidier) judgeTree(ctx context.Context, judge *judge, here repo, each tree, patterns []string) treeItem {
	var decided verdict
	if each.branch != "" {
		decided = judge.branch(ctx, each.branch, here.tipOf(ctx, each.branch))
	} else {
		decided = detachedVerdict(ctx, here, each.head, judge.defaultRef)
	}
	item := treeItem{Path: each.path, Branch: each.branch, Head: each.head, Finished: decided.finished, Why: decided.why, Keep: []reason{}}
	if !decided.finished {
		item.Keep = append(item.Keep, decided.why)
	}
	if each.locked {
		item.Keep = append(item.Keep, reason{ID: whyLocked, Detail: "locked (`git worktree lock`)"})
	}
	if decided.finished {
		// The worktree's own jackstay.json counts too, as it does for the guard there.
		own := slices.Concat(patterns, regenerablePatterns(each.path))
		item.Keep = append(item.Keep, treeReasons(ctx, each.path, own)...)
		found, err := tidy.processes(ctx, each.path)
		if err == nil && inside(tidy.workDir, realPath(each.path)) &&
			!slices.ContainsFunc(found.holders, func(one holder) bool { return one.PID == tidy.pid }) {
			found.holders = append(found.holders, holder{PID: tidy.pid, Name: "jst tidy"})
		}
		item.Holders = found.holders
		item.Keep = append(item.Keep, processReasons(found, err)...)
	}
	item.Remove = len(item.Keep) == 0
	return item
}

// detachedVerdict is the verdict on a detached worktree: its commit on the default branch or held
// by a branch or tag.
func detachedVerdict(ctx context.Context, here repo, head, defaultRef string) verdict {
	if head == "" {
		return kept(whyNoCommit, "no commit")
	}
	if defaultRef != "" && here.succeeds(ctx, "merge-base", "--is-ancestor", head, defaultRef) {
		return finished(whyDetachedOnMain, "detached at a commit on %s", defaultRef)
	}
	held, err := here.line(ctx, "for-each-ref", "--contains", head, "--format=%(refname)", "refs/heads", "refs/tags", "refs/remotes")
	if err == nil && held != "" {
		return finished(whyDetachedHeld, "detached at a commit a branch or tag holds")
	}
	return kept(whyDetachedUnheld, "detached at a commit no branch or tag holds")
}

// settings is the project's jackstay.json as tidy reads it, and why its `github` section cannot be used
// ("" when it can). A file that cannot be read gives no settings and a note.
func (tidy *tidier) settings(main string, found *report) (config.Config, string) {
	_, loaded, err := config.Load(main, []string{"github"})
	if err == nil {
		return loaded, ""
	}
	githubProblem := err.Error()
	_, loaded, err = config.Load(main, []string{})
	if err != nil {
		found.Notes = append(found.Notes, note{ID: noteConfig, Detail: err.Error() + ": its guard.regenerable is not applied"})
		return config.Config{}, githubProblem
	}
	return loaded, githubProblem
}

// github is GitHub for this run, connected once; nil when it is not asked, with the reason noted.
func (tidy *tidier) github(ctx context.Context, main string, settings config.Config, remoteName, githubProblem string,
	found *report) *hub {
	if !tidy.connected {
		tidy.connected = true
		switch {
		case tidy.noGitHub:
			tidy.hubNotes = append(tidy.hubNotes, note{ID: noteGitHubOff, Detail: "--no-github: pr-merged not tried"})
		case githubProblem != "":
			tidy.hubNotes = append(tidy.hubNotes, note{ID: noteGitHubMissing, Detail: githubProblem + ": pr-merged not tried"})
		case remoteName == "" && settings.Section("github")["repository"] == nil:
			tidy.hubNotes = append(tidy.hubNotes, note{ID: noteGitHubMissing, Detail: "no remote: pr-merged not tried"})
		default:
			connected, tokenNote, err := tidy.connect(ctx, main, settings)
			if err != nil {
				tidy.hubNotes = append(tidy.hubNotes, note{ID: noteGitHubMissing, Detail: githubReason(err) +
					": pr-merged not tried; a branch only GitHub could show finished is kept"})
				break
			}
			if tokenNote != "" {
				tidy.hubNotes = append(tidy.hubNotes, note{ID: noteGitHubToken, Detail: tokenNote})
			}
			tidy.hub = connected
		}
	}
	found.Notes = append(found.Notes, tidy.hubNotes...)
	return tidy.hub
}

func githubReason(err error) string {
	var missing *githubapi.AccountMissingError
	if errors.As(err, &missing) {
		return "set github.account in jackstay.json (the account jackstay acts as): GitHub is not asked without it"
	}
	return err.Error()
}
