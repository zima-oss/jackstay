package release

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/changes"
)

// trailerPattern finds the candidates of release commits (git's --grep, extended).
const trailerPattern = `^Jackstay-Release: [0-9]+\.[0-9]+\.[0-9]+$`

// Landed is a proven release commit.
type Landed struct {
	Commit  string
	Parent  string
	Version Version
	// Settings are the release's own, read from its parent's jackstay.json: its tag's format, its paths.
	Settings Settings
}

// Tag is the release's tag, in its own settings' format.
func (landed Landed) Tag() string { return landed.Settings.TagName(landed.Version) }

// Unproven is a commit with the trailer that is not a release commit, and why.
type Unproven struct {
	Commit string
	Reason string
}

// History is the releases on a tip's first-parent line, proven oldest first, and the commits that
// carry the trailer without being releases.
type History struct {
	Releases []Landed
	Unproven []Unproven
	// First is release.first of the tip's settings: the last release before any release commit.
	First Version
}

// Last is the last release: the newest release commit's version, else release.first.
func (history History) Last() Version {
	if len(history.Releases) == 0 {
		return history.First
	}
	return history.Releases[len(history.Releases)-1].Version
}

// LastBefore is the last release before the release commit at a position: the newest one before it
// (release commits are listed oldest first), else release.first.
func (history History) LastBefore(position int) Version {
	if position <= 0 {
		return history.First
	}
	return history.Releases[position-1].Version
}

// HistoryOf proves every candidate on tip's first-parent line. It refuses a shallow clone: the
// line could not be walked.
func (repo Repo) HistoryOf(tip string, first Version) (History, error) {
	shallow, err := repo.Shallow()
	if err != nil {
		return History{}, err
	}
	if shallow {
		return History{}, fmt.Errorf("the clone is shallow: the first-parent line cannot be walked (fetch with full history)")
	}
	candidates, err := repo.FirstParentWith(tip, trailerPattern)
	if err != nil {
		return History{}, err
	}
	history := History{First: first}
	for _, candidate := range candidates {
		// Before the first proven release, the baseline is the candidate's parent's release.first,
		// not today's: a later edit of release.first does not unprove history.
		var previous *Version
		if len(history.Releases) > 0 {
			last := history.Last()
			previous = &last
		}
		proven, reason, err := repo.Prove(candidate, previous)
		if err != nil {
			return History{}, err
		}
		if reason != "" {
			history.Unproven = append(history.Unproven, Unproven{Commit: candidate, Reason: reason})
			continue
		}
		history.Releases = append(history.Releases, proven)
	}
	return history, nil
}

// trailerVersion is the version of the message's last paragraph `Jackstay-Release: X.Y.Z`.
func trailerVersion(message string) (Version, bool) {
	trimmed := strings.TrimRight(message, "\n ")
	lastParagraph := trimmed
	if at := strings.LastIndex(trimmed, "\n\n"); at >= 0 {
		lastParagraph = trimmed[at+2:]
	}
	value, found := strings.CutPrefix(lastParagraph, "Jackstay-Release: ")
	if !found || strings.Contains(value, "\n") {
		return Version{}, false
	}
	version, err := ParseVersion(strings.TrimSpace(value))
	return version, err == nil
}

// Prove says whether commit is a release commit given the last release at its first parent; the
// reason is "" when it is. The proof is about what the commit changed, read with its parent's
// jackstay.json; no bump is recomputed and nothing is rendered again (docs/design/release.md, section 3).
func (repo Repo) Prove(commit string, previous *Version) (Landed, string, error) {
	message, err := repo.Message(commit)
	if err != nil {
		return Landed{}, "", err
	}
	version, ok := trailerVersion(message)
	if !ok {
		return Landed{}, "its message does not end with the paragraph `Jackstay-Release: X.Y.Z`", nil
	}
	parent, err := repo.Parent(commit)
	if err != nil {
		return Landed{}, "", err
	}
	if parent == "" {
		return Landed{}, "it has no parent", nil
	}
	loaded, err := repo.ConfigAt(parent)
	if err != nil {
		return Landed{}, "its parent's jackstay.json cannot be read: " + err.Error(), nil
	}
	settings, problems := SettingsOf(loaded)
	if len(problems) > 0 {
		return Landed{}, "its parent's release settings: " + strings.Join(problems, "; "), nil
	}
	last := settings.First
	if previous != nil {
		last = *previous
	}
	if !last.Less(version) {
		return Landed{}, fmt.Sprintf("%s is not greater than the last release %s", version, last), nil
	}
	parentTree := repo.Tree(parent)
	listed, err := changes.ListEntries(parentTree, settings.Changes.Dir)
	if err != nil {
		return Landed{}, "", err
	}
	if listed.Request == nil {
		return Landed{}, "its parent holds no release request", nil
	}
	modes := []changes.File{*listed.Request}
	for _, listing := range listed.Entries {
		modes = append(modes, listing.File)
	}
	if listed.Override != nil {
		modes = append(modes, *listed.Override)
	}
	for _, file := range modes {
		if !file.Regular() {
			return Landed{}, file.Path + " in its parent is not a regular file", nil
		}
	}
	if problem, err := repo.outputModes(parent, settings); err != nil || problem != "" {
		return Landed{}, problem, err
	}
	data, _, err := parentTree.Read(listed.Request.Path)
	if err != nil {
		return Landed{}, "", err
	}
	request, err := DecodeRequest(data, repo.Format)
	if err != nil {
		return Landed{}, "its parent's request: " + err.Error(), nil
	}
	if request.Version != version.String() {
		return Landed{}, fmt.Sprintf("its parent's request asks for %s", request.Version), nil
	}
	if !request.Matches(listed) {
		return Landed{}, "its parent's request does not list exactly its parent's entries", nil
	}
	landing, err := repo.LastTouch(parent, listed.Request.Path)
	if err != nil {
		return Landed{}, "", err
	}
	landed, err := changes.ListEntries(repo.Tree(landing), settings.Changes.Dir)
	if err != nil {
		return Landed{}, "", err
	}
	if !request.Matches(landed) {
		return Landed{}, "its request did not match the base when it was merged", nil
	}
	reason, err := repo.changedOnlyARelease(parent, commit, settings, listed, last, version)
	if err != nil || reason != "" {
		return Landed{}, reason, err
	}
	return Landed{Commit: commit, Parent: parent, Version: version, Settings: settings}, "", nil
}

// changedOnlyARelease checks the commit's diff: the entries, the override and the request
// removed; the version and build values moved to the release's; the changelogs changed; nothing
// else touched.
func (repo Repo) changedOnlyARelease(parent, commit string, settings Settings, listed changes.Listed, last, version Version) (string, error) {
	removed := map[string]bool{listed.Request.Path: true}
	for _, listing := range listed.Entries {
		removed[listing.File.Path] = true
	}
	if listed.Override != nil {
		removed[listed.Override.Path] = true
	}
	allowed := map[string]bool{}
	for _, output := range settings.Outputs() {
		allowed[output] = true
	}
	found, err := repo.Changes(parent, commit)
	if err != nil {
		return "", err
	}
	changedOutputs := map[string]bool{}
	for _, change := range found {
		switch {
		case removed[change.Path]:
			if change.Status != "D" {
				return change.Path + " should be removed by a release", nil
			}
			delete(removed, change.Path)
		case allowed[change.Path]:
			if change.Status == "D" {
				return change.Path + " is removed", nil
			}
			changedOutputs[change.Path] = true
		default:
			return "it changes " + change.Path + ", which a release does not", nil
		}
	}
	if len(removed) > 0 {
		var left []string
		for filePath := range removed {
			left = append(left, path.Base(filePath))
		}
		return "it does not remove " + strings.Join(left, ", "), nil
	}
	if !changedOutputs[settings.Changes.Into] {
		return settings.Changes.Into + " is not written: a release writes its section", nil
	}
	internalEntries := false
	for _, listing := range listed.Entries {
		internalEntries = internalEntries || listing.Entry.Audience == changes.Internal
	}
	if internalEntries && settings.Changes.InternalInto != "" && !changedOutputs[settings.Changes.InternalInto] {
		return settings.Changes.InternalInto + " is not written although the release has internal entries", nil
	}
	tree := repo.Tree(commit)
	for _, output := range settings.Outputs() {
		files, err := tree.List(path.Dir(output))
		if err != nil {
			return "", err
		}
		for _, file := range files {
			if file.Path == output && !file.Regular() {
				return output + " is not a regular file", nil
			}
		}
	}
	parentTree := repo.Tree(parent)
	for _, file := range settings.Files {
		before, _, err := parentTree.Read(file.Path)
		if err != nil {
			return "", err
		}
		if held, err := file.captures(before); err != nil || held != last.String() {
			return file.Path + " did not hold the last release " + last.String() + " before it", nil
		}
		data, _, err := tree.Read(file.Path)
		if err != nil {
			return "", err
		}
		held, err := file.captures(data)
		if err != nil || held != version.String() {
			return file.Path + " does not hold " + version.String(), nil
		}
	}
	if settings.Build != nil {
		before, _, err := repo.Tree(parent).Read(settings.Build.Path)
		if err != nil {
			return "", err
		}
		after, _, err := tree.Read(settings.Build.Path)
		if err != nil {
			return "", err
		}
		oldValue, errOld := settings.Build.captures(before)
		newValue, errNew := settings.Build.captures(after)
		oldNumber, errOldNumber := strconv.ParseUint(oldValue, 10, 63)
		newNumber, errNewNumber := strconv.ParseUint(newValue, 10, 63)
		if errOld != nil || errNew != nil || errOldNumber != nil || errNewNumber != nil || newNumber != oldNumber+1 {
			return settings.Build.Path + " does not move the build number up by one", nil
		}
	}
	return "", nil
}

// YankProblems are the entries whose `yanks` names no release of the history.
func (history History) YankProblems(listed changes.Listed) []string {
	released := map[string]bool{}
	for _, landed := range history.Releases {
		released[landed.Version.String()] = true
	}
	var problems []string
	for _, listing := range listed.Entries {
		if yanks := listing.Entry.Yanks; yanks != "" && !released[yanks] {
			problems = append(problems, listing.File.Path+": it yanks "+yanks+", which is no release on this branch")
		}
	}
	return problems
}
