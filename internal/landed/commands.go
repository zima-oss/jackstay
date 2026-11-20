package landed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/project"
	"github.com/zima-oss/jackstay/internal/series"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

const cardsUsage = "usage: jst cards [ID...] [--json] [--ref REF] | --check [--json] | --stale [--commit-map FILE] | " +
	"snapshot [--write] | compare FILE"

const rangeUsage = "usage: jst lint range A..B"

func init() {
	cli.Register(
		cli.Command{Name: "cards", Summary: "what landed per card, read from history", Usage: cardsHelp, Run: runCards},
		cli.Command{Name: "lint range", Summary: "every commit in A..B has valid Card lines", Usage: rangeHelp, Run: runRange},
	)
}

// snapshotName is a snapshot file's name.
var snapshotName = regexp.MustCompile(`^landed-[0-9]{4}-[0-9]{2}-[0-9]{2}(?:-[0-9]+)?\.json$`)

// hashText is a commit's hash, whole or cut short.
var hashText = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func isFile(root, name string) bool { return project.Repo{Root: root}.IsFile(name) }

// readFile reads a file of the checkout through project.Repo (inside the repository, or an error).
func readFile(root, name string) (string, error) {
	data, err := project.Repo{Root: root}.ReadFile(name)
	return string(data), err
}

// cardState is what a card file says: its state, its `commits` and its path.
type cardState struct {
	state   any
	commits any
	path    string
}

// states read every card file's front matter; one that cannot be read is a Problem.
func states(root string, files map[string][]string) (map[string]cardState, error) {
	found := map[string]cardState{}
	for card, paths := range files {
		text, err := readFile(root, paths[0])
		if err != nil {
			return nil, project.Fail("%s: %v", paths[0], err)
		}
		matter, err := series.ReadFrontMatter(text)
		if err != nil {
			return nil, project.Fail("%s: %v", paths[0], err)
		}
		state, _ := matter.Get("state")
		written, _ := matter.Get("commits")
		found[card] = cardState{state: state, commits: written, path: paths[0]}
	}
	return found, nil
}

// options of `jst cards`.
type cardsOptions struct {
	json, check, stale, write bool
	ref, commitMap            string
	words                     []string
}

func parseCards(args []string) (cardsOptions, error) {
	var found cardsOptions
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--json":
			found.json = true
		case "--check":
			found.check = true
		case "--stale":
			found.stale = true
		case "--write":
			found.write = true
		case "--ref", "--commit-map":
			if index+1 >= len(args) {
				return found, project.Fail(cardsUsage)
			}
			if arg == "--ref" {
				found.ref = args[index+1]
			} else {
				found.commitMap = args[index+1]
			}
			index++
		default:
			if strings.HasPrefix(arg, "-") {
				return found, project.Fail(cardsUsage)
			}
			found.words = append(found.words, arg)
		}
	}
	return found, nil
}

func runCards(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	return report("cards", cardsCommand(args, stdout), stderr)
}

func cardsCommand(args []string, stdout io.Writer) error {
	given, err := parseCards(args)
	if err != nil {
		return err
	}
	proj, err := project.Here([]string{"cards"})
	if err != nil {
		return project.Fail("%v", err)
	}
	section, err := SectionOf(proj)
	if err != nil {
		return err
	}
	here := repo{git: gitcli.Git{Dir: proj.Root}}
	ref := given.ref
	if ref == "" {
		if ref, err = here.resolveBranch(section.Branch); err != nil {
			return err
		}
	}
	paths, err := here.workTreePaths()
	if err != nil {
		return err
	}
	files := section.Rules.Files(paths)
	if len(files) == 0 && !(project.Repo{Root: proj.Root}).IsDir(section.Rules.Dir) {
		return project.Fail("there is no folder %s (cards.dir) in %s", section.Rules.Dir, proj.Root)
	}
	switch {
	case len(given.words) > 0 && given.words[0] == "snapshot":
		if len(given.words) != 1 {
			return project.Fail(cardsUsage)
		}
		return snapshotCommand(proj, here, section, ref, given.write, stdout)
	case len(given.words) > 0 && given.words[0] == "compare":
		if len(given.words) != 2 {
			return project.Fail(cardsUsage)
		}
		return compareCommand(here, section, ref, given.words[1], stdout)
	}
	if len(given.words) > 0 {
		if given.check || given.stale {
			return project.Fail(cardsUsage)
		}
		for _, word := range given.words {
			if !section.Rules.ID.MatchString(word) {
				return project.Fail("%s is not a card ID of this project (they match %s)", word, section.IDText)
			}
		}
	}
	read, err := states(proj.Root, files)
	if err != nil {
		return err
	}
	if given.stale {
		return staleCommand(here, read, ref, given.commitMap, stdout)
	}
	entries, disagreements, err := here.history(section, ref)
	if err != nil {
		return err
	}
	for _, name := range paths {
		if section.Snapshots == "" || path.Dir(name) != section.Snapshots || !snapshotName.MatchString(path.Base(name)) {
			continue
		}
		text, err := readFile(proj.Root, name)
		if err != nil {
			return project.Fail("%s: %v", name, err)
		}
		earlier, _, err := readSnapshot(text, name)
		if err != nil {
			return err
		}
		for _, entry := range earlier {
			if !slices.ContainsFunc(entries, func(later Entry) bool { return sameEntry(entry, later) }) {
				entries = append(entries, entry)
			}
		}
	}
	if given.check {
		found := check(section, read, entries, disagreements)
		if given.json {
			if err := writeJSON(stdout, found); err != nil {
				return err
			}
		} else {
			for _, item := range found {
				what := item.Card
				if what == "" {
					what = short(item.Hash) + " " + item.Subject
				}
				problem := ""
				if item.Problem != "" {
					problem = " — " + item.Problem
				}
				fmt.Fprintf(stdout, "%s: %s%s (%s)\n", item.Kind, what, problem, item.ReadAs)
			}
			if len(found) == 0 {
				fmt.Fprintf(stdout, "no disagreement (read from %s)\n", ref)
			} else {
				fmt.Fprintf(stdout, "%d disagreement(s) (read from %s)\n", len(found), ref)
			}
		}
		if len(found) > 0 {
			return &project.Problem{Code: 1}
		}
		return nil
	}
	return show(section, read, entries, given, stdout)
}

func short(hash string) string { return hash[:min(10, len(hash))] }

// check is `--check`: the commit-level disagreements of history, then the card-level ones.
func check(section Section, read map[string]cardState, entries []Entry, disagreements []Disagreement) []Disagreement {
	found := append([]Disagreement{}, disagreements...)
	landedFor := map[string][]Entry{}
	for _, entry := range entries {
		if entry.Kind == "landed" {
			landedFor[entry.Card] = append(landedFor[entry.Card], entry)
		}
	}
	for _, card := range sortedKeys(landedFor) {
		if _, ok := read[card]; ok {
			continue
		}
		var where []string
		for _, entry := range landedFor[card] {
			if entry.Hash != "" {
				where = append(where, entry.Hash)
			} else if entry.HashThen != nil {
				where = append(where, *entry.HashThen)
			}
		}
		found = append(found, Disagreement{Kind: "no-card-file", Card: card, Where: where, ReadAs: "a typo that the check did not see"})
	}
	for _, card := range sortedKeys(read) {
		state := read[card]
		closed := state.state == "done" || state.state == "verified"
		_, hasLanded := landedFor[card]
		if section.IsNew(card) && closed && !hasLanded {
			found = append(found, Disagreement{Kind: "closed-with-nothing-landed", Card: card, State: state.state,
				ReadAs: "closed without a change, or the change carries another card's line"})
		}
		if state.state == "ready" && hasLanded {
			found = append(found, Disagreement{Kind: "ready-with-work-landed", Card: card,
				ReadAs: "work on a card nobody opened; or the state was not moved"})
		}
		if section.IsNew(card) && truthy(state.commits) {
			found = append(found, Disagreement{Kind: "hashes-in-a-new-card", Card: card, Path: state.path, ReadAs: "the old habit"})
		}
	}
	return found
}

// truthy is a JSON value that says something: not null, false, 0, "" or empty.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case json.Number:
		number, err := typed.Float64()
		return err != nil || number != 0
	}
	return true
}

func sortedKeys[Value any](members map[string]Value) []string {
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// cardView is one card as `jst cards --json` shows it.
type cardView struct {
	Card   string  `json:"card"`
	State  any     `json:"state"`
	File   *string `json:"file"`
	Landed []Entry `json:"landed"`
	Named  []Entry `json:"named_in_the_subject"`
}

func show(section Section, read map[string]cardState, entries []Entry, given cardsOptions, stdout io.Writer) error {
	chosen := given.words
	if len(chosen) == 0 {
		unique := map[string]bool{}
		for card := range read {
			unique[card] = true
		}
		for _, entry := range entries {
			if entry.Kind == "landed" {
				unique[entry.Card] = true
			}
		}
		chosen = sortedKeys(unique)
	}
	views := make([]cardView, 0, len(chosen))
	for _, card := range chosen {
		view := cardView{Card: card, Landed: []Entry{}, Named: []Entry{}}
		if state, ok := read[card]; ok {
			view.State = state.state
			view.File = &state.path
		}
		for _, entry := range entries {
			if entry.Card != card {
				continue
			}
			if entry.Kind == "landed" {
				view.Landed = append(view.Landed, entry)
			} else {
				view.Named = append(view.Named, entry)
			}
		}
		views = append(views, view)
	}
	if given.json {
		return writeJSON(stdout, views)
	}
	_ = section
	for _, view := range views {
		state := "no card file"
		if view.File != nil {
			state = fmt.Sprint(view.State)
			if view.State == nil {
				state = "no state"
			}
		}
		named := ""
		if len(view.Named) > 0 {
			named = fmt.Sprintf(", named in %d subjects", len(view.Named))
		}
		fmt.Fprintf(stdout, "%s  %s  %d landed%s\n", view.Card, state, len(view.Landed), named)
		for _, entry := range append(append([]Entry{}, view.Landed...), view.Named...) {
			fmt.Fprintln(stdout, entryText(entry))
		}
	}
	return nil
}

func entryText(entry Entry) string {
	where := short(entry.Hash)
	if entry.Source != "history" {
		then := "?"
		if entry.HashThen != nil {
			then = short(*entry.HashThen)
		}
		where = "was " + then + ", from " + entry.Source
	}
	pullRequest := ""
	if entry.PR != nil && !strings.Contains(entry.Subject, fmt.Sprintf("(#%d)", *entry.PR)) {
		pullRequest = fmt.Sprintf(" (#%d)", *entry.PR)
	}
	date := entry.Date
	if len(date) > 10 {
		date = date[:10]
	}
	return fmt.Sprintf("  %-6s %s %s  %s%s", entry.Kind, date, where, entry.Subject, pullRequest)
}

// staleCommand is `--stale`: each card's `commits` entries that are not on the branch, with the
// new hash a commit map gives. No card is edited.
func staleCommand(here repo, read map[string]cardState, ref, commitMap string, stdout io.Writer) error {
	mapping := map[string]string{}
	var order []string
	if commitMap != "" {
		data, err := os.ReadFile(commitMap) // #nosec G304 -- the file the caller names
		if err != nil {
			return project.Fail("cannot read the commit map %s: %v", commitMap, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			parts := strings.Fields(line)
			if len(parts) == 2 && hashText.MatchString(parts[0]) && hashText.MatchString(parts[1]) {
				if _, seen := mapping[parts[0]]; !seen {
					order = append(order, parts[0])
				}
				mapping[parts[0]] = parts[1]
			}
		}
	}
	count := 0
	for _, card := range sortedKeys(read) {
		written, _ := read[card].commits.([]any)
		for _, item := range written {
			text := strings.TrimSpace(fmt.Sprint(item))
			if encoded, isText := item.(string); isText {
				text = strings.TrimSpace(encoded)
			}
			first, _, _ := strings.Cut(text, "..")
			first = strings.TrimSpace(first)
			if !hashText.MatchString(first) {
				fmt.Fprintf(stdout, "%s\t%s\tnot a hash\n", card, text)
				count++
				continue
			}
			if here.succeeds("merge-base", "--is-ancestor", first, ref) {
				continue
			}
			line := fmt.Sprintf("%s\t%s\tnot on %s", card, text, ref)
			for _, old := range order {
				if strings.HasPrefix(old, first) {
					line += "\tnow " + mapping[old]
					break
				}
			}
			fmt.Fprintln(stdout, line)
			count++
		}
	}
	if count > 0 {
		fmt.Fprintf(stdout, "%d entries in cards' `commits` are not on %s\n", count, ref)
	} else {
		fmt.Fprintf(stdout, "every hash in cards' `commits` is on %s\n", ref)
	}
	return nil
}

// Snapshot is the file `jst cards snapshot --write` keeps (docs/design/implementation.md section 4.4); snapshots are
// committed in the projects.
type Snapshot struct {
	Format   string          `json:"format"`
	Version  int             `json:"version"`
	Written  string          `json:"written"`
	Branch   string          `json:"branch"`
	HeadThen string          `json:"head_then"`
	Note     string          `json:"note"`
	Entries  []SnapshotEntry `json:"entries"`
}

// SnapshotEntry is one entry as a snapshot keeps it, its hash as it was.
type SnapshotEntry struct {
	Card       string `json:"card"`
	Kind       string `json:"kind"`
	AuthorDate string `json:"author_date"`
	Author     string `json:"author"`
	Subject    string `json:"subject"`
	PR         *int   `json:"pr"`
	HashThen   string `json:"hash_then"`
}

// readSnapshot reads a snapshot's entries (and the head it was written at), strictly: one JSON
// document, no member twice, nothing after it; anything else is a Problem.
func readSnapshot(text, name string) ([]Entry, string, error) {
	var value struct {
		Format   any              `json:"format"`
		HeadThen any              `json:"head_then"`
		Entries  []map[string]any `json:"entries"`
	}
	var shape map[string]any
	if err := strictjson.UnmarshalOpen([]byte(text), &shape); err != nil {
		return nil, "", project.Fail("%s is not JSON: %v", name, err)
	}
	if _, isList := shape["entries"].([]any); !isList || shape["format"] != "jackstay-cards-snapshot" {
		return nil, "", project.Fail("%s is not a snapshot `jst cards snapshot` wrote", name)
	}
	if err := strictjson.UnmarshalOpen([]byte(text), &value); err != nil {
		return nil, "", project.Fail("%s holds an entry of another shape: %v", name, err)
	}
	headThen, _ := value.HeadThen.(string)
	var found []Entry
	for _, members := range value.Entries {
		card, okCard := members["card"].(string)
		kind, _ := members["kind"].(string)
		if !okCard || (kind != "landed" && kind != "named") {
			shown, _ := json.Marshal(members)
			return nil, "", project.Fail("%s holds an entry of another shape: %.200s", name, shown)
		}
		entry := Entry{Card: card, Kind: kind, Source: "snapshot " + name}
		entry.Date, _ = members["author_date"].(string)
		entry.Author, _ = members["author"].(string)
		entry.Subject, _ = members["subject"].(string)
		if number, ok := members["pr"].(float64); ok && number == math.Trunc(number) && math.Abs(number) < 1e15 {
			pullRequest := int(number)
			entry.PR = &pullRequest
		}
		if then, ok := members["hash_then"].(string); ok {
			entry.HashThen = &then
		}
		found = append(found, entry)
	}
	return found, headThen, nil
}

func snapshotCommand(proj project.Project, here repo, section Section, ref string, write bool, stdout io.Writer) error {
	entries, _, err := here.history(section, ref)
	if err != nil {
		return err
	}
	head, err := here.run(nil, "rev-parse", ref)
	if err != nil {
		return err
	}
	value := Snapshot{Format: "jackstay-cards-snapshot", Version: 1, Written: time.Now().Format("2006-01-02"), Branch: section.Branch,
		HeadThen: strings.TrimSpace(head),
		Note:     "Written once by `jst cards snapshot --write` and never edited; hashes are as they were then.",
		Entries:  []SnapshotEntry{}}
	for _, entry := range entries {
		value.Entries = append(value.Entries, SnapshotEntry{Card: entry.Card, Kind: entry.Kind, AuthorDate: entry.Date,
			Author: entry.Author, Subject: entry.Subject, PR: entry.PR, HashThen: entry.Hash})
	}
	var text bytes.Buffer
	encoder := json.NewEncoder(&text)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", " ")
	if err := encoder.Encode(value); err != nil {
		return project.Fail("%v", err)
	}
	if !write {
		_, err := stdout.Write(text.Bytes())
		return err
	}
	if section.Snapshots == "" {
		return project.Fail("jackstay.json has no `cards.snapshots` folder to write into")
	}
	repo := project.Repo{Root: proj.Root}
	if err := repo.MkdirAll(section.Snapshots); err != nil {
		return project.Fail("the snapshot cannot be written in %s (%v)", section.Snapshots, err)
	}
	for number := 1; number < 1000; number++ {
		name := "landed-" + value.Written + ".json"
		if number > 1 {
			name = fmt.Sprintf("landed-%s-%d.json", value.Written, number)
		}
		err := repo.Create(path.Join(section.Snapshots, name), text.Bytes())
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return project.Fail("the snapshot cannot be written in %s (%v)", section.Snapshots, err)
		}
		fmt.Fprintf(stdout, "wrote %s (%d entries, %s of %s); commit it before the rewrite\n",
			path.Join(section.Snapshots, name), len(value.Entries), short(value.HeadThen), ref)
		return nil
	}
	return project.Fail("no free snapshot name in %s for %s", section.Snapshots, value.Written)
}

// compareCommand is `compare FILE`: which of the snapshot's entries history no longer has, and
// whether commits landed after it was written (they are in no snapshot). Exit 1 for either.
func compareCommand(here repo, section Section, ref, file string, stdout io.Writer) error {
	data, err := os.ReadFile(file) // #nosec G304 -- the snapshot the caller names
	if err != nil {
		return project.Fail("cannot read %s: %v", file, err)
	}
	earlier, headThen, err := readSnapshot(string(data), file)
	if err != nil {
		return err
	}
	now, _, err := here.history(section, ref)
	if err != nil {
		return err
	}
	later := "0"
	if headThen != "" && here.succeeds("merge-base", "--is-ancestor", headThen, ref) {
		// Commits that only add snapshot files (the commit of this one) bring nothing new.
		pathspec := "."
		if section.Snapshots != "" {
			pathspec = ":(exclude,glob)" + globQuote(section.Snapshots) + "/landed-*.json"
		}
		counted, err := here.run(nil, "rev-list", "--count", headThen+".."+ref, "--", ".", pathspec)
		if err != nil {
			return err
		}
		later = strings.TrimSpace(counted)
	}
	if later != "0" {
		fmt.Fprintf(stdout, "%s commits landed on %s after %s was written (%s): they are in no snapshot. "+
			"Write a new one (`jst cards snapshot --write`) and commit it before a rewrite\n", later, ref, file, short(headThen))
	}
	lost := 0
	for _, entry := range earlier {
		if slices.ContainsFunc(now, func(current Entry) bool { return sameEntry(entry, current) }) {
			continue
		}
		lost++
		pullRequest := ""
		if entry.PR != nil {
			pullRequest = fmt.Sprintf(" (#%d)", *entry.PR)
		}
		then := ""
		if entry.HashThen != nil {
			then = short(*entry.HashThen)
		}
		date := entry.Date
		if len(date) > 10 {
			date = date[:10]
		}
		fmt.Fprintf(stdout, "lost: %s %s %s %s: %s%s, was %s\n", entry.Card, entry.Kind, date, entry.Author, entry.Subject, pullRequest, then)
	}
	if lost > 0 {
		fmt.Fprintf(stdout, "%d of %d entries of %s are not in the history of %s\n", lost, len(earlier), file, ref)
	} else {
		fmt.Fprintf(stdout, "every one of the %d entries of %s is in the history of %s\n", len(earlier), file, ref)
	}
	if lost > 0 || later != "0" {
		return &project.Problem{Code: 1}
	}
	return nil
}

// globQuote escapes the characters a glob pathspec reads.
func globQuote(text string) string {
	return regexp.MustCompile(`([*?\[\]\\])`).ReplaceAllString(text, `\$1`)
}

func writeJSON(stdout io.Writer, value any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func runRange(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	return report("lint", rangeCommand(args, stdout), stderr)
}

// rangeCommand is `jst lint range A..B`: every commit `git log A..B` gives whose committer date is
// on or after `cards.since` (all without one) has valid `Card:` lines, judged against the card
// files of B's tree. The committer date, so an old author date does not exempt a commit pushed now.
func rangeCommand(args []string, stdout io.Writer) error {
	if len(args) != 1 || !strings.Contains(args[0], "..") || strings.HasPrefix(args[0], "-") {
		return project.Fail(rangeUsage)
	}
	proj, err := project.Here([]string{"cards"})
	if err != nil {
		return project.Fail("%v", err)
	}
	section, err := SectionOf(proj)
	if err != nil {
		return err
	}
	here := repo{git: gitcli.Git{Dir: proj.Root}}
	_, tip, _ := strings.Cut(args[0], "..")
	tip = strings.TrimPrefix(tip, ".")
	if tip == "" {
		tip = "HEAD"
	}
	paths, err := here.treePaths(tip)
	if err != nil {
		return err
	}
	if section.Rules.Dir != "." && !slices.ContainsFunc(paths, func(name string) bool { return strings.HasPrefix(name, section.Rules.Dir+"/") }) {
		return project.Fail("there are no card files in %s (cards.dir) in %s: nothing is guessed", section.Rules.Dir, tip)
	}
	known := section.Rules.Known(paths)
	commits, err := here.log("%cI", args[0])
	if err != nil {
		return err
	}
	broken := 0
	for _, commit := range commits {
		if section.Since != "" && !section.commitIsNew(commit.Date) {
			continue
		}
		problems := section.Rules.Check(commit.Message, known)
		if len(problems) == 0 {
			continue
		}
		broken++
		fmt.Fprintf(stdout, "%s %s\n", short(commit.Hash), commit.Subject)
		for _, problem := range problems {
			fmt.Fprintf(stdout, "  - [%s] %s\n", problem.Rule, problemText(problem))
		}
	}
	if broken > 0 {
		fmt.Fprintf(stdout, "jst lint: %s: %d commit(s) without valid Card lines\n", args[0], broken)
		return &project.Problem{Code: 1}
	}
	fmt.Fprintf(stdout, "jst lint: %s: every commit has valid Card lines\n", args[0])
	return nil
}
