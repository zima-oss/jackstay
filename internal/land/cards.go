package land

import (
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/hosting"
	"github.com/zima-oss/jackstay/internal/lint"
)

// cardSection is the project's `cards` section when its unit is the change request, else nil.
func (work *session) cardSection() map[string]any {
	section := work.config.Section("cards")
	if section == nil {
		return nil
	}
	if unit, _ := section["unit"].(string); unit != "" && unit != "pr" {
		return nil
	}
	return section
}

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

func cardExempt(section map[string]any, branch string) bool {
	exempt, _ := section["exempt_branches"].([]any)
	return slices.ContainsFunc(exempt, func(item any) bool { return item == branch })
}

// judgeDescription refuses a description that breaks the Card line's rules against the files of
// a tree (none for a branch the section exempts): internal/lint's, the sentences `jst lint pr`'s.
func judgeDescription(section map[string]any, files lint.Files, body, branch string) error {
	if cardExempt(section, branch) {
		return nil
	}
	found, err := lint.CardLines(section, files, body)
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

// judgeDescriptionOf judges, before a merge in a project with cards, the description as the host
// holds it against the files of what would land: the merge of the head that was checked with the
// base commit it was judged at, made in memory (a card added to the base since the branch split
// is there). Only the Card line is judged here; the tree's rules are CI's (W-092). It returns the
// description judged: the merge commit's message.
func (work *session) judgeDescriptionOf(change hosting.Change) (string, error) {
	out, err := work.git.Run(work.ctx, "merge-tree", "--write-tree", change.BaseSHA, change.HeadSHA)
	tree, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil || tree == "" {
		return "", failf(exitRefused, "#%d: the merge of %s with %s cannot be made here (%v); not merging", change.Number,
			short(change.HeadSHA, 10), short(change.BaseSHA, 10), err)
	}
	if err := judgeDescription(work.cardSection(), lint.RevisionFiles(work.ctx, tree, work.git), change.Body, work.branch); err != nil {
		return "", err
	}
	work.say("#%d: its Card line is fine on the merge with %s at %s", change.Number, change.Base, short(change.BaseSHA, 8))
	return change.Body, nil
}
