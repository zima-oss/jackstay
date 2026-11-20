package land

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/hosting"
	"github.com/zima-oss/jackstay/internal/lint"
	"github.com/zima-oss/jackstay/internal/release"
)

// The Card line is pr-land's safety check, kept: jst land judges the exact description it writes
// or merges with, against the files of the in-memory merge of the head with the base, by the
// base's `cards` section (as `jst lint pr` does). CI's lint judges the tree's rules; it is read by
// commit, so it cannot vouch for one version of the description: this judgement can.

// cardLine is the `Card:` line --card asks for ("" without one).
func cardLine(ids []string, reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if len(ids) == 0 {
		if reason != "" {
			return "", failf(exitInvalid, "--reason goes with --card none")
		}
		return "", nil
	}
	if slices.Contains(ids, "none") {
		if len(ids) > 1 || reason == "" {
			return "", failf(exitInvalid, "--card none stands alone and needs --reason TEXT")
		}
		return "Card: none (" + reason + ")", nil
	}
	if reason != "" {
		return "", failf(exitInvalid, "--reason goes with --card none")
	}
	var unique []string
	for _, id := range ids {
		if !slices.Contains(unique, id) {
			unique = append(unique, id)
		}
	}
	return "Card: " + strings.Join(unique, ", "), nil
}

// withLine is body with the line added as its last paragraph.
func withLine(body, line string) string {
	if line == "" {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return line + "\n"
	}
	return strings.TrimRight(body, "\n") + "\n\n" + line + "\n"
}

// cardsSectionOf is the `cards` section of a jackstay.json (as bytes) when it puts the Card line
// in the change request's description (`cards.unit` `pr`, the default), else nil.
func cardsSectionOf(data []byte) map[string]any {
	loaded, err := config.Parse(data)
	if err != nil {
		return nil
	}
	section := loaded.Section("cards")
	if section == nil {
		return nil
	}
	if unit, _ := section["unit"].(string); unit != "" && unit != "pr" {
		return nil
	}
	return section
}

func cardExempt(section map[string]any, branch string) bool {
	exempt, _ := section["exempt_branches"].([]any)
	return slices.ContainsFunc(exempt, func(item any) bool { return item == branch })
}

// judgeDescription judges a description's Card line by a base's `cards` section (nil: none to
// judge) against the files of the in-memory merge of head with base.
func (work *session) judgeDescription(cards map[string]any, base, head, body string) error {
	if cards == nil || cardExempt(cards, work.branch) {
		return nil
	}
	out, err := work.git.Run(work.ctx, "merge-tree", "--write-tree", base, head)
	tree, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil || tree == "" {
		return failf(exitRefused, "the merge of %s with %s cannot be made here to judge the Card line (%v)", short(head, 10), short(base, 10), err)
	}
	found, err := lint.CardLines(cards, lint.RevisionFiles(work.ctx, tree, work.git), body)
	if err != nil {
		return failf(exitRefused, "the Card line cannot be checked: %v", err)
	}
	if len(found) == 0 {
		return nil
	}
	problems := make([]string, 0, len(found))
	for _, finding := range found {
		problems = append(problems, finding.Text)
	}
	return failf(exitRefused, "the description fails the Card line's rules:\n- %s\n"+
		"(--card ID adds the line; --card none --reason TEXT for a change that is no card's work)", strings.Join(problems, "\n- "))
}

// judgeChangesLine judges D1 (a changelog entry, or a `Changes: none (reason)` line) on the exact
// description sent, by the base's file, as `jst lint pr` does on the merge (W-047: the base's
// settings alone). The other release rules read only the tree, which CI's required lint judges by
// commit; D1 reads the description, which a check read by commit cannot vouch for.
func (work *session) judgeChangesLine(baseFile []byte, change hosting.Change) error {
	baseConfig, err := config.Parse(baseFile)
	if err != nil || baseConfig.Section("changes") == nil {
		return nil
	}
	root, err := work.gitLine("rev-parse", "--show-toplevel")
	if err != nil {
		return failf(exitRefused, "not a checkout: %v", err)
	}
	mergeBase, err := work.gitLine("merge-base", change.BaseSHA, change.HeadSHA)
	if err != nil {
		return failf(exitRefused, "the merge base of %s and %s cannot be found: %v", short(change.BaseSHA, 10), short(change.HeadSHA, 10), err)
	}
	out, err := work.git.Run(work.ctx, "merge-tree", "--write-tree", change.BaseSHA, change.HeadSHA)
	tree, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil || tree == "" {
		return failf(exitRefused, "the merge of %s with %s cannot be made here to judge the description (%v)",
			short(change.HeadSHA, 10), short(change.BaseSHA, 10), err)
	}
	found, _, err := lint.ReleaseRules(release.NewRepo(root), baseConfig, lint.Change{Base: change.BaseSHA, Head: change.HeadSHA,
		MergeBase: mergeBase, HeadBranch: change.Head, Body: change.Body, Judged: lint.RevisionFiles(work.ctx, tree, work.git)})
	if err != nil {
		return failf(exitRefused, "the changelog line cannot be checked: %v", err)
	}
	var problems []string
	for _, finding := range found {
		if finding.Rule == lint.RuleUndocumented || finding.Rule == lint.RuleBothDocumented {
			problems = append(problems, fmt.Sprintf("[%s] %s", finding.Rule, finding.Text))
		}
	}
	if len(problems) > 0 {
		return failf(exitRefused, "the description that would be merged fails the changelog rule:\n- %s", strings.Join(problems, "\n- "))
	}
	return nil
}

// cardsNeedAMergeCommit refuses `land.method: rebase` where the base's file puts the Card line in
// the description: a rebase writes no merge commit, so the line would not land.
func cardsNeedAMergeCommit(method hosting.Method, inDescription bool) error {
	if method == hosting.Rebase && inDescription {
		return failf(exitInvalid, "`land.method` is rebase, and the base's Card line is in the change request's "+
			"description, which a rebase does not land: use squash or merge")
	}
	return nil
}
