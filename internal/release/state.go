package release

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/config"
)

// State is what the release knows of a commit: its settings, the releases before it, the last
// release, its committer date.
type State struct {
	Commit   string
	Settings Settings
	History  History
	Date     string
}

// StateAt reads a commit's settings (its own jackstay.json) and proves its history.
func (repo Repo) StateAt(name string) (State, error) {
	commit, err := repo.Resolve(name)
	if err != nil || commit == "" {
		return State{}, fmt.Errorf("%s is not a commit", name)
	}
	loaded, err := repo.ConfigAt(commit)
	if err != nil {
		return State{}, fmt.Errorf("the jackstay.json of %s: %w", short(commit), err)
	}
	// The commit acted on now is held to the whole jackstay.json table (unknown keys, missing ones);
	// history is read as written, by Prove, not here.
	sectionProblems := config.SectionProblems(loaded)
	for _, section := range []string{"", "changes", "release", "cards"} {
		if found := sectionProblems.Of(section); len(found) > 0 {
			return State{}, errors.New("the jackstay.json of " + short(commit) + ": " + strings.Join(found, "; "))
		}
	}
	settings, problems := SettingsOf(loaded)
	if len(problems) > 0 {
		return State{}, errors.New("jackstay.json's release settings: " + strings.Join(problems, "; "))
	}
	history, err := repo.HistoryOf(commit, settings.First)
	if err != nil {
		return State{}, err
	}
	date, err := repo.CommitterDate(commit)
	if err != nil {
		return State{}, err
	}
	return State{Commit: commit, Settings: settings, History: history, Date: date}, nil
}

// CheckVersionFiles is section 3's invariant: the version files at the commit hold the last
// release's version (only release commits change them). Exit 2 when not, naming what they say.
func (repo Repo) CheckVersionFiles(state State) error {
	tree := repo.Tree(state.Commit)
	problems, err := repo.ValidateAt(state.Commit, state.Settings)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return &Problem{Code: 2, Message: "jackstay.json at " + short(state.Commit) + ": " + strings.Join(problems, "; ")}
	}
	last := state.History.Last().String()
	for _, file := range state.Settings.Files {
		data, exists, err := tree.Read(file.Path)
		if err != nil {
			return err
		}
		if !exists {
			return &Problem{Code: 2, Message: file.Path + ": the version file is missing"}
		}
		held, err := file.captures(data)
		if err != nil {
			return &Problem{Code: 2, Message: err.Error()}
		}
		if held != last {
			return &Problem{Code: 2, Message: fmt.Sprintf("the version files say %s, the last release is %s (%s): a version value changed outside a release; revert it by hand", held, last, file.Path)}
		}
	}
	return nil
}

// ValidateAt is the release rules a commit's tree must keep, read from git alone (the workflow has
// no checkout to trust, lint judges commits): ref names as git takes them, outputs that are regular
// files, a typed template. Validate adds what needs a working tree.
func (repo Repo) ValidateAt(commit string, settings Settings) ([]string, error) {
	var problems []string
	for _, ref := range []struct{ key, name string }{
		{"release.branch", "refs/heads/" + settings.Branch},
		{"release.base", "refs/heads/" + settings.Base},
		{"release.tag", "refs/tags/" + settings.TagName(settings.First)},
	} {
		if _, err := repo.run("check-ref-format", ref.name); err != nil {
			problems = append(problems, fmt.Sprintf("`%s` gives %s, which git does not take as a ref name", ref.key, ref.name))
		}
	}
	problem, err := repo.outputModes(commit, settings)
	if err != nil {
		return nil, err
	}
	if problem != "" {
		problems = append(problems, problem)
	}
	// A link anywhere on a managed path is a settings error (W-047), found here once.
	managed := append(settings.Outputs(), path.Join(settings.Changes.Dir, "x"))
	for _, managedPath := range managed {
		if link, err := repo.linkOnPath(commit, managedPath); err != nil {
			return nil, err
		} else if link != "" {
			problems = append(problems, link+" is a symbolic link on the managed path "+managedPath)
		}
	}
	if settings.Changes.Template != "" {
		data, exists, err := repo.Tree(commit).Read(settings.Changes.Template)
		if err != nil {
			return nil, err
		}
		if !exists {
			problems = append(problems, "`changes.template` "+settings.Changes.Template+" is missing")
		} else if problem := changes.TemplateProblem(data); problem != "" {
			problems = append(problems, "`changes.template`: "+problem)
		}
	}
	return problems, nil
}

// linkOnPath is the first folder on filePath that is a symbolic link in the commit's tree, or "".
func (repo Repo) linkOnPath(commit, filePath string) (string, error) {
	tree := repo.Tree(commit)
	parts := strings.Split(path.Clean(filePath), "/")
	current := "."
	for _, part := range parts[:len(parts)-1] {
		files, err := tree.List(current)
		if err != nil {
			return "", err
		}
		next := path.Join(current, part)
		for _, file := range files {
			if file.Path == next && file.Mode == changes.ModeLink {
				return next, nil
			}
		}
		current = next
	}
	return "", nil
}

// outputModes says which output, if any, is in the commit's tree as something other than a regular
// file (a release writes only regular files; a link's blob is its target's name).
func (repo Repo) outputModes(commit string, settings Settings) (string, error) {
	tree := repo.Tree(commit)
	for _, output := range settings.Outputs() {
		files, err := tree.List(path.Dir(output))
		if err != nil {
			return "", err
		}
		for _, file := range files {
			if file.Path == output && !file.Regular() {
				return output + " is not a regular file in " + short(commit) + ": a release writes only regular files", nil
			}
		}
	}
	return "", nil
}

// Prepare is P(commit).
func (repo Repo) Prepare(state State) (Prepared, error) {
	listed, err := changes.ListEntries(repo.Tree(state.Commit), state.Settings.Changes.Dir)
	if err != nil {
		return Prepared{}, err
	}
	if problems := state.History.YankProblems(listed); len(problems) > 0 {
		return Prepared{}, &InvalidEntries{Problems: problems}
	}
	headings := map[string]string{}
	for _, landed := range state.History.Releases {
		headings[landed.Version.String()] = landed.Settings.Changes.Heading
	}
	prepared, err := Prepare(Inputs{Tree: repo.Tree(state.Commit), Settings: state.Settings, Last: state.History.Last(), Date: state.Date, ReleaseHeadings: headings})
	prepared.Base = state.Commit
	return prepared, err
}

// Releasable says whether the request file at the commit is releasable there: it agrees with the
// commit (its entries blob for blob and the version computed for them) and matched the base when
// it landed. The reason says why not; a commit without a request file has reason "".
func (repo Repo) Releasable(state State) (request *Request, ok bool, reason string, err error) {
	tree := repo.Tree(state.Commit)
	listed, err := changes.ListEntries(tree, state.Settings.Changes.Dir)
	if err != nil {
		return nil, false, "", err
	}
	if listed.Request == nil {
		return nil, false, "", nil
	}
	data, _, err := tree.Read(listed.Request.Path)
	if err != nil {
		return nil, false, "", err
	}
	read, err := DecodeRequest(data, repo.Format)
	if err != nil {
		return nil, false, err.Error(), nil
	}
	request = &read
	if !listed.Request.Regular() {
		return request, false, "the request file is not a regular file", nil
	}
	next, _, _, _, err := Next(tree, state.Settings, state.History.Last())
	if err != nil {
		return request, false, "the base's entries: " + err.Error(), nil
	}
	if !read.Agrees(listed, next) {
		return request, false, describeMismatch(read, listed, next), nil
	}
	landing, err := repo.LastTouch(state.Commit, listed.Request.Path)
	if err != nil {
		return request, false, "", err
	}
	landed, err := changes.ListEntries(repo.Tree(landing), state.Settings.Changes.Dir)
	if err != nil {
		return request, false, "", err
	}
	if !read.Matches(landed) {
		return request, false, "the request did not match the base when it was merged (" + short(landing) + "): a new request must be merged", nil
	}
	return request, true, "", nil
}

// describeMismatch says how a request differs from the entries it should list.
func describeMismatch(request Request, listed changes.Listed, next Version) string {
	var parts []string
	if request.Version != next.String() {
		parts = append(parts, fmt.Sprintf("it asks for %s, the entries give %s", request.Version, next))
	}
	present := map[string]string{}
	for _, listing := range listed.Entries {
		name := listing.File.Path[strings.LastIndex(listing.File.Path, "/")+1:]
		present[name] = listing.File.Blob
		blob, listedThere := request.Entries[name]
		switch {
		case !listedThere:
			parts = append(parts, name+" landed after it")
		case blob != listing.File.Blob:
			parts = append(parts, name+" changed")
		}
	}
	for name := range request.Entries {
		if _, there := present[name]; !there {
			parts = append(parts, name+" is gone")
		}
	}
	if (request.Override == nil) != (listed.Override == nil) ||
		(request.Override != nil && listed.Override != nil && *request.Override != listed.Override.Blob) {
		parts = append(parts, "the override differs")
	}
	if len(parts) == 0 {
		return "it does not agree with the base"
	}
	return "it does not agree with the base: " + strings.Join(parts, "; ")
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// SettingsProblemsAt is what keeps a commit's settings from working: the whole jackstay.json table, and,
// with a `release` section, ValidateAt, each version file there holding last (the version a
// release's settings must leave in place: the last release, or release.first when there is none),
// and each version and build pattern reading back its value raised by one. For the owner's
// settings pull request (D0), judged before it lands.
func (repo Repo) SettingsProblemsAt(commit string, last *Version) ([]string, error) {
	loaded, err := repo.ConfigAt(commit)
	if err != nil {
		return []string{"jackstay.json cannot be read: " + err.Error()}, nil
	}
	var problems []string
	sectionProblems := config.SectionProblems(loaded)
	for _, section := range sectionProblems.Sections() {
		problems = append(problems, sectionProblems.Of(section)...)
	}
	if loaded.Section("release") == nil || len(problems) > 0 {
		return problems, nil
	}
	settings, settingsProblems := SettingsOf(loaded)
	if len(settingsProblems) > 0 {
		return append(problems, settingsProblems...), nil
	}
	treeProblems, err := repo.ValidateAt(commit, settings)
	if err != nil {
		return nil, err
	}
	problems = append(problems, treeProblems...)
	expected := settings.First
	if last != nil {
		expected = *last
	}
	tree := repo.Tree(commit)
	for _, file := range settings.Files {
		data, exists, err := tree.Read(file.Path)
		if err != nil {
			return nil, err
		}
		if !exists {
			problems = append(problems, file.Path+": the version file is missing")
			continue
		}
		held, err := file.captures(data)
		switch {
		case err != nil:
			problems = append(problems, err.Error())
		case held != expected.String():
			problems = append(problems, fmt.Sprintf("%s holds %s, not %s", file.Path, held, expected))
		default:
			if _, err := file.replace(data, raiseVersion(held)); err != nil {
				problems = append(problems, err.Error())
			}
		}
	}
	if settings.Build != nil {
		data, exists, err := tree.Read(settings.Build.Path)
		if err != nil {
			return nil, err
		}
		if !exists {
			problems = append(problems, settings.Build.Path+": the build file is missing")
		} else if held, err := settings.Build.captures(data); err != nil {
			problems = append(problems, err.Error())
		} else if _, err := strconv.ParseUint(held, 10, 63); err != nil {
			problems = append(problems, fmt.Sprintf("%s: the build number %q is not a decimal integer", settings.Build.Path, held))
		} else if _, err := settings.Build.replace(data, raiseBuild(held)); err != nil {
			problems = append(problems, err.Error())
		}
	}
	return problems, nil
}
