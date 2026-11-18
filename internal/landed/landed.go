// Package landed is `jst cards` (what landed for each card, read from history and the snapshots)
// and `jst lint range` (every commit of a range has valid `Card:` lines): docs/design/implementation.md,
// sections 4.1 and 4.4. The lines themselves
// are judged by internal/cards, the same rules the guard's push rule and `jst lint pr` use. Nothing
// here writes a hash into a file it keeps, and no card is edited: `--check` and `--stale` report.
package landed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cards"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/project"
	"github.com/zima-oss/jackstay/internal/series"
)

// gitTimeout bounds each question to git (a long history is read in one `git log`).
const gitTimeout = 5 * time.Minute

// Section is a project's `cards` section, read once.
type Section struct {
	Rules     cards.Rules
	IDText    string
	Unit      string
	Branch    string
	Since     string
	FirstNew  string
	Snapshots string
}

// SectionOf reads the `cards` section of a project.
func SectionOf(proj project.Project) (Section, error) {
	members := proj.Config.Section("cards")
	if members == nil {
		return Section{}, project.Fail("jackstay.json has no `cards` section: this project has not taken up the `Card:` line")
	}
	rules, err := cards.FromSection(members)
	if err != nil {
		return Section{}, project.Fail("%v", err)
	}
	text := func(key, otherwise string) string {
		if value, ok := members[key].(string); ok && value != "" {
			return value
		}
		return otherwise
	}
	return Section{Rules: rules, IDText: text("id", ""), Unit: text("unit", "pr"), Branch: text("branch", "main"),
		Since: text("since", ""), FirstNew: text("first_new", ""), Snapshots: strings.Trim(text("snapshots", ""), "/")}, nil
}

// IsNew says whether a card is numbered `first_new` or above, with the same prefix.
func (section Section) IsNew(card string) bool {
	if section.FirstNew == "" {
		return false
	}
	prefix, number, ok := series.NumberOf(card)
	firstPrefix, first, firstOK := series.NumberOf(section.FirstNew)
	return ok && firstOK && prefix == firstPrefix && number >= first
}

// commitIsNew says whether a commit dated date (ISO 8601) is on or after `since`.
func (section Section) commitIsNew(date string) bool {
	return section.Since != "" && len(date) >= 10 && date[:10] >= section.Since
}

// repo asks git about the project's repository.
type repo struct {
	git gitcli.Git
}

func (here repo) run(input []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	out, err := here.git.RunInput(ctx, input, args...)
	if err != nil {
		return "", project.Fail("%v", err)
	}
	return string(out), nil
}

// succeeds says whether git answers 0 (an ancestor, a ref that exists).
func (here repo) succeeds(args ...string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	_, err := here.git.Run(ctx, args...)
	return err == nil
}

// resolveBranch is the ref `cards.branch` is read from: the remote-tracking copy where it is at or
// ahead of the local branch (a worktree's local `main` is often behind), the local branch where that
// is ahead, the remote-tracking one where they went apart.
func (here repo) resolveBranch(branch string) (string, error) {
	listed, err := here.run(nil, "remote")
	if err != nil {
		return "", err
	}
	remotes := strings.Fields(listed)
	ordered := []string{}
	if slices.Contains(remotes, "origin") {
		ordered = append(ordered, "origin")
	}
	for _, remote := range remotes {
		if remote != "origin" {
			ordered = append(ordered, remote)
		}
	}
	tracking := ""
	for _, remote := range ordered {
		ref := "refs/remotes/" + remote + "/" + branch
		if here.succeeds("rev-parse", "--verify", "--quiet", ref+"^{commit}") {
			tracking = ref
			break
		}
	}
	local := "refs/heads/" + branch
	hasLocal := here.succeeds("rev-parse", "--verify", "--quiet", local+"^{commit}")
	switch {
	case tracking == "" && !hasLocal:
		return "", project.Fail("there is no branch %s here (cards.branch), local or fetched", branch)
	case tracking == "":
		return local, nil
	case !hasLocal:
		return tracking, nil
	case here.succeeds("merge-base", "--is-ancestor", tracking, local):
		return local, nil
	}
	return tracking, nil
}

// workTreePaths are the files of the checkout on disk: tracked and untracked, not ignored.
func (here repo) workTreePaths() ([]string, error) {
	listed, err := here.run(nil, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	unique := map[string]bool{}
	for _, name := range strings.Split(listed, "\x00") {
		if name != "" && isFile(here.git.Dir, name) {
			unique[name] = true
		}
	}
	paths := make([]string, 0, len(unique))
	for name := range unique {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	return paths, nil
}

// treePaths are the files of a commit or tree object.
func (here repo) treePaths(revision string) ([]string, error) {
	listed, err := here.run(nil, "ls-tree", "-r", "-z", "--name-only", "--full-tree", revision)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, name := range strings.Split(listed, "\x00") {
		if name != "" {
			paths = append(paths, name)
		}
	}
	return paths, nil
}

// Commit is one commit of history as `jst cards` and `jst lint range` read it.
type Commit struct {
	Hash, Author, Date, Subject, Message string
}

// log reads `git log` with the given selection; format names the date (%aI or %cI).
func (here repo) log(date string, selection ...string) ([]Commit, error) {
	args := append([]string{"log", "-z", "--format=%H%x1f%an%x1f" + date + "%x1f%s%x1f%B"}, selection...)
	out, err := here.run(nil, args...)
	if err != nil {
		return nil, err
	}
	var found []Commit
	for _, record := range strings.Split(out, "\x00") {
		record = strings.TrimLeft(record, "\n")
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\x1f", 5)
		if len(parts) != 5 {
			return nil, &project.Problem{Code: 5, Message: "git log gave a record of another shape"}
		}
		found = append(found, Commit{Hash: parts[0], Author: parts[1], Date: parts[2], Subject: parts[3], Message: parts[4]})
	}
	return found, nil
}

// Entry is one thing that landed for a card (kind "landed") or a subject that names it ("named").
type Entry struct {
	Card     string  `json:"card"`
	Kind     string  `json:"kind"`
	Date     string  `json:"date"`
	Author   string  `json:"author"`
	Subject  string  `json:"subject"`
	PR       *int    `json:"pr"`
	Hash     string  `json:"hash,omitempty"`
	HashThen *string `json:"hash_then,omitempty"`
	Source   string  `json:"source"`
}

// Disagreement is one finding of `jst cards --check`.
type Disagreement struct {
	Kind    string   `json:"kind"`
	Card    string   `json:"card,omitempty"`
	Hash    string   `json:"hash,omitempty"`
	Subject string   `json:"subject,omitempty"`
	Problem string   `json:"problem,omitempty"`
	State   any      `json:"state,omitempty"`
	Path    string   `json:"path,omitempty"`
	Where   []string `json:"where,omitempty"`
	ReadAs  string   `json:"read_as"`
}

var prNumber = regexp.MustCompile(`\(#([0-9]+)\)\s*$`)

func prOf(subject string) *int {
	match := prNumber.FindStringSubmatch(subject)
	if match == nil {
		return nil
	}
	number, err := strconv.Atoi(match[1])
	if err != nil {
		return nil
	}
	return &number
}

// history is (entries, disagreements) from the history of ref: a project that lands pull requests
// is read along the first parent, one that lands commits whole with merge commits left out.
func (here repo) history(section Section, ref string) ([]Entry, []Disagreement, error) {
	shape := "--first-parent"
	if section.Unit == "commit" {
		shape = "--no-merges"
	}
	commits, err := here.log("%aI", shape, ref)
	if err != nil {
		return nil, nil, err
	}
	var entries []Entry
	var found []Disagreement
	for _, commit := range commits {
		base := Entry{Date: commit.Date, Author: commit.Author, Subject: commit.Subject, PR: prOf(commit.Subject),
			Hash: commit.Hash, Source: "history"}
		lines := cards.Lines(commit.Message)
		switch {
		case section.commitIsNew(commit.Date):
			if len(lines) == 0 {
				found = append(found, Disagreement{Kind: "no-card-line", Hash: commit.Hash, Subject: commit.Subject,
					ReadAs: "something reached the branch past the check: a direct push, a merge made with another message"})
			}
			var named []string
			nones := 0
			for _, line := range lines {
				read, problems := section.Rules.Parse(line)
				if read.IsNone {
					nones++
				}
				for _, problem := range problems {
					found = append(found, Disagreement{Kind: "unreadable-card-line", Hash: commit.Hash, Subject: commit.Subject,
						Problem: problemText(problem), ReadAs: "a line the check did not see"})
				}
				for _, id := range read.IDs {
					named = append(named, id)
					entry := base
					entry.Card, entry.Kind = id, "landed"
					entries = append(entries, entry)
				}
			}
			if nones > 0 && len(named) > 0 {
				found = append(found, Disagreement{Kind: "unreadable-card-line", Hash: commit.Hash, Subject: commit.Subject,
					Problem: "`Card: none (…)` stands alone: this message also names " + strings.Join(named, ", "),
					ReadAs:  "a line the check did not see"})
			}
		case len(lines) > 0:
			for _, line := range lines {
				for _, id := range section.Rules.Anywhere(strings.TrimPrefix(line.Text, "Card: ")) {
					entry := base
					entry.Card, entry.Kind = id, "landed"
					entries = append(entries, entry)
				}
			}
		default:
			for _, id := range section.Rules.Anywhere(commit.Subject) {
				entry := base
				entry.Card, entry.Kind = id, "named"
				entries = append(entries, entry)
			}
		}
	}
	// One commit naming a card twice is one entry.
	seen := map[[3]string]bool{}
	unique := entries[:0]
	for _, entry := range entries {
		key := [3]string{entry.Card, entry.Kind, entry.Hash}
		if !seen[key] {
			seen[key] = true
			unique = append(unique, entry)
		}
	}
	return unique, found, nil
}

// problemText is a finding of internal/cards as a sentence.
func problemText(finding cards.Finding) string {
	switch finding.Rule {
	case cards.NoLine:
		return "no `Card:` line: say which card this is work for, or `Card: none (why)`"
	case cards.NoReason:
		if finding.Line == 0 {
			return finding.Text
		}
		return fmt.Sprintf("`%s`: `none` needs a reason in brackets, and stands alone: `Card: none (why)`", finding.Text)
	case cards.Unknown:
		return fmt.Sprintf("`%s` has no card file", finding.ID)
	}
	return fmt.Sprintf("`%s` does not keep the form `Card: ID[, ID…]` (an ID or its card's path in the cards folder)", finding.Text)
}

// sameEntry says whether later (from history) stands for earlier (from a snapshot): the card and
// the pull-request number; without a number, the card, author's date, author and subject. A
// commit that only names the card does not stand for one that carried its line.
func sameEntry(earlier, later Entry) bool {
	if earlier.Card != later.Card {
		return false
	}
	if earlier.Kind == "landed" && later.Kind != "landed" {
		return false
	}
	if earlier.PR != nil || later.PR != nil {
		return earlier.PR != nil && later.PR != nil && *earlier.PR == *later.PR
	}
	return earlier.Date == later.Date && earlier.Author == later.Author && earlier.Subject == later.Subject
}

func report(name string, err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	var failure *project.Problem
	if errors.As(err, &failure) {
		if failure.Message != "" {
			fmt.Fprintf(stderr, "jst %s: %s\n", name, failure.Message)
		}
		return failure.Code
	}
	fmt.Fprintf(stderr, "jst %s: %v\n", name, err)
	return 2
}

func gitDir(root string) gitcli.Git { return gitcli.Git{Dir: root} }
