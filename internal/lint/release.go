package lint

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/release"
)

// Rule ids of the release rules (docs/design/release.md, section 7).
const (
	RuleUndocumented     = "D1-undocumented"
	RuleBothDocumented   = "D1-entry-and-none"
	RuleInvalidEntry     = "D2-invalid-entry"
	RuleVersionChanged   = "D3-version-changed"
	RuleChangelogChanged = "D3-changelog-changed"
	RuleRequestChanged   = "D3-request-changed"
	RuleMergeVersion     = "D3-merge-version"
	RuleRequestOnly      = "D4-not-request-only"
	RuleSettingsAlone    = "D0-settings-alone"
	RuleInvalidRequest   = "D4-invalid-request"
)

// Change is a pull request's inputs for the release rules.
type Change struct {
	Base       string // b
	Head       string // h
	MergeBase  string // m = merge-base(b, h)
	HeadBranch string // after a queue copy is resolved to its original
	Body       string
	Judged     Files // the tree the check judges (the merge)
}

// noneLine is `Changes: none (REASON)` with a reason.
func noneReason(body string) (string, bool) {
	text := strings.ReplaceAll(body, "\r\n", "\n")
	for _, line := range strings.Split(text, "\n") {
		value, found := strings.CutPrefix(line, "Changes: none")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") && strings.TrimSpace(value[1:len(value)-1]) != "" {
			return strings.TrimSpace(value[1 : len(value)-1]), true
		}
		return "", true
	}
	return "", false
}

// ReleaseRules judges a pull request by D0–D4 with the base's jackstay.json alone (W-047: jackstay.json is
// fixed settings; a pull request that changes it changes nothing else, and no rule reads the pull
// request's settings). notes are what D4's freshness says, never a failure.
func ReleaseRules(repo release.Repo, baseConfig config.Config, change Change) (found []Finding, notes []string, err error) {
	diff, err := repo.Changes(change.MergeBase, change.Head)
	if err != nil {
		return nil, nil, err
	}
	settingsChanged := false
	for _, item := range diff {
		settingsChanged = settingsChanged || item.Path == config.Name
	}
	if settingsChanged && len(diff) > 1 {
		found = append(found, Finding{Rule: RuleSettingsAlone, Text: "the pull request changes " + config.Name + " and other files: settings change in a pull request of their own, merged by the owner (W-047)"})
	}
	if settingsChanged {
		// The settings that would land must work, whether they change a release or adopt one: the
		// version files there hold the last release (release.first when there is none yet).
		var last *release.Version
		if baseConfig.Section("release") != nil {
			if state, err := repo.StateAt(change.Base); err == nil {
				version := state.History.Last()
				last = &version
			}
		}
		problems, err := repo.SettingsProblemsAt(change.Head, last)
		if err != nil {
			return nil, nil, err
		}
		for _, problem := range problems {
			found = append(found, Finding{Rule: RuleSettingsAlone, Text: "the new settings: " + problem})
		}
	}
	if baseConfig.Section("changes") == nil {
		return found, nil, nil
	}
	exempt := false
	if section := baseConfig.Section("cards"); section != nil {
		list, _ := section["exempt_branches"].([]any)
		for _, item := range list {
			if name, _ := item.(string); name == change.HeadBranch {
				exempt = true
			}
		}
	}
	changeSettings, err := changes.SettingsOf(baseConfig)
	if err != nil {
		return nil, nil, err
	}
	hasRelease := baseConfig.Section("release") != nil
	var settings release.Settings
	var baseState release.State
	if hasRelease {
		var problems []string
		settings, problems = release.SettingsOf(baseConfig)
		if len(problems) > 0 {
			return nil, nil, fmt.Errorf("the base's release settings: %s", strings.Join(problems, "; "))
		}
		if baseState, err = repo.StateAt(change.Base); err != nil {
			return nil, nil, err
		}
	}
	isRequest := hasRelease && change.HeadBranch == settings.Branch
	headTree := repo.Tree(change.Head)
	entriesDir := path.Clean(changeSettings.Dir)
	listed, err := changes.ListEntries(headTree, entriesDir)
	if err != nil {
		return nil, nil, err
	}
	documented := false
	for _, item := range diff {
		if path.Dir(item.Path) != entriesDir || item.Status == "D" {
			continue
		}
		name := path.Base(item.Path)
		switch {
		case name == changes.OverrideName:
			documented = true
			found = append(found, overrideRules(repo, change, item.Path, hasRelease)...)
		case strings.HasSuffix(name, ".md") && changes.IsSlug(strings.TrimSuffix(name, ".md")):
			documented = true
			for _, listing := range listed.Entries {
				if listing.File.Path != item.Path {
					continue
				}
				for _, problem := range changes.EntryProblems(listing, changeSettings) {
					found = append(found, Finding{Rule: RuleInvalidEntry, Text: item.Path + ": " + problem})
				}
			}
		}
	}
	if hasRelease {
		for _, problem := range baseState.History.YankProblems(listed) {
			found = append(found, Finding{Rule: RuleInvalidEntry, Text: problem})
		}
	}
	if !isRequest && !exempt {
		reason, hasNone := noneReason(change.Body)
		switch {
		case documented && hasNone:
			found = append(found, Finding{Rule: RuleBothDocumented, Text: "the pull request adds an entry and says `Changes: none`: one or the other"})
		case !documented && !hasNone:
			found = append(found, Finding{Rule: RuleUndocumented, Text: fmt.Sprintf("no changelog entry in %s and no `Changes: none (<reason>)` line in the description (`jst changes new`)", entriesDir)})
		case hasNone && reason == "":
			found = append(found, Finding{Rule: RuleUndocumented, Text: "`Changes: none` needs a reason in brackets: `Changes: none (<reason>)`"})
		}
	}
	if !hasRelease {
		return found, nil, nil
	}
	if isRequest {
		requestFindings, requestNotes, err := requestRules(repo, settings, change, diff)
		return append(found, requestFindings...), requestNotes, err
	}
	versionFindings, err := versionRules(repo, baseState, change, diff)
	return append(found, versionFindings...), nil, err
}

// requestRules is D4: the release pull request changes the request file and nothing else, and the
// file is a request; its freshness is said, never failed.
func requestRules(repo release.Repo, settings release.Settings, change Change, diff []release.Change) ([]Finding, []string, error) {
	var found []Finding
	for _, item := range diff {
		if item.Path != settings.RequestPath() {
			found = append(found, Finding{Rule: RuleRequestOnly, Text: "the release pull request changes " + item.Path + ": it changes the request file only (W-034)"})
		}
	}
	data, exists, err := repo.Tree(change.Head).Read(settings.RequestPath())
	if err != nil {
		return nil, nil, err
	}
	if !exists {
		return append(found, Finding{Rule: RuleInvalidRequest, Text: "the release pull request has no request file"}), nil, nil
	}
	if !regularAt(repo, change.Head, settings.RequestPath()) {
		return append(found, Finding{Rule: RuleInvalidRequest, Text: settings.RequestPath() + " is not a regular file"}), nil, nil
	}
	request, err := release.DecodeRequest(data, repo.Format)
	if err != nil {
		return append(found, Finding{Rule: RuleInvalidRequest, Text: settings.RequestPath() + ": " + err.Error()}), nil, nil
	}
	var notes []string
	state, err := repo.StateAt(change.Base)
	if err != nil {
		return found, []string{"the base's release state could not be read: " + err.Error()}, nil
	}
	listed, err := changes.ListEntries(repo.Tree(change.Base), settings.Changes.Dir)
	if err != nil {
		return nil, nil, err
	}
	next, _, _, _, nextErr := release.Next(repo.Tree(change.Base), state.Settings, state.History.Last())
	if nextErr != nil || !request.Agrees(listed, next) {
		notes = append(notes, "the request does not agree with the base now (the workflow rebuilds it after the base moves); landing it would release nothing")
	}
	return found, notes, nil
}

// versionRules is D3, by the base's settings alone: the request file, the changelogs and the version
// and build values are a release's; in what would land, the values are the base's released ones,
// the changelogs the base's, the outputs regular files. A settings-only pull request (D0) is checked
// to leave valid settings behind.
func versionRules(repo release.Repo, base release.State, change Change, diff []release.Change) ([]Finding, error) {
	var found []Finding
	settings := base.Settings
	protected := map[string]string{settings.RequestPath(): RuleRequestChanged, settings.Changes.Into: RuleChangelogChanged}
	if settings.Changes.InternalInto != "" {
		protected[settings.Changes.InternalInto] = RuleChangelogChanged
	}
	for _, item := range diff {
		if rule, ok := protected[item.Path]; ok {
			found = append(found, Finding{Rule: rule, Text: item.Path + " is written by releases only"})
		}
	}
	last := base.History.Last().String()
	baseTree := repo.Tree(change.Base)
	baseBuild := ""
	if settings.Build != nil {
		if data, exists, _ := baseTree.Read(settings.Build.Path); exists {
			baseBuild, _ = captured(*settings.Build, data)
		}
	}
	trees := []struct {
		files fileReader
		where string
		rule  string
	}{{repo.Tree(change.Head), "", RuleVersionChanged}}
	if change.Judged != nil {
		trees = append(trees, struct {
			files fileReader
			where string
			rule  string
		}{change.Judged, "in the merge, ", RuleMergeVersion})
	}
	for _, tree := range trees {
		for _, file := range settings.Files {
			content, _, _ := tree.files.Read(file.Path)
			if value, err := captured(file, content); err != nil || value != last {
				found = append(found, Finding{Rule: tree.rule, Text: fmt.Sprintf("%s%s does not hold the last release %s: versions are written by releases only", tree.where, file.Path, last)})
			}
		}
		if settings.Build != nil {
			content, _, _ := tree.files.Read(settings.Build.Path)
			value, err := captured(*settings.Build, content)
			if _, notNumber := strconv.ParseUint(value, 10, 63); err != nil || notNumber != nil || (baseBuild != "" && value != baseBuild) {
				found = append(found, Finding{Rule: tree.rule, Text: fmt.Sprintf("%s%s does not hold the base's build number %s", tree.where, settings.Build.Path, baseBuild)})
			}
		}
	}
	if change.Judged != nil {
		for _, output := range []string{settings.Changes.Into, settings.Changes.InternalInto} {
			if output == "" {
				continue
			}
			before, beforeExists, _ := baseTree.Read(output)
			after, afterExists, _ := change.Judged.Read(output)
			if beforeExists != afterExists || string(before) != string(after) {
				found = append(found, Finding{Rule: RuleChangelogChanged, Text: "in the merge, " + output + " is not the base's: changelogs are written by releases only"})
			}
		}
		if moder, ok := change.Judged.(interface{ Regular(string) (bool, error) }); ok {
			for _, output := range settings.Outputs() {
				if _, exists, _ := change.Judged.Read(output); !exists {
					continue
				}
				if regular, err := moder.Regular(output); err != nil || !regular {
					found = append(found, Finding{Rule: RuleMergeVersion, Text: "in the merge, " + output + " is not a regular file"})
				}
			}
		}
	}
	return found, nil
}

// fileReader is a tree as far as reading a file goes.
type fileReader interface {
	Read(filePath string) (data []byte, exists bool, err error)
}

func captured(file release.VersionFile, data []byte) (string, error) {
	return release.Captured(file, data)
}

// regularAt says whether a path is a regular file in a commit's tree.
func regularAt(repo release.Repo, commit, filePath string) bool {
	files, err := repo.Tree(commit).List(path.Dir(filePath))
	if err != nil {
		return false
	}
	for _, file := range files {
		if file.Path == filePath {
			return file.Regular()
		}
	}
	return false
}

// overrideRules is D2 for an override the pull request adds or changes: a regular file, the
// override's shape, a version greater than the last release at the base.
func overrideRules(repo release.Repo, change Change, filePath string, hasRelease bool) []Finding {
	if !hasRelease {
		return []Finding{{Rule: RuleInvalidEntry, Text: filePath + ": an override needs a `release` section"}}
	}
	if !regularAt(repo, change.Head, filePath) {
		return []Finding{{Rule: RuleInvalidEntry, Text: filePath + " is not a regular file"}}
	}
	data, _, err := repo.Tree(change.Head).Read(filePath)
	if err != nil {
		return []Finding{{Rule: RuleInvalidEntry, Text: filePath + ": " + err.Error()}}
	}
	override, err := release.DecodeOverride(data)
	if err != nil {
		return []Finding{{Rule: RuleInvalidEntry, Text: filePath + ": " + err.Error()}}
	}
	state, err := repo.StateAt(change.Base)
	if err != nil {
		return []Finding{{Rule: RuleInvalidEntry, Text: filePath + ": the base's last release cannot be read: " + err.Error()}}
	}
	version, _ := release.ParseVersion(override.Version)
	if !state.History.Last().Less(version) {
		return []Finding{{Rule: RuleInvalidEntry, Text: fmt.Sprintf("%s: %s is not greater than the last release %s", filePath, version, state.History.Last())}}
	}
	return nil
}
