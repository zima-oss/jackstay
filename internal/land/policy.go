package land

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/hosting"
)

// policy is step 2's decision, bound to the base and head it was made for.
type policy struct {
	base     config.Land
	hasBase  bool
	baseSHA  string
	headSHA  string
	agentMay bool
	// cards is the base's `cards` section where it puts the Card line in the description, else nil.
	cards map[string]any
	// reasons say why the change is a person's.
	reasons []string
}

// projectFile is jackstay's file; its policy is read from the base's copy and the change's.
const projectFile = "jackstay.json"

// basePolicyAtTip is the `land` section of the base's file at the remote's tip of the base, for
// the plan (step 2 judges it again at the change's own base commit).
func (work *session) basePolicyAtTip() (config.Land, bool, error) {
	if _, err := work.git.Run(work.ctx, "fetch", "--quiet", "--", work.remoteName,
		"refs/heads/"+work.base+":refs/remotes/"+work.remoteName+"/"+work.base); err != nil {
		return config.Land{}, false, failf(exitRefused, "%s cannot be fetched from %s: %v", work.base, work.remoteName, err)
	}
	return work.policyAt("refs/remotes/" + work.remoteName + "/" + work.base)
}

// fileAt is jackstay.json at a revision (nil when there is none).
func (work *session) fileAt(revision string) []byte {
	if _, err := work.git.Run(work.ctx, "cat-file", "-e", revision+":"+projectFile); err != nil {
		return nil
	}
	data, err := work.git.Run(work.ctx, "show", revision+":"+projectFile)
	if err != nil {
		return nil
	}
	return data
}

// policyAt is the `land` section of jackstay.json at a revision: (zero, false) when the file or
// the section is not there.
func (work *session) policyAt(revision string) (config.Land, bool, error) {
	if _, err := work.git.Run(work.ctx, "cat-file", "-e", revision+":"+projectFile); err != nil {
		return config.Land{}, false, nil
	}
	data, err := work.git.Run(work.ctx, "show", revision+":"+projectFile)
	if err != nil {
		return config.Land{}, false, failf(exitRefused, "%s at %s cannot be read: %v", projectFile, short(revision, 10), err)
	}
	land, found, err := config.LandOf(data)
	if err != nil {
		return config.Land{}, false, failf(exitRefused, "the `land` section of %s at %s cannot be used: %v", projectFile, short(revision, 10), err)
	}
	return land, found, nil
}

// judgePolicy is step 2: the paths the change touches (merge base to head), against the fixed
// human paths and `human_paths` from the base's file and from the change's. Autonomy is read from
// the base's file only, so a branch cannot keep a permission the base has withdrawn.
func (work *session) judgePolicy(change hosting.Change) (policy, error) {
	if err := work.haveCommit(change.Number, change.HeadSHA); err != nil {
		return policy{}, err
	}
	if err := work.haveBase(change); err != nil {
		return policy{}, err
	}
	decision := policy{baseSHA: change.BaseSHA, headSHA: change.HeadSHA}
	var err error
	if decision.base, decision.hasBase, err = work.policyAt(change.BaseSHA); err != nil {
		return policy{}, err
	}
	decision.cards = cardsSectionOf(work.fileAt(change.BaseSHA))
	changed, hasChanged, err := work.policyAt(change.HeadSHA)
	if err != nil {
		return policy{}, err
	}
	mergeBase, err := work.gitLine("merge-base", change.BaseSHA, change.HeadSHA)
	if err != nil {
		return policy{}, failf(exitRefused, "#%d: the merge base of %s and %s cannot be found: %v", change.Number,
			short(change.BaseSHA, 10), short(change.HeadSHA, 10), err)
	}
	out, err := work.git.Run(work.ctx, "diff", "--name-only", "--no-renames", "-z", mergeBase, change.HeadSHA, "--")
	if err != nil {
		return policy{}, failf(exitRefused, "#%d: the paths it touches cannot be listed: %v", change.Number, err)
	}
	patterns := slices.Clone(config.FixedHumanPaths)
	if decision.hasBase {
		patterns = append(patterns, decision.base.HumanPaths...)
	}
	if hasChanged {
		patterns = append(patterns, changed.HumanPaths...)
	}
	var touched []string
	for _, file := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if file != "" && slices.ContainsFunc(patterns, func(pattern string) bool { return humanPathMatches(pattern, file) }) {
			touched = append(touched, file)
		}
	}
	switch {
	case !decision.hasBase:
		decision.reasons = append(decision.reasons, fmt.Sprintf("%s has no landing policy yet (no `land` section in its %s)", change.Base, projectFile))
	case work.options.prepare:
		decision.reasons = append(decision.reasons, "--prepare")
	case decision.base.Autonomy != config.AutonomyMergeWhenGreen:
		decision.reasons = append(decision.reasons, fmt.Sprintf("`land.autonomy` is %s on %s", decision.base.Autonomy, change.Base))
	}
	if len(touched) > 0 {
		shown := touched[:min(len(touched), 5)]
		more := ""
		if len(touched) > len(shown) {
			more = fmt.Sprintf(" (+%d)", len(touched)-len(shown))
		}
		decision.reasons = append(decision.reasons, "it touches paths only a person lands: "+strings.Join(shown, ", ")+more)
	}
	decision.agentMay = len(decision.reasons) == 0
	return decision, nil
}

// humanPathMatches matches a path against a human-path glob: `**` is any number of whole path
// segments; other wildcards stay inside one segment (path.Match).
func humanPathMatches(pattern, file string) bool {
	return segmentsMatch(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func segmentsMatch(pattern, file []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			rest := pattern[1:]
			for skip := 0; skip <= len(file); skip++ {
				if segmentsMatch(rest, file[skip:]) {
					return true
				}
			}
			return false
		}
		if len(file) == 0 {
			return false
		}
		if matched, err := path.Match(pattern[0], file[0]); err != nil || !matched {
			return false
		}
		pattern, file = pattern[1:], file[1:]
	}
	return len(file) == 0
}
