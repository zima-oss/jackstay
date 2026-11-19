package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/remote"
)

// attempts is how often the workflow's commands start again when GitHub or the base moved under
// them (docs/design/release.md, sections 6 and 8).
const attempts = 3

// Flow is the workflow's view: the repository, GitHub, and who it acts as.
type Flow struct {
	Repo       Repo
	GitHub     GitHub
	Remote     string   // the selected Git remote
	Repository string   // owner/name, for links
	Author     string   // the writing App's login, `<slug>[bot]`
	Identity   Identity // who its commits and tags are by
	Out        io.Writer
	DryRun     bool
	Context    context.Context
	// protectedBase is the base of the run (set by the commands): its branch is never deleted.
	protectedBase string
}

// outcome gathers what a run reports and the worst exit status.
type outcome struct {
	out  io.Writer
	code int
}

func (result *outcome) say(format string, args ...any) { fmt.Fprintf(result.out, format+"\n", args...) }

func (result *outcome) problem(format string, args ...any) {
	fmt.Fprintf(result.out, "PROBLEM: "+format+"\n", args...)
	if result.code == 0 {
		result.code = 1
	}
}

// context is the run's context: the command's deadline, else none.
func (flow Flow) context() context.Context {
	if flow.Context != nil {
		return flow.Context
	}
	return context.Background()
}

// Publish is `jst release publish`: finish what was released, release what a landed request asks,
// then mark yanked releases (docs/design/release.md, section 8).
func (flow Flow) Publish(base string) (int, error) {
	cleanup, err := flow.inPreview()
	if err != nil {
		return 2, err
	}
	defer cleanup()
	result := &outcome{out: flow.Out}
	for attempt := 1; attempt <= attempts; attempt++ {
		tip, err := flow.Repo.fetchBase(flow.Remote, base)
		if err != nil {
			return 2, err
		}
		state, err := flow.stateAtBase(base, tip)
		if err != nil {
			return 2, err
		}
		if err := flow.Repo.CheckVersionFiles(state); err != nil {
			return 2, err
		}
		releases, err := flow.GitHub.Releases(flow.context())
		if err != nil {
			return 2, err
		}
		tags, err := flow.Repo.RemoteRefs(flow.Remote, "refs/tags/*")
		if err != nil {
			return 2, err
		}
		flow.finish(state, releases, tags, result)
		moved, askedErr := flow.releaseAsked(state, releases, result)
		if askedErr == nil && moved {
			result.say("the base moved while the release commit was pushed; reading it again")
			continue
		}
		if askedErr != nil {
			result.problem("releasing what the request asks: %v", askedErr)
			result.code = 2
		}
		// A release just pushed is part of what the yanks are reconciled against.
		tip, err = flow.Repo.fetchBase(flow.Remote, base)
		if err != nil {
			return 2, fmt.Errorf("reading the base after the release: %w", err)
		}
		if tip != state.Commit {
			fresh, err := flow.stateAtBase(base, tip)
			if err != nil {
				return 2, fmt.Errorf("reading the base after the release: %w", err)
			}
			state = fresh
		}
		if err := flow.reconcile(state, result); err != nil {
			return 2, err
		}
		return result.code, nil
	}
	result.problem("the base kept moving; nothing more was released in this run")
	// Reconciled against the base as it is now, read once more; without that reading, nothing is
	// changed on GitHub (a yank or a release that landed meanwhile must not be missed).
	if tip, err := flow.Repo.fetchBase(flow.Remote, base); err == nil {
		if fresh, err := flow.stateAtBase(base, tip); err == nil {
			if err := flow.reconcile(fresh, result); err != nil {
				return 2, err
			}
			return result.code, nil
		}
	}
	result.problem("the base could not be read again: yanks were not reconciled")
	return result.code, nil
}

// stateAtBase ensures a fetched branch and its own release settings identify the same base.
func (flow Flow) stateAtBase(base, tip string) (State, error) {
	state, err := flow.Repo.StateAt(tip)
	if err != nil {
		return State{}, err
	}
	if state.Settings.Base != base {
		return State{}, fmt.Errorf("the fetched branch is %s, but its release base is %s; run with a checkout configured for that base", base, state.Settings.Base)
	}
	return state, nil
}

// finish tags and publishes every proven release that lacks its tag or its GitHub release, and
// reports trailers that prove nothing and tags on no release.
func (flow Flow) finish(state State, releases []Release, tags map[string]string, result *outcome) {
	byTag := map[string]Release{}
	for _, release := range releases {
		byTag[release.Tag] = release
	}
	onRelease := map[string]bool{}
	for index, release := range state.History.Releases {
		tag := release.Tag()
		onRelease[tag] = true
		previous := ""
		if index > 0 {
			previous = state.History.Releases[index-1].Tag()
		}
		flow.publishOne(state, release, previous, tags, byTag, result)
	}
	for _, unproven := range state.History.Unproven {
		result.problem("%s has the release trailer but is no release commit: %s (left alone)", short(unproven.Commit), unproven.Reason)
	}
	names := make([]string, 0, len(tags))
	for ref := range tags {
		names = append(names, ref)
	}
	sort.Strings(names)
	for _, ref := range names {
		tag := strings.TrimPrefix(ref, "refs/tags/")
		version, ok := state.Settings.VersionOfTag(tag)
		if ok && state.Settings.First.Less(version) && !onRelease[tag] {
			result.problem("the tag %s is on %s, which is no release commit (left alone)", tag, short(tags[ref]))
		}
	}
}

// publishOne makes sure a release has its tag on its commit and a published GitHub release.
func (flow Flow) publishOne(state State, release Landed, previousTag string, tags map[string]string, byTag map[string]Release, result *outcome) {
	tag := release.Tag()
	ref := "refs/tags/" + tag
	switch taggedCommit, exists := tags[ref]; {
	case exists && taggedCommit != release.Commit:
		result.problem("the tag %s is on %s, not on the release commit %s: a tag is never moved; the owner decides", tag, short(taggedCommit), short(release.Commit))
		return
	case !exists:
		if flow.DryRun {
			result.say("would tag %s on %s", tag, short(release.Commit))
			return
		}
		if err := flow.Repo.tagAndPush(flow.Remote, tag, release.Commit, "Release "+release.Version.String(), flow.Identity); err != nil {
			result.problem("tagging %s: %v", tag, err)
			return
		}
		tags[ref] = release.Commit
		result.say("tagged %s on %s", tag, short(release.Commit))
	}
	notes, err := flow.notes(state, release, previousTag, tag)
	if err != nil {
		result.problem("the notes of %s: %v", tag, err)
		return
	}
	existing, has := byTag[tag]
	// A GitHub release is made once, published, on its proven tag (W-047). One that exists, as a
	// draft or not, is left as it is: a draft is a person's to publish or delete.
	if has {
		if existing.Draft {
			result.problem("a draft GitHub release %s exists: publish or delete it by hand", tag)
		}
		return
	}
	if flow.DryRun {
		result.say("would publish the GitHub release %s", tag)
		return
	}
	before, err := flow.Repo.RemoteRefs(flow.Remote, ref, ref+"^{}")
	if err != nil || before[ref] != release.Commit {
		result.problem("the tag %s does not point at %s on the remote: no release is made; a person looks", tag, short(release.Commit))
		return
	}
	if _, err := flow.GitHub.CreateRelease(flow.context(), tag, release.Commit, release.Version.String(), notes, false); err != nil {
		result.problem("creating the release %s: %v", tag, err)
		return
	}
	result.say("published the GitHub release %s", tag)
}

// notes are a GitHub release's notes: the consumer entries its commit removed, the override's
// reason, a compare link from the previous release's tag.
func (flow Flow) notes(state State, release Landed, previousTag, tag string) (string, error) {
	entries, err := flow.Repo.ReleasedEntries(release)
	if err != nil {
		return "", err
	}
	text := changes.Body(changes.Of(entries, changes.Consumer))
	parentTree := flow.Repo.Tree(release.Parent)
	if data, exists, err := parentTree.Read(release.Settings.OverridePath()); err == nil && exists {
		if override, err := DecodeOverride(data); err == nil {
			text += "\n\nVersion given by hand: " + override.Reason
		}
	}
	if previousTag != "" && flow.Repository != "" {
		text += fmt.Sprintf("\n\nhttps://github.com/%s/compare/%s...%s", flow.Repository, previousTag, tag)
	}
	return strings.TrimSpace(text) + "\n", nil
}

// releaseAsked makes the release commit when the base holds a releasable request. moved is true
// when the push was refused because the base moved.
func (flow Flow) releaseAsked(state State, releases []Release, result *outcome) (moved bool, err error) {
	request, ok, reason, err := flow.Repo.Releasable(state)
	if err != nil {
		return false, err
	}
	if request == nil && reason != "" {
		result.problem("the request file on the base is not a request: %s", reason)
		return false, nil
	}
	if request == nil {
		return false, nil
	}
	if !ok {
		result.say("the request on the base releases nothing: %s", reason)
		return false, nil
	}
	prepared, err := flow.Repo.Prepare(state)
	if err != nil {
		result.problem("preparing %s: %v", request.Version, err)
		return false, nil
	}
	commit, err := flow.Repo.commitOn(state.Commit, prepared.Writes, prepared.Removes, prepared.Message(), flow.Identity)
	if err != nil {
		return false, err
	}
	last := state.History.Last()
	proven, why, err := flow.Repo.Prove(commit, &last)
	if err != nil {
		return false, err
	}
	if why != "" {
		return false, fmt.Errorf("the release commit jackstay made does not prove itself (%s); nothing pushed", why)
	}
	if flow.DryRun {
		result.say("would push the release commit %s (%s) onto %s", short(commit), proven.Version, short(state.Commit))
		return false, nil
	}
	refused, err := flow.Repo.pushLeased(flow.Remote, state.Settings.Base, commit, state.Commit)
	if err != nil {
		return false, err
	}
	if refused {
		return true, nil
	}
	result.say("pushed the release commit %s: %s", short(commit), proven.Version)
	tags, err := flow.Repo.RemoteRefs(flow.Remote, "refs/tags/*")
	if err != nil {
		return false, err
	}
	byTag := map[string]Release{}
	for _, release := range releases {
		byTag[release.Tag] = release
	}
	previous := ""
	if count := len(state.History.Releases); count > 0 {
		previous = state.History.Releases[count-1].Tag()
	}
	flow.publishOne(state, proven, previous, tags, byTag, result)
	return false, nil
}

// reconcile marks yanked releases.
func (flow Flow) reconcile(state State, result *outcome) error {
	yanked := map[string]string{}
	listed, err := changes.ListEntries(flow.Repo.Tree(state.Commit), state.Settings.Changes.Dir)
	if err != nil {
		return err
	}
	for _, listing := range listed.Entries {
		if listing.Entry.Yanks != "" {
			yanked[listing.Entry.Yanks] = listing.Entry.Body
		}
	}
	for _, release := range state.History.Releases {
		entries, err := flow.Repo.ReleasedEntries(release)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Yanks != "" {
				yanked[entry.Yanks] = entry.Body
			}
		}
	}
	releases, err := flow.GitHub.Releases(flow.context())
	if err != nil {
		return err
	}
	// Each release's tag in its own format (a later change of release.tag renames nothing).
	// Only a release whose tag is on its proven commit counts; a tag on another commit is left out
	// of every edit (finish reports it).
	remoteTags, err := flow.Repo.RemoteRefs(flow.Remote, "refs/tags/*")
	if err != nil {
		return err
	}
	versionOfTag := map[string]Version{}
	for _, landed := range state.History.Releases {
		if remoteTags["refs/tags/"+landed.Tag()] == landed.Commit {
			versionOfTag[landed.Tag()] = landed.Version
		}
	}
	// A yank changes the release's title and puts the reason on top; "latest" is GitHub's own rule.
	for _, release := range releases {
		version, ok := versionOfTag[release.Tag]
		if !ok || release.Draft {
			continue
		}
		reason, isYanked := yanked[version.String()]
		if !isYanked || strings.HasSuffix(release.Name, "(yanked)") || flow.DryRun {
			continue
		}
		name := version.String() + " (yanked)"
		body := "**Yanked:** " + strings.TrimSpace(reason) + "\n\n" + release.Body
		if _, err := flow.GitHub.EditRelease(flow.context(), release.ID, Edit{Name: &name, Body: &body}); err != nil {
			result.problem("marking %s yanked: %v", release.Tag, err)
		} else {
			result.say("marked %s yanked", release.Tag)
		}
	}
	return nil
}

// PullRequestFlow is `jst release pr` (docs/design/release.md, section 6).
func (flow Flow) PullRequestFlow(base string) (int, error) {
	cleanup, err := flow.inPreview()
	if err != nil {
		return 2, err
	}
	defer cleanup()
	flow.protectedBase = base
	result := &outcome{out: flow.Out}
	for attempt := 1; attempt <= attempts; attempt++ {
		settled, err := flow.pullRequestOnce(base, result)
		if err != nil {
			return 2, err
		}
		if settled {
			return result.code, nil
		}
		result.say("the release pull request changed while it was updated; reading it again")
	}
	result.problem("the release pull request kept changing; stopped after %d tries", attempts)
	return result.code, nil
}

func (flow Flow) pullRequestOnce(base string, result *outcome) (bool, error) {
	tip, err := flow.Repo.fetchBase(flow.Remote, base)
	if err != nil {
		return false, err
	}
	state, err := flow.stateAtBase(base, tip)
	if err != nil {
		return false, err
	}
	if err := flow.Repo.CheckVersionFiles(state); err != nil {
		return false, err
	}
	settings := state.Settings
	open, err := flow.GitHub.OpenPullRequests(flow.context())
	if err != nil {
		return false, err
	}
	var current *PullRequest
	for index := range open {
		pull := open[index]
		if !flow.ownRepository(pull) {
			continue // a fork's pull request is never the release pull request, nor closed by it
		}
		switch {
		case pull.Head == settings.Branch && pull.Base == settings.Base:
			current = &open[index]
		case pull.Head == settings.Branch || (flow.Author != "" && pull.Author == flow.Author):
			flow.closeAndDelete(pull, "The release pull request moved (`release.base` or `release.branch` changed): this one is closed by the release workflow.", result)
		}
	}
	tree := flow.Repo.Tree(tip)
	_, alreadyOk, _, err := flow.Repo.Releasable(state)
	if err != nil {
		return false, err
	}
	next, _, _, listed, nextErr := Next(tree, settings, state.History.Last())
	if alreadyOk || errors.Is(nextErr, ErrNothing) {
		if current != nil {
			why := fmt.Sprintf("Nothing to release since %s: the release workflow closed this pull request.", state.History.Last())
			if alreadyOk {
				why = "The request on the base is releasable already; the release workflow makes the release."
			}
			flow.closeAndDelete(*current, why, result)
		}
		return true, nil
	}
	if nextErr != nil {
		result.problem("the base's entries cannot be released: %v", nextErr)
		return true, nil
	}
	request := RequestOf(listed, next, tip)
	prepared, err := flow.Repo.Prepare(state)
	if err != nil {
		result.problem("preparing %s on the base: %v", next, err)
		return true, nil
	}
	message := fmt.Sprintf("Request release %s\n\nCard: none (release request %s)\n", next, next)
	commit, err := flow.Repo.commitOn(tip, map[string][]byte{settings.RequestPath(): request.Encode()}, nil, message, flow.Identity)
	if err != nil {
		return false, err
	}
	title := "Release " + next.String()
	body := description(prepared, settings, request)
	if flow.DryRun {
		result.say("would push %s to %s and set the pull request %q", short(commit), settings.Branch, title)
		return true, nil
	}
	owner := settings.Merge == "owner"
	if current != nil && owner && !current.Draft {
		if err := flow.GitHub.ConvertToDraft(flow.context(), current.NodeID); err != nil {
			return false, err
		}
	}
	remote, err := flow.Repo.RemoteRefs(flow.Remote, "refs/heads/"+settings.Branch)
	if err != nil {
		return false, err
	}
	refused, err := flow.Repo.pushLeased(flow.Remote, settings.Branch, commit, remote["refs/heads/"+settings.Branch])
	if err != nil {
		return false, err
	}
	if refused {
		return false, nil
	}
	number := 0
	if current == nil {
		created, err := flow.GitHub.CreatePullRequest(flow.context(), settings.Branch, settings.Base, title, body, owner)
		if err != nil {
			return false, err
		}
		number = created.Number
		result.say("opened the release pull request #%d: %s", number, title)
	} else {
		number = current.Number
		if err := flow.GitHub.UpdatePullRequest(flow.context(), number, title, body); err != nil {
			return false, err
		}
		result.say("updated the release pull request #%d: %s", number, title)
	}
	back, err := flow.GitHub.PullRequest(flow.context(), number)
	if err != nil {
		return false, err
	}
	if back.HeadSHA != commit || back.Base != settings.Base || back.State != "open" || (owner && !back.Draft) {
		return false, nil
	}
	return true, nil
}

// ownRepository says whether a pull request's head and base are this repository.
func (flow Flow) ownRepository(pull PullRequest) bool {
	if pull.HeadRepository == "" && pull.BaseRepository == "" {
		return true // a fake or an answer without repositories: nothing to tell apart
	}
	if pull.HeadRepository != pull.BaseRepository {
		return false
	}
	return flow.Repository == "" || pull.BaseRepository == flow.Repository
}

// closeAndDelete closes a pull request with a comment and deletes its branch under a lease on
// the head it read.
func (flow Flow) closeAndDelete(pull PullRequest, comment string, result *outcome) {
	if flow.DryRun {
		result.say("would close #%d (%s)", pull.Number, pull.Head)
		return
	}
	if err := flow.GitHub.ClosePullRequest(flow.context(), pull.Number, comment); err != nil {
		result.problem("closing #%d: %v", pull.Number, err)
		return
	}
	if pull.Head == flow.protectedBase {
		result.say("closed #%d; its branch %s is a base and is kept", pull.Number, pull.Head)
		return
	}
	if err := flow.Repo.deleteLeased(flow.Remote, pull.Head, pull.HeadSHA); err != nil {
		result.say("closed #%d; its branch %s was kept (%v)", pull.Number, pull.Head, err)
		return
	}
	result.say("closed #%d and deleted %s", pull.Number, pull.Head)
}

// description is the release pull request's description: the release as it will be written.
func description(prepared Prepared, settings Settings, request Request) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Merging this pull request releases **%s** (last release %s", prepared.Version, prepared.Last)
	if prepared.Bump != "" {
		fmt.Fprintf(&out, ", bump %s", prepared.Bump)
	}
	out.WriteString("). It changes one file, the request; after it lands, the release workflow writes the version, the changelog and the tag. The heading's date is a preview: the release takes the date of the commit it is made on. This branch is rebuilt by the workflow; change a release through its entries.\n\n")
	out.WriteString(prepared.Consumer)
	if prepared.Internal != "" {
		out.WriteString("\nInternal:\n\n")
		out.WriteString(changes.Body(changes.Of(prepared.Entries, changes.Internal)))
		out.WriteString("\n")
	}
	if prepared.Override != nil {
		fmt.Fprintf(&out, "\nOverride: %s\n", prepared.Override.Reason)
	}
	out.WriteString("\nEntries:\n")
	names := make([]string, 0, len(request.Entries))
	for name := range request.Entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&out, "- `%s`\n", path.Join(settings.Changes.Dir, name))
	}
	fmt.Fprintf(&out, "\nCard: none (release request %s)\n", prepared.Version)
	return out.String()
}

// flowFromEnvironment builds the workflow's Flow from the environment GitHub Actions gives. The
// token follows docs/design/implementation.md, section 8.1: inside Actions GH_TOKEN only; outside, every
// run is a dry run, read from GitHub with the project's account (github.account, or
// JACKSTAY_GITHUB_ACCOUNT) when it names one and the repository is known, else offline.
func flowFromEnvironment(ctx context.Context, stdout, stderr io.Writer, dryRun bool) (Flow, Settings, error) {
	loaded, settings, repo, err := project()
	if err != nil {
		return Flow{}, Settings{}, err
	}
	env := githubapi.OSEnvironment()
	inActions := githubapi.InActions(env)
	_, githubConfig, err := config.Load(loaded.Root, []string{"github"})
	if err != nil {
		return Flow{}, Settings{}, &Problem{Code: 2, Message: err.Error()}
	}
	remoteName, err := remote.Name(ctx, repo.Git, remote.ConfiguredRemote(githubConfig.Section("github")))
	if err != nil {
		return Flow{}, Settings{}, &Problem{Code: 2, Message: err.Error()}
	}
	flow := Flow{Repo: repo, Remote: remoteName, Author: os.Getenv("JACKSTAY_RELEASE_AUTHOR"), Out: stdout, DryRun: dryRun || !inActions}
	if flow.Author != "" {
		flow.Identity = Identity{Name: flow.Author, Email: os.Getenv("JACKSTAY_RELEASE_EMAIL")}
		if flow.Identity.Email == "" {
			flow.Identity.Email = flow.Author + "@users.noreply.github.com"
		}
	}
	if !inActions {
		return localFlow(ctx, flow, settings, loaded.Root, env, stderr)
	}
	// GITHUB_REPOSITORY is read only here: in a local shell it may be left from anything.
	flow.Repository = env.Getenv("GITHUB_REPOSITORY")
	token, err := githubapi.ResolveToken(ctx, env, "")
	if err != nil && !flow.DryRun {
		return Flow{}, Settings{}, &Problem{Code: 2, Message: "GH_TOKEN is not set (the writing App's token, in the workflow)"}
	}
	if flow.Repository == "" {
		if flow.DryRun {
			flow.GitHub = offlineGitHub{}
			return flow, settings, nil
		}
		return Flow{}, Settings{}, &Problem{Code: 2, Message: "GITHUB_REPOSITORY is not set"}
	}
	if err != nil {
		flow.GitHub = offlineGitHub{}
		return flow, settings, nil
	}
	client, err := NewGitHub(token.Value, flow.Repository)
	if err != nil {
		return Flow{}, Settings{}, err
	}
	flow.GitHub = client
	if !flow.DryRun {
		if err := flow.verifyRemoteRepository(); err != nil {
			return Flow{}, Settings{}, err
		}
	}
	return flow, settings, nil
}

// verifyRemoteRepository binds the configured Git remote to the authenticated workflow repo
// before writes. Check the effective transport URLs, including explicit push URLs and rewrites.
func (flow Flow) verifyRemoteRepository() error {
	for _, direction := range []string{"fetch", "push"} {
		args := []string{"remote", "get-url", "--all"}
		if direction == "push" {
			args = append(args, "--push")
		}
		addresses, err := flow.Repo.run(append(args, "--", flow.Remote)...)
		if err != nil {
			return fmt.Errorf("cannot verify the %s destinations of release remote %q", direction, flow.Remote)
		}
		for _, address := range strings.Split(strings.TrimSuffix(string(addresses), "\n"), "\n") {
			repository, err := remote.ParseURL(address)
			if err != nil || !strings.EqualFold(repository.String(), flow.Repository) {
				return fmt.Errorf("the %s destination of release remote %q does not identify %s", direction, flow.Remote, flow.Repository)
			}
		}
	}
	return nil
}

// localFlow is a dry run outside GitHub Actions: GH_TOKEN and GITHUB_REPOSITORY are ignored (they
// could be any account's and any repository's); GitHub is read as the project's account when one is
// named and the repository is known (github.repository, else the URL of the remote internal/remote
// picks), else the run is offline.
func localFlow(ctx context.Context, flow Flow, settings Settings, root string, env githubapi.Environment, stderr io.Writer,
	options ...githubapi.Option,
) (Flow, Settings, error) {
	_, loaded, err := config.Load(root, []string{"github"})
	if err != nil {
		return Flow{}, Settings{}, &Problem{Code: 2, Message: err.Error()}
	}
	section, _ := loaded.GitHub()
	flow.Repository = ""
	configuredRemote := flow.Remote
	if configuredRemote == "" {
		configuredRemote = section.Remote
	}
	source := map[string]any{"remote": configuredRemote, "repository": section.Repository}
	if found, err := remote.RepositoryOf(ctx, gitcli.Git{Dir: root}, source); err == nil {
		flow.Repository = found.String()
	}
	account := githubapi.EffectiveAccount(env, section.Account)
	if account == "" || flow.Repository == "" {
		if env.Getenv("GH_TOKEN") != "" {
			fmt.Fprintln(stderr, "jst release: GH_TOKEN is ignored outside GitHub Actions; with no github.account (or no repository) the dry run is offline")
		}
		flow.GitHub = offlineGitHub{}
		return flow, settings, nil
	}
	client, token, err := githubapi.Connect(ctx, env, section.Account, flow.Repository, options...)
	if err != nil {
		var coded interface{ Code() int }
		if errors.As(err, &coded) {
			return Flow{}, Settings{}, &Problem{Code: coded.Code(), Message: err.Error()}
		}
		return Flow{}, Settings{}, &Problem{Code: 2, Message: err.Error()}
	}
	if token.Note != "" {
		fmt.Fprintln(stderr, "jst release: "+token.Note)
	}
	flow.GitHub = gitHubOf(client)
	return flow, settings, nil
}

func runPublish(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flagSet("publish", stderr)
	dryRun := flags.Bool("dry-run", false, "say what would be done; write nothing")
	if flags.Parse(args) != nil || flags.NArg() > 0 {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	flow, settings, err := flowFromEnvironment(ctx, stdout, stderr, *dryRun)
	if err != nil {
		return fail(stderr, "publish", err)
	}
	if flow.DryRun && !*dryRun {
		fmt.Fprintln(stdout, "jst release publish: outside GitHub Actions, a dry run")
	}
	flow.Context = ctx
	code, err := flow.Publish(settings.Base)
	if err != nil {
		return fail(stderr, "publish", err)
	}
	return code
}

func runPullRequest(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flagSet("pr", stderr)
	dryRun := flags.Bool("dry-run", false, "say what would be done; write nothing")
	if flags.Parse(args) != nil || flags.NArg() > 0 {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	flow, settings, err := flowFromEnvironment(ctx, stdout, stderr, *dryRun)
	if err != nil {
		return fail(stderr, "pr", err)
	}
	if flow.DryRun && !*dryRun {
		fmt.Fprintln(stdout, "jst release pr: outside GitHub Actions, a dry run")
	}
	flow.Context = ctx
	code, err := flow.PullRequestFlow(settings.Base)
	if err != nil {
		return fail(stderr, "pr", err)
	}
	return code
}

// offlineGitHub answers a dry run with nothing open and nothing released.
type offlineGitHub struct{}

func (offlineGitHub) OpenPullRequests(context.Context) ([]PullRequest, error) { return nil, nil }
func (offlineGitHub) PullRequest(context.Context, int) (PullRequest, error) {
	return PullRequest{}, errors.New("offline")
}
func (offlineGitHub) CreatePullRequest(context.Context, string, string, string, string, bool) (PullRequest, error) {
	return PullRequest{}, errors.New("offline")
}
func (offlineGitHub) UpdatePullRequest(context.Context, int, string, string) error {
	return errors.New("offline")
}
func (offlineGitHub) ClosePullRequest(context.Context, int, string) error {
	return errors.New("offline")
}
func (offlineGitHub) ConvertToDraft(context.Context, string) error { return errors.New("offline") }
func (offlineGitHub) Releases(context.Context) ([]Release, error)  { return nil, nil }
func (offlineGitHub) CreateRelease(context.Context, string, string, string, string, bool) (Release, error) {
	return Release{}, errors.New("offline")
}
func (offlineGitHub) EditRelease(context.Context, int64, Edit) (Release, error) {
	return Release{}, errors.New("offline")
}
