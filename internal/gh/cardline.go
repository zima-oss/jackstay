package gh

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/lint"
)

// cardSection is the project's `cards` section when its unit is the pull request, else nil.
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
			return "", failf(exitRequest, "--reason goes with --card none")
		}
		return "", nil
	}
	if slices.Contains(ids, "none") {
		if len(ids) > 1 || reason == "" {
			return "", failf(exitRequest, "--card none stands alone and needs --reason TEXT")
		}
		return "Card: none (" + reason + ")", nil
	}
	if reason != "" {
		return "", failf(exitRequest, "--reason goes with --card none")
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

// descriptionProblems are the Card line's rules broken by body against the files of a tree (none
// for a branch the section exempts): internal/lint's, the sentences `jst lint pr`'s.
func descriptionProblems(section map[string]any, files lint.Files, body, branch string) ([]string, error) {
	exempt, _ := section["exempt_branches"].([]any)
	for _, item := range exempt {
		if name, _ := item.(string); name == branch {
			return nil, nil
		}
	}
	found, err := lint.CardLines(section, files, body)
	if err != nil {
		return nil, fmt.Errorf("the Card line cannot be checked: %w", err)
	}
	problems := make([]string, 0, len(found))
	for _, finding := range found {
		problems = append(problems, finding.Text)
	}
	return problems, nil
}

// judgeDescription fails (exit 1) naming each rule body breaks.
func judgeDescription(section map[string]any, files lint.Files, body, branch string) error {
	problems, err := descriptionProblems(section, files, body, branch)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return failf(exitNotFine, "the description fails the Card line's rules:\n- %s\n"+
			"(--card ID adds the line; --card none --reason TEXT for a change that is no card's work)", strings.Join(problems, "\n- "))
	}
	return nil
}

// judgeMerge judges, before a merge, the description as GitHub holds it now and the tree's rules
// on the merge of the head seen green with the pull request's own base as just fetched (made in
// memory). It returns the description judged: the squash commit's body.
func (work *session) judgeMerge(number int, head, branch string) (string, error) {
	pull, err := work.github.PullRequest(work.ctx, number)
	if err != nil {
		return "", fmt.Errorf("pull request #%d: %w", number, err)
	}
	base := pull.Base // the pull request's own base, which need not be the default branch
	remoteName, err := work.pushRemote(branch)
	if err != nil {
		return "", err
	}
	baseRef := "refs/remotes/" + remoteName + "/" + base
	if _, err := work.git.Run(work.ctx, "fetch", "--quiet", remoteName, "refs/heads/"+base+":"+baseRef); err != nil {
		return "", fmt.Errorf("#%d: %s cannot be fetched from %s: %w", number, base, remoteName, err)
	}
	if err := work.haveCommit(number, head, remoteName, branch); err != nil {
		return "", err
	}
	out, err := work.git.Run(work.ctx, "merge-tree", "--write-tree", baseRef, head)
	tree, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil || tree == "" {
		return "", failf(exitNotFine, "#%d: the merge of %s with %s/%s cannot be made here (%v); not merging", number, short(head, 10), remoteName, base, err)
	}
	section := work.cardSection()
	files := lint.RevisionFiles(work.ctx, tree, work.git)
	if err := judgeDescription(section, files, pull.Body, branch); err != nil {
		return "", err
	}
	found, err := lint.TreeRules(work.config, files, lint.GitHistory(work.ctx, work.git, baseRef, head))
	if err != nil {
		return "", failf(exitRequest, "the tree's rules cannot be checked: %v", err)
	}
	if len(found) > 0 {
		var lines []string
		for _, finding := range found {
			lines = append(lines, fmt.Sprintf("- [%s] %s", finding.Rule, finding.Text))
		}
		return "", failf(exitNotFine, "#%d merged with %s as just fetched breaks the tree's rules; not merging:\n%s", number, base, strings.Join(lines, "\n"))
	}
	work.say("#%d: Card line and tree's rules fine on the merge with %s as just fetched", number, base)
	return pull.Body, nil
}

// haveCommit makes sure the checkout has the commit (the head may have been made elsewhere: an
// update of the branch on GitHub, another checkout): fetched by the pull request's ref, then the
// branch.
func (work *session) haveCommit(number int, commit, remoteName, branch string) error {
	present := func() bool {
		_, err := work.git.Run(work.ctx, "cat-file", "-e", commit+"^{commit}")
		return err == nil
	}
	if present() {
		return nil
	}
	_, _ = work.git.Run(work.ctx, "fetch", "--quiet", remoteName, fmt.Sprintf("refs/pull/%d/head", number))
	if present() {
		return nil
	}
	_, _ = work.git.Run(work.ctx, "fetch", "--quiet", remoteName, "refs/heads/"+branch)
	if present() {
		return nil
	}
	return failf(exitNotFine, "#%d: the head %s cannot be fetched from %s; not merging", number, short(commit, 10), remoteName)
}
