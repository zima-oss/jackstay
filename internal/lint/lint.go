// Package lint is `jst lint pr` and `jst lint tree`: the `Card:` line and tree rules
// (docs/design/implementation.md, section 4.1) and the release rules D1–D4 (docs/design/release.md, section 7),
// designed from those specs (W-036). The `Card:` rules are internal/cards'; `jst lint range`, the
// guard's push rule, is the guard's.
package lint

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cards"
	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/check"
	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/release"
	"github.com/zima-oss/jackstay/internal/remote"
)

func init() {
	cli.Register(
		cli.Command{Name: "lint pr", Summary: "a pull request's Card lines, its changelog entry and the tree's rules", Usage: pullRequestHelp, Run: runPullRequest},
		cli.Command{Name: "lint tree", Summary: "the tree's rules (numbers, new cards, snapshots, records)", Usage: treeHelp, Run: runTree},
	)
}

// problem is a failure with its exit status: 2 an unusable request or jackstay.json, 5 an event file
// that is not what GitHub documents.
type problem struct {
	code    int
	message string
}

func (failure *problem) Error() string { return failure.message }

// Code is the exit status the failure means.
func (failure *problem) Code() int { return failure.code }

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "jst lint: %v\n", err)
	var failure *problem
	if errors.As(err, &failure) {
		return failure.code
	}
	return 2
}

func say(stdout io.Writer, found []Finding, what string) int {
	for _, finding := range found {
		fmt.Fprintf(stdout, "- [%s] %s\n", finding.Rule, finding.Text)
	}
	if len(found) == 0 {
		fmt.Fprintf(stdout, "jst lint: %s: fine\n", what)
		return 0
	}
	fmt.Fprintf(stdout, "jst lint: %s: %d rule(s) broken\n", what, len(found))
	return 1
}

// workspace is the project lint runs in.
type workspace struct {
	root   string
	config config.Config // every section but github, which only `--pr` and a queue copy read
	git    gitcli.Git
	stderr io.Writer // where a note on the token is said; nil says nothing
	// gitHubEnv and gitHubOptions are how GitHub is reached; a zero gitHubEnv is the process's.
	gitHubEnv     githubapi.Environment
	gitHubOptions []githubapi.Option
}

func loadWorkspace() (workspace, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return workspace{}, &problem{code: 2, message: err.Error()}
	}
	root, loaded, err := config.Load(workDir, config.Sections("github"))
	if err != nil {
		return workspace{}, &problem{code: 2, message: err.Error()}
	}
	if root == "" {
		return workspace{}, &problem{code: 2, message: "no " + config.Name + " at or above " + workDir}
	}
	return workspace{root: root, config: loaded, git: changes.LiteralGit(root)}, nil
}

// judged is the tree a command judges and the history behind it: the working tree with HEAD's
// history, or a commit's tree with that commit's history (a commit, so that its history exists).
func (space workspace) judged(revision string) (Files, History, error) {
	if revision == "" {
		return worktreeFiles{root: space.root, git: space.git}, gitHistory{revisions: []string{"HEAD"}, git: space.git}, nil
	}
	commit, err := run(space.git, "rev-parse", "--verify", "--quiet", revision+"^{commit}")
	if err != nil || strings.TrimSpace(string(commit)) == "" {
		return nil, nil, &problem{code: 2, message: "--rev " + revision + " is not a commit (its history is read too)"}
	}
	resolved := strings.TrimSpace(string(commit))
	return revisionFiles{revision: resolved, git: space.git}, gitHistory{revisions: []string{resolved}, git: space.git}, nil
}

// mergeOf is the in-memory merge of head into base (git merge-tree) as the judged tree, with the
// history of both sides. A merge with conflicts cannot be judged: exit 1, as GitHub would refuse it.
func (space workspace) mergeOf(base, head string) (Files, History, error) {
	if err := space.ensureCommits(base, head); err != nil {
		return nil, nil, err
	}
	out, err := run(space.git, "merge-tree", "--write-tree", base, head)
	if err != nil {
		return nil, nil, &problem{code: 1, message: "the pull request does not merge with its base without conflicts"}
	}
	tree, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return revisionFiles{revision: tree, git: space.git}, gitHistory{revisions: []string{base, head}, git: space.git}, nil
}

// ensureCommits fetches the commits a pull request names when they are not here yet.
func (space workspace) ensureCommits(commits ...string) error {
	for _, commit := range commits {
		if _, err := run(space.git, "cat-file", "-e", commit+"^{commit}"); err != nil {
			if _, fetchErr := run(space.git, "fetch", "--quiet", "--no-tags", "origin", commit); fetchErr != nil {
				return &problem{code: 2, message: "the commit " + commit + " is not here and could not be fetched"}
			}
		}
	}
	return nil
}

// configOf is the jackstay.json of the judged tree, which its tree rules follow; the checkout's when the
// tree has none.
func (space workspace) configOf(files Files) (config.Config, error) {
	data, exists, err := files.Read(config.Name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return space.config, nil
	}
	loaded, err := config.Parse(data)
	if err != nil {
		return nil, &problem{code: 2, message: "the judged tree's jackstay.json: " + err.Error()}
	}
	return loaded, nil
}

func runTree(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("jst lint tree", flag.ContinueOnError)
	flags.SetOutput(stderr)
	revision := flags.String("rev", "", "judge a commit's tree instead of the working tree")
	if flags.Parse(args) != nil || flags.NArg() > 0 {
		return 2
	}
	space, err := loadWorkspace()
	if err != nil {
		return fail(stderr, err)
	}
	files, history, err := space.judged(*revision)
	if err != nil {
		return fail(stderr, err)
	}
	judgedConfig, err := space.configOf(files)
	if err != nil {
		return fail(stderr, err)
	}
	if problems := check.ConfigProblems(judgedConfig); len(problems) > 0 {
		return fail(stderr, &problem{code: 2, message: "the judged tree's jackstay.json: " + strings.Join(problems, "; ")})
	}
	found, err := TreeRules(judgedConfig, files, history)
	if err != nil {
		return fail(stderr, &problem{code: 2, message: err.Error()})
	}
	found = append(found, entryRules(space, judgedConfig, *revision)...)
	name := "the working tree"
	if *revision != "" {
		name = *revision
	}
	return say(stdout, found, "the tree's rules on "+name)
}

// entryRules is D2 over every entry of the tree (the working tree, or a revision's).
func entryRules(space workspace, judgedConfig config.Config, revision string) []Finding {
	if judgedConfig.Section("changes") == nil {
		return nil
	}
	settings, err := changes.SettingsOf(judgedConfig)
	if err != nil {
		return []Finding{{Rule: RuleInvalidEntry, Text: err.Error()}}
	}
	repo := release.NewRepo(space.root)
	var tree changes.Tree = changes.Worktree{Root: space.root, Format: repo.Format}
	if revision != "" {
		tree = repo.Tree(revision)
	}
	listed, err := changes.ListEntries(tree, settings.Dir)
	if err != nil {
		return []Finding{{Rule: RuleInvalidEntry, Text: err.Error()}}
	}
	var found []Finding
	for _, listing := range listed.Entries {
		for _, entryProblem := range changes.EntryProblems(listing, settings) {
			found = append(found, Finding{Rule: RuleInvalidEntry, Text: listing.File.Path + ": " + entryProblem})
		}
	}
	if settings.Release {
		commit := "HEAD"
		if revision != "" {
			commit = revision
		}
		state, err := repo.StateAt(commit)
		if err != nil {
			return append(found, Finding{Rule: RuleInvalidEntry, Text: "the release history cannot be read: " + err.Error()})
		}
		for _, problem := range state.History.YankProblems(listed) {
			found = append(found, Finding{Rule: RuleInvalidEntry, Text: problem})
		}
	}
	return found
}

// event is the part of a pull_request event lint reads.
type event struct {
	PullRequest *struct {
		Body *string `json:"body"`
		Head *struct {
			Ref  *string `json:"ref"`
			SHA  string  `json:"sha"`
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base *struct {
			SHA  string `json:"sha"`
			Repo *struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	} `json:"pull_request"`
}

// inputs are a pull request's facts (docs/design/release.md, section 7's table).
type inputs struct {
	body       string
	headBranch string
	base       string
	head       string
	fork       bool
	mergeOf    bool // judge the in-memory merge of head and base, not the checkout
}

func fromEvent(filePath string) (inputs, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return inputs{}, &problem{code: 2, message: "cannot read the event file: " + err.Error()}
	}
	var read event
	if err := json.Unmarshal(data, &read); err != nil {
		return inputs{}, &problem{code: 5, message: filePath + " is not JSON: " + err.Error()}
	}
	pull := read.PullRequest
	if pull == nil || pull.Head == nil || pull.Head.Ref == nil {
		return inputs{}, &problem{code: 5, message: filePath + " is not a pull_request event as GitHub documents it (no pull_request.head.ref)"}
	}
	found := inputs{headBranch: *pull.Head.Ref, head: pull.Head.SHA}
	if pull.Body != nil {
		found.body = *pull.Body
	}
	if pull.Base != nil {
		found.base = pull.Base.SHA
		if pull.Head.Repo != nil && pull.Base.Repo != nil && pull.Head.Repo.FullName != pull.Base.Repo.FullName {
			found.fork = true
		}
	}
	return found, nil
}

func runPullRequest(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("jst lint pr", flag.ContinueOnError)
	flags.SetOutput(stderr)
	eventFile := flags.String("event", "", "a GitHub pull_request event file")
	number := flags.Int("pr", 0, "a pull request, read from GitHub")
	bodyFile := flags.String("body-file", "", "a description in a file")
	revision := flags.String("rev", "", "judge a commit's tree instead of the working tree")
	baseFlag := flags.String("base", "", "the base commit b")
	headFlag := flags.String("head", "", "the head commit h")
	branchFlag := flags.String("head-branch", "", "the head branch")
	if flags.Parse(args) != nil || flags.NArg() > 0 {
		return 2
	}
	sources := 0
	for _, given := range []bool{*eventFile != "", *number != 0, *bodyFile != ""} {
		if given {
			sources++
		}
	}
	if sources != 1 {
		fmt.Fprintln(stderr, "usage: jst lint pr (--event FILE | --pr N | --body-file F) [--rev REV] [--base B] [--head H] [--head-branch NAME]")
		return 2
	}
	space, err := loadWorkspace()
	if err != nil {
		return fail(stderr, err)
	}
	space.stderr = stderr
	var facts inputs
	switch {
	case *eventFile != "":
		facts, err = fromEvent(*eventFile)
	case *number != 0:
		facts, err = space.fromGitHub(*number)
	default:
		facts, err = space.fromBodyFile(*bodyFile)
	}
	if err != nil {
		return fail(stderr, err)
	}
	if *baseFlag != "" {
		facts.base = *baseFlag
	}
	if *headFlag != "" {
		facts.head = *headFlag
	}
	if *branchFlag != "" {
		facts.headBranch = *branchFlag
	}
	found, notes, err := space.judgePullRequest(facts, *revision)
	if err != nil {
		return fail(stderr, err)
	}
	for _, note := range notes {
		fmt.Fprintf(stdout, "note: %s\n", note)
	}
	return say(stdout, found, "the pull request")
}

var queueOf = regexp.MustCompile(`(?m)^Jackstay-Queue-Of: #([0-9]+)\s*$`)

// judgePullRequest applies every rule to a pull request's facts.
func (space workspace) judgePullRequest(facts inputs, revision string) ([]Finding, []string, error) {
	var found []Finding
	var notes []string
	if strings.HasPrefix(facts.headBranch, "jackstay-queue/") || queueOf.MatchString(facts.body) {
		original, finding, err := space.resolveQueueCopy(facts)
		if err != nil {
			return nil, nil, err
		}
		if finding != nil {
			found = append(found, *finding)
		} else {
			facts.headBranch = original
		}
	}
	// The rules a pull request is judged by come from the base's jackstay.json, never from the pull
	// request's or the checkout's (so it cannot exempt or loosen itself); the tree's rules follow
	// the judged tree's own jackstay.json (docs/design/release.md, section 7).
	if facts.mergeOf {
		if err := space.ensureCommits(facts.base, facts.head); err != nil {
			return nil, nil, err
		}
	}
	baseConfig := space.config
	// adopting: the base has no jackstay.json, so this pull request is the one that adopts jackstay;
	// with no base rules to protect, it is judged by its own (the judged tree's) jackstay.json.
	adopting := false
	if facts.base != "" {
		read, exists, err := release.NewRepo(space.root).ConfigIfAt(facts.base)
		if err != nil {
			return nil, nil, &problem{code: 2, message: "the base's jackstay.json: " + err.Error()}
		}
		baseConfig, adopting = read, !exists
	}
	files, history, err := space.judged(revision)
	if err != nil {
		return nil, nil, err
	}
	if facts.mergeOf && revision == "" {
		// A pull request read from GitHub: what would land is the merge of its head with the base,
		// made in memory, with both sides' history behind it.
		files, history, err = space.mergeOf(facts.base, facts.head)
		if err != nil {
			return nil, nil, err
		}
	}
	judgedConfig, err := space.configOf(files)
	if err != nil {
		return nil, nil, err
	}
	if adopting {
		if _, exists, err := files.Read(config.Name); err != nil || !exists {
			return nil, nil, &problem{code: 2, message: "the base's jackstay.json: " + facts.base + " has none, and neither has the pull request"}
		}
		baseConfig = judgedConfig
		notes = append(notes, "the base has no "+config.Name+": this pull request adopts jackstay, so it is judged by its own "+
			config.Name+" (the release rules wait for a base that has one; the new settings are checked)")
	}
	exempt := false
	if section := baseConfig.Section("cards"); section != nil {
		list, _ := section["exempt_branches"].([]any)
		for _, item := range list {
			if name, _ := item.(string); name == facts.headBranch {
				exempt = true
			}
		}
	}
	if section := baseConfig.Section("cards"); section != nil && !exempt {
		lines, err := CardLines(section, files, facts.body)
		if err != nil {
			return nil, nil, err
		}
		found = append(found, lines...)
	}
	tree, err := TreeRules(judgedConfig, files, history)
	if err != nil {
		return nil, nil, &problem{code: 2, message: err.Error()}
	}
	found = append(found, tree...)
	if facts.fork {
		return append(found, Finding{Rule: RuleUndocumented, Text: "a head in another repository: the release rules cannot judge it"}), notes, nil
	}
	if facts.base == "" || facts.head == "" {
		if baseConfig.Section("changes") == nil {
			return found, notes, nil // no release rules apply
		}
		return nil, nil, &problem{code: 2, message: "the base and head commits are not known (an event without pull_request.base.sha and head.sha, or a base branch not found): the release rules cannot be applied"}
	}
	repo := release.NewRepo(space.root)
	if adopting {
		problems, err := settingsProblemsAt(repo, facts.head, nil)
		if err != nil {
			return nil, nil, &problem{code: 2, message: err.Error()}
		}
		for _, problem := range problems {
			found = append(found, Finding{Rule: RuleSettingsAlone, Text: "the new settings: " + problem})
		}
		return found, notes, nil
	}
	mergeBase, err := repo.MergeBase(facts.base, facts.head)
	if err != nil {
		return nil, nil, &problem{code: 2, message: "the merge base of " + facts.base + " and " + facts.head + ": " + err.Error()}
	}
	releaseFound, releaseNotes, err := ReleaseRules(repo, baseConfig, Change{Base: facts.base, Head: facts.head, MergeBase: mergeBase,
		HeadBranch: facts.headBranch, Body: facts.body, Judged: files})
	if err != nil {
		return nil, nil, &problem{code: 2, message: err.Error()}
	}
	return append(found, releaseFound...), append(notes, releaseNotes...), nil
}

// CardLines is the description rules on body against the card files of files, through
// internal/cards; section is the project's `cards` section. A section that cannot be read, or a
// tree with no card files, is an error whose Code() is 2.
func CardLines(section map[string]any, files Files, body string) ([]Finding, error) {
	rules, err := cards.FromSection(section)
	if err != nil {
		return nil, &problem{code: 2, message: err.Error()}
	}
	paths, err := files.Paths()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, filePath := range paths {
		if path.Dir(filePath) == rules.Dir {
			names = append(names, filePath)
		}
	}
	if len(names) == 0 {
		return nil, &problem{code: 2, message: "there are no card files in " + rules.Dir + " (cards.dir): nothing is guessed"}
	}
	if strings.TrimSpace(body) == "" {
		return []Finding{{Rule: cards.NoLine, Text: "the pull request has no description, so no `Card:` line"}}, nil
	}
	var found []Finding
	for _, finding := range rules.Check(body, rules.Known(names)) {
		var text string
		switch finding.Rule {
		case cards.NoLine:
			text = "no `Card: ID` line (or `Card: none (<reason>)`)"
		case cards.Unknown:
			text = fmt.Sprintf("line %d: %s has no card file in %s", finding.Line, finding.ID, rules.Dir)
		default:
			text = fmt.Sprintf("line %d: %q", finding.Line, finding.Text)
		}
		found = append(found, Finding{Rule: finding.Rule, Text: text})
	}
	return found, nil
}

// resolveQueueCopy reads the original a queue copy names (docs/design/release.md,
// section 7). A finding when the copy is malformed.
func (space workspace) resolveQueueCopy(facts inputs) (string, *Finding, error) {
	parts := queueOf.FindStringSubmatch(facts.body)
	onQueue := strings.HasPrefix(facts.headBranch, "jackstay-queue/")
	switch {
	case onQueue && parts == nil:
		return "", &Finding{Rule: RuleRequestOnly, Text: "a jackstay-queue/ branch without `Jackstay-Queue-Of: #n` in its description"}, nil
	case !onQueue:
		return "", &Finding{Rule: RuleRequestOnly, Text: "`Jackstay-Queue-Of` on a branch outside jackstay-queue/"}, nil
	}
	number, _ := strconv.Atoi(parts[1])
	original, err := space.fromGitHub(number)
	if err != nil {
		return "", nil, err
	}
	if original.fork {
		return "", &Finding{Rule: RuleRequestOnly, Text: fmt.Sprintf("the queue copy names #%d, whose head is in another repository", number)}, nil
	}
	return original.headBranch, nil, nil
}

// fromGitHub reads a pull request through GitHub's API with jackstay's one client (docs/design/implementation.md,
// section 8.1): inside GitHub Actions with GH_TOKEN; elsewhere as the project's account
// (github.account, or JACKSTAY_GITHUB_ACCOUNT), whose token gh keeps. The repository is
// GITHUB_REPOSITORY inside GitHub Actions; elsewhere github.repository, else the URL of the remote
// internal/remote picks (a GITHUB_REPOSITORY left in a local shell names no project's repository).
func (space workspace) fromGitHub(number int) (inputs, error) {
	env := space.gitHubEnv
	if env.Getenv == nil {
		env = githubapi.OSEnvironment()
	}
	repository, account := "", ""
	var githubSection map[string]any // nil inside GitHub Actions, where jackstay.json is not read
	if githubapi.InActions(env) {
		repository = env.Getenv("GITHUB_REPOSITORY")
	} else {
		_, loaded, err := config.Load(space.root, []string{"github"})
		if err != nil {
			return inputs{}, &problem{code: 2, message: err.Error()}
		}
		section, _ := loaded.GitHub()
		account = section.Account
		repository = section.Repository
		githubSection = loaded.Section("github")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if repository == "" {
		// The remote's URL, by the project's rule; none read leaves it to Connect to refuse.
		if found, err := remote.RepositoryOf(ctx, space.git, githubSection); err == nil {
			repository = found.String()
		}
	}
	client, token, err := githubapi.Connect(ctx, env, account, repository, space.gitHubOptions...)
	if err != nil {
		return inputs{}, &problem{code: 2, message: err.Error()}
	}
	if token.Note != "" && space.stderr != nil {
		fmt.Fprintf(space.stderr, "jst lint: %s\n", token.Note)
	}
	pull, err := client.PullRequest(ctx, number)
	if err != nil {
		return inputs{}, &problem{code: 2, message: fmt.Sprintf("pull request #%d: %v", number, err)}
	}
	return inputs{body: pull.Body, headBranch: pull.Head, head: pull.HeadSHA, base: pull.BaseSHA, mergeOf: true,
		fork: pull.HeadRepository != "" && pull.HeadRepository != pull.BaseRepository}, nil
}

// fromBodyFile is a description in a file, on the current branch against the base branch.
func (space workspace) fromBodyFile(filePath string) (inputs, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return inputs{}, &problem{code: 2, message: "cannot read " + filePath + ": " + err.Error()}
	}
	branch, _ := run(space.git, "rev-parse", "--abbrev-ref", "HEAD")
	facts := inputs{body: string(data), headBranch: strings.TrimSpace(string(branch)), head: "HEAD"}
	base := "main"
	if section := space.config.Section("cards"); section != nil {
		if name, _ := section["branch"].(string); name != "" {
			base = name
		}
	}
	if section := space.config.Section("release"); section != nil {
		if name, _ := section["base"].(string); name != "" {
			base = name
		}
	}
	if resolved, err := run(space.git, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err == nil {
		facts.base = strings.TrimSpace(string(resolved))
	}
	if resolved, err := run(space.git, "rev-parse", "--verify", "HEAD^{commit}"); err == nil {
		facts.head = strings.TrimSpace(string(resolved))
	}
	return facts, nil
}
