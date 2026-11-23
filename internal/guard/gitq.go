package guard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/gitcli"
)

// An answer is git's answer to a yes-or-no question: yes, no, or it could not say.
type answer int

const (
	answerUnsure answer = iota
	answerYes
	answerNo
)

func answerOf(truth bool) answer {
	if truth {
		return answerYes
	}
	return answerNo
}

// The modes of Changed: what a command writes over.
const (
	modeIndex    = "index"    // `git checkout -- p`, `git restore p`: unstaged edits to tracked files
	modeTree     = "tree"     // another commit's files: anything uncommitted, untracked or ignored
	modeWorktree = "worktree" // a whole checkout: anything uncommitted or untracked
)

// Questions are what the rules ask of git (guard.md section 6). Each is asked of the folder a
// command runs in; a question git cannot answer counts as the unsafe answer.
type Questions interface {
	// Held: whether another ref still holds the commits of the local branch (unsure: no such branch).
	Held(folder, branch string) answer
	// BranchExists: whether a local branch of that name exists.
	BranchExists(folder, name string) answer
	// Conflicts: whether a merge with conflicted paths is under way.
	Conflicts(folder string) bool
	// Unrelated: the edited paths under `paths` that are not in conflict; false when git cannot say.
	Unrelated(folder string, paths []string) ([]string, bool)
	// Changed: what writing over `paths` (mode) would lose; false when git cannot say.
	Changed(folder string, paths []string, mode string) ([]string, bool)
	// AddsNothing: whether merging the branch into the default branch would change no file.
	AddsNothing(folder, branch string) bool
	// Checkout: the top of the checkout at the folder and its repository's common folder.
	Checkout(folder string) (top, common string, ok bool)
	// RefExists: whether the repository at the folder is one and has the ref.
	RefExists(folder, ref string) answer
	// CardsPush: what is wrong with the commits a push would add to the cards branch; false when
	// it cannot be told.
	CardsPush(folder string, push CardPush) ([]string, bool)
}

// A CardPush is one push the Card rule asks about.
type CardPush struct {
	Remote, Source, Target string // Remote "" is the branch's own; Target "" its upstream; "HEAD" the current branch
	Branch                 string // the project's cards branch
	Section                map[string]any
	SectionBroken          bool
}

// answers asks each question once per call.
type answers struct {
	git  Questions
	seen map[string]any
}

func newAnswers(git Questions) *answers {
	if git == nil {
		git = NewGit(nil)
	}
	return &answers{git: git, seen: map[string]any{}}
}

func remember[Result any](cache *answers, key string, ask func() Result) Result {
	if found, ok := cache.seen[key]; ok {
		typed, _ := found.(Result)
		return typed
	}
	result := ask()
	cache.seen[key] = result
	return result
}

type listAnswer struct {
	paths []string
	ok    bool
}

func (cache *answers) held(folder, branch string) answer {
	return remember(cache, "held\x00"+folder+"\x00"+branch, func() answer { return cache.git.Held(folder, branch) })
}

func (cache *answers) branchExists(folder, name string) answer {
	return remember(cache, "branch\x00"+folder+"\x00"+name, func() answer { return cache.git.BranchExists(folder, name) })
}

func (cache *answers) conflicts(folder string) bool {
	return remember(cache, "conflicts\x00"+folder, func() bool { return cache.git.Conflicts(folder) })
}

func (cache *answers) unrelated(folder string, paths []string) ([]string, bool) {
	found := remember(cache, "unrelated\x00"+folder+"\x00"+strings.Join(paths, "\x00"), func() listAnswer {
		paths, ok := cache.git.Unrelated(folder, paths)
		return listAnswer{paths, ok}
	})
	return found.paths, found.ok
}

func (cache *answers) changed(folder string, paths []string, mode string) ([]string, bool) {
	found := remember(cache, "changed\x00"+folder+"\x00"+mode+"\x00"+strings.Join(paths, "\x00"), func() listAnswer {
		paths, ok := cache.git.Changed(folder, paths, mode)
		return listAnswer{paths, ok}
	})
	return found.paths, found.ok
}

func (cache *answers) addsNothing(folder, branch string) bool {
	return remember(cache, "adds\x00"+folder+"\x00"+branch, func() bool { return cache.git.AddsNothing(folder, branch) })
}

type checkoutAnswer struct {
	top, common string
	ok          bool
}

func (cache *answers) checkout(folder string) (string, string, bool) {
	found := remember(cache, "checkout\x00"+folder, func() checkoutAnswer {
		top, common, ok := cache.git.Checkout(folder)
		return checkoutAnswer{top, common, ok}
	})
	return found.top, found.common, found.ok
}

func (cache *answers) refExists(folder, ref string) answer {
	return remember(cache, "ref\x00"+folder+"\x00"+ref, func() answer { return cache.git.RefExists(folder, ref) })
}

func (cache *answers) cardsPush(folder string, push CardPush) ([]string, bool) {
	key := fmt.Sprintf("cards\x00%s\x00%s\x00%s\x00%s\x00%s", folder, push.Remote, push.Source, push.Target, push.Branch)
	found := remember(cache, key, func() listAnswer {
		paths, ok := cache.git.CardsPush(folder, push)
		return listAnswer{paths, ok}
	})
	return found.paths, found.ok
}

// The bounds of the questions to git (section 3): each at most a second, all of a call three.
const (
	questionBudget = time.Second
	callBudget     = 3 * time.Second
)

// Git answers the questions by running git, each with its timeout.
type Git struct {
	program string
	spent   time.Duration
	limit   time.Duration
	each    time.Duration
	cards   func(folder string, push CardPush, ask func(args ...string) (string, bool)) ([]string, bool)
}

// NewGit is the questions asked of the real git; cards answers the Card push question (nil: the
// push rule cannot be judged and refuses).
func NewGit(cards func(folder string, push CardPush, ask func(args ...string) (string, bool)) ([]string, bool)) *Git {
	return NewGitWithBudget(cards, questionBudget, callBudget)
}

// NewGitWithBudget is NewGit with each question given at most each and a call at most limit, so
// tests can give real git room on a loaded machine.
func NewGitWithBudget(cards func(folder string, push CardPush, ask func(args ...string) (string, bool)) ([]string, bool),
	each, limit time.Duration,
) *Git {
	return &Git{program: "git", limit: limit, each: each, cards: cards}
}

// run asks git one question in a folder; false when git fails, times out, or the call's budget is spent.
func (git *Git) run(folder string, env []string, args ...string) (string, bool) {
	text, code, ok := git.runCode(folder, env, args...)
	return text, ok && code == 0
}

func (git *Git) runCode(folder string, env []string, args ...string) (string, int, bool) {
	left := git.limit - git.spent
	if left <= 0 || folder == "" {
		return "", -1, false
	}
	budget := min(git.each, left)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	started := time.Now()
	out, err := gitcli.Git{Dir: folder, Env: env, Git: git.program}.Run(ctx, append([]string{"--no-optional-locks"}, args...)...)
	git.spent += time.Since(started)
	if err == nil {
		return string(out), 0, true
	}
	var exitErr *gitcli.ExitError
	if errors.As(err, &exitErr) {
		return "", exitErr.Code, true
	}
	return "", -1, false
}

// Held reports whether another ref holds the branch's commits.
func (git *Git) Held(folder, branch string) answer {
	ref := "refs/heads/" + branch
	if _, ok := git.run(folder, nil, "rev-parse", "--verify", "--quiet", ref); !ok {
		return answerUnsure
	}
	refs, ok := git.run(folder, nil, "for-each-ref", "--contains", ref, "--format=%(refname)")
	if !ok {
		return answerUnsure
	}
	for _, line := range strings.Split(refs, "\n") {
		if line != "" && line != ref {
			return answerYes
		}
	}
	return answerNo
}

// BranchExists reports whether a local branch exists: git's exit 1 is no, anything else unsure.
func (git *Git) BranchExists(folder, name string) answer {
	_, code, ok := git.runCode(folder, nil, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	switch {
	case !ok:
		return answerUnsure
	case code == 0:
		return answerYes
	case code == 1:
		return answerNo
	}
	return answerUnsure
}

// Conflicts reports whether a merge with conflicted paths is under way.
func (git *Git) Conflicts(folder string) bool {
	out, ok := git.run(folder, nil, "ls-files", "--unmerged")
	return ok && strings.TrimSpace(out) != ""
}

// Unrelated lists the edited paths under `paths` that are not in conflict.
func (git *Git) Unrelated(folder string, paths []string) ([]string, bool) {
	changed, ok := git.run(folder, nil, append([]string{"diff", "--name-only", "-z", "--"}, paths...)...)
	if !ok {
		return nil, false
	}
	unmerged, ok := git.run(folder, nil, "ls-files", "--unmerged", "--full-name", "-z")
	if !ok {
		return nil, false
	}
	conflicted := map[string]bool{}
	for _, entry := range strings.Split(unmerged, "\x00") {
		if _, name, found := strings.Cut(entry, "\t"); found {
			conflicted[name] = true
		}
	}
	found := []string{}
	for _, name := range strings.Split(changed, "\x00") {
		if name != "" && !conflicted[name] && !slices.Contains(found, name) {
			found = append(found, name)
		}
	}
	slices.Sort(found)
	return found, true
}

// Changed lists what writing over `paths` would lose. What git is told not to look at (a file
// marked skip-worktree or assume-unchanged) and what is inside a submodule cannot be said.
func (git *Git) Changed(folder string, paths []string, mode string) ([]string, bool) {
	if mode != modeWorktree {
		if len(paths) == 0 {
			return nil, false
		}
		listed, ok := git.run(folder, nil, append([]string{"ls-files", "-s", "-v", "-z", "--"}, paths...)...)
		if !ok {
			return nil, false
		}
		for _, entry := range strings.Split(listed, "\x00") {
			if entry == "" {
				continue
			}
			if entry[0] == 'S' || (entry[0] >= 'a' && entry[0] <= 'z') ||
				strings.HasPrefix(strings.TrimLeft(entry[1:], " "), "160000 ") {
				return nil, false
			}
		}
	}
	var out string
	var ok bool
	switch mode {
	case modeIndex:
		out, ok = git.run(folder, nil, append([]string{"diff", "--name-only", "--diff-filter=d", "-z", "--"}, paths...)...)
	case modeTree:
		out, ok = git.run(folder, nil, append([]string{"status", "--porcelain", "-z", "--untracked-files=all", "--ignored", "--"}, paths...)...)
	default:
		out, ok = git.run(folder, nil, "status", "--porcelain", "-z")
	}
	if !ok {
		return nil, false
	}
	found := []string{}
	for _, entry := range strings.Split(out, "\x00") {
		if entry != "" && len(found) < 20 {
			found = append(found, entry)
		}
	}
	return found, true
}

// defaultRef is the default branch: what origin/HEAD points to, else origin/main, origin/master,
// main, master; never `mine`.
func (git *Git) defaultRef(folder, mine string) string {
	var candidates []string
	if head, ok := git.run(folder, nil, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); ok &&
		strings.HasPrefix(strings.TrimSpace(head), "refs/remotes/") {
		candidates = append(candidates, strings.TrimSpace(head))
	}
	candidates = append(candidates, "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master")
	for _, ref := range candidates {
		if ref == mine {
			continue
		}
		if _, ok := git.run(folder, nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); ok {
			return ref
		}
	}
	return ""
}

// AddsNothing reports whether merging the branch into the default branch would change no file,
// with commits of its own whose files differ from the merge base's (work done and taken back is
// not proven). The merge is made in memory; its objects go to a folder that is thrown away.
func (git *Git) AddsNothing(folder, branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return false
	}
	mine := "refs/heads/" + branch
	if _, ok := git.run(folder, nil, "rev-parse", "--verify", "--quiet", mine); !ok {
		return false
	}
	base := git.defaultRef(folder, mine)
	if base == "" {
		return false
	}
	return git.AddsNothingAt(folder, base, mine)
}

// AddsNothingAt is the same test asked of a given default ref and a given tip (jst tidy asks it of
// the local default branch too, and at the tip it judged). Any doubt is false.
func (git *Git) AddsNothingAt(folder, base, mine string) bool {
	if base == "" || mine == "" || strings.HasPrefix(base, "-") || strings.HasPrefix(mine, "-") {
		return false
	}
	tree, ok := git.run(folder, nil, "rev-parse", "--verify", "--quiet", base+"^{tree}")
	if !ok {
		return false
	}
	objects, ok := git.run(folder, nil, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if !ok {
		return false
	}
	spare, err := os.MkdirTemp("", "jst-guard-merge-")
	if err != nil {
		return false
	}
	defer func() { _ = os.RemoveAll(spare) }()
	env := append(os.Environ(), "GIT_OBJECT_DIRECTORY="+spare, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+strings.TrimSpace(objects))
	merged, ok := git.run(folder, withoutRepoVariables(env), "merge-tree", "--write-tree", base, mine)
	if !ok {
		return false
	}
	first, _, _ := strings.Cut(merged, "\n")
	if strings.TrimSpace(first) == "" || strings.TrimSpace(first) != strings.TrimSpace(tree) {
		return false
	}
	ahead, ok := git.run(folder, nil, "rev-list", "--count", base+".."+mine)
	if !ok {
		return false
	}
	if count, err := strconv.Atoi(strings.TrimSpace(ahead)); err == nil && count == 0 {
		return true
	}
	mergeBase, ok := git.run(folder, nil, "merge-base", base, mine)
	if !ok {
		return false
	}
	baseTree, ok := git.run(folder, nil, "rev-parse", "--verify", "--quiet", strings.TrimSpace(mergeBase)+"^{tree}")
	if !ok {
		return false
	}
	tipTree, ok := git.run(folder, nil, "rev-parse", "--verify", "--quiet", mine+"^{tree}")
	return ok && strings.TrimSpace(baseTree) != strings.TrimSpace(tipTree)
}

// withoutRepoVariables keeps the merge's object folders and drops the variables that would point
// git at another repository.
func withoutRepoVariables(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, pair := range env {
		name, _, _ := strings.Cut(pair, "=")
		if strings.HasPrefix(name, "GIT_") && name != "GIT_OBJECT_DIRECTORY" && name != "GIT_ALTERNATE_OBJECT_DIRECTORIES" {
			continue
		}
		kept = append(kept, pair)
	}
	return kept
}

// Checkout gives the checkout's top folder and its repository's common folder.
func (git *Git) Checkout(folder string) (string, string, bool) {
	top, ok := git.run(folder, nil, "rev-parse", "--show-toplevel")
	if !ok {
		return "", "", false
	}
	common, ok := git.run(folder, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if !ok {
		return "", "", false
	}
	return realPath(strings.TrimSpace(top)), realPath(strings.TrimSpace(common)), true
}

// RefExists reports whether the folder is a repository with that ref (unsure: not a repository).
func (git *Git) RefExists(folder, ref string) answer {
	if _, ok := git.run(folder, nil, "rev-parse", "--git-dir"); !ok {
		return answerUnsure
	}
	_, ok := git.run(folder, nil, "rev-parse", "--verify", "--quiet", ref)
	return answerOf(ok)
}

// CardsPush hands the Card question to the range reader given at construction.
func (git *Git) CardsPush(folder string, push CardPush) ([]string, bool) {
	if git.cards == nil {
		return nil, false
	}
	return git.cards(folder, push, func(args ...string) (string, bool) { return git.run(folder, nil, args...) })
}
