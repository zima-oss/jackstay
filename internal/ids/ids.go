// Package ids is `jst id`: one counter of numbers per project on this machine (docs/design/implementation.md,
// section 4.2). Under one lock per project, `next` takes the highest number it
// can see and adds one: its own record, every worktree on disk, the tree of every local branch and
// remote-tracking ref, and the `also_in` files of the main checkout. A number it cannot record is
// never printed, and a partial view hands out nothing (exit 2).
//
// The lock and the record are `~/.jackstay/<project>/ids/lock` (flock) and `<SERIES>.jsonl`, one
// line per number handed out.
package ids

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/project"
	"github.com/zima-oss/jackstay/internal/series"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

const usage = "usage: jst id next SERIES [--count N] [--json] | jst id list [SERIES] [--json] | jst id peek SERIES"

// gitTimeout bounds each question to git.
const gitTimeout = 60 * time.Second

func init() {
	cli.Register(cli.Command{Name: "id", Summary: "hand out numbers from one counter per project on this machine", Usage: help, Run: run})
}

// Series is one entry of jackstay.json's `ids`.
type Series struct {
	Name   string
	Files  string
	Width  int
	AlsoIn []string
}

// SeriesOf is the `ids` entry of a series, or a Problem naming the series there are.
func SeriesOf(proj project.Project, name string) (Series, error) {
	section := proj.Config.Section("ids")
	entry, ok := section[name].(map[string]any)
	if !ok {
		known := make([]string, 0, len(section))
		for key := range section {
			known = append(known, key)
		}
		sort.Strings(known)
		listed := strings.Join(known, ", ")
		if listed == "" {
			listed = "none"
		}
		return Series{}, project.Fail("jackstay.json has no `ids.%s` (the series here: %s)", name, listed)
	}
	found := Series{Name: name, Width: 1}
	found.Files, _ = entry["files"].(string)
	if width, ok := entry["width"].(json.Number); ok {
		if parsed, err := strconv.Atoi(width.String()); err == nil {
			found.Width = parsed
		}
	}
	for _, item := range anyList(entry["also_in"]) {
		if text, ok := item.(string); ok {
			found.AlsoIn = append(found.AlsoIn, text)
		}
	}
	return found, nil
}

func anyList(value any) []any { list, _ := value.([]any); return list }

// Record is one line of the counter's record.
type Record struct {
	Series   string  `json:"series"`
	Number   int     `json:"number"`
	ID       string  `json:"id"`
	At       string  `json:"at"`
	Epoch    float64 `json:"epoch"`
	Worktree string  `json:"worktree"`
	Session  *string `json:"session"`
	PID      int     `json:"pid"`
}

func recordPath(proj project.Project, name string) (string, error) {
	return homedir.ProjectPath(proj.Name, "ids", name+".jsonl")
}

// Recorded is every number handed out, from the counter's own record. A record that exists and
// cannot be read, or holds a line that is not one of its records, is a Problem: a number could
// be handed out twice.
func Recorded(proj project.Project, name string) ([]Record, error) {
	place, err := recordPath(proj, name)
	if err != nil {
		return nil, project.Fail("%v", err)
	}
	data, err := os.ReadFile(place) // #nosec G304 -- the counter's own record under jackstay's home
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, project.Fail("the counter's record %s cannot be read (%v); no number was handed out", place, err)
	}
	var found []Record
	for index, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record, ok := readRecord(line)
		if !ok {
			return nil, project.Fail("the counter's record %s has a line %d that is not one of its records; "+
				"look at it (and move it aside if it is damaged): no number was handed out", place, index+1)
		}
		found = append(found, record)
	}
	return found, nil
}

// readRecord reads one line: an object whose `number` is an integer.
func readRecord(line string) (Record, bool) {
	members, err := strictjson.Members([]byte(line))
	if err != nil {
		return Record{}, false
	}
	record, numbered := Record{}, false
	for _, member := range members {
		var text string
		isText := json.Unmarshal(member.Value, &text) == nil && member.Value.Kind() == '"'
		switch member.Name {
		case "number":
			parsed, err := strictjson.Integer(member.Value)
			if err != nil || parsed < 0 || parsed > series.MaxNumber {
				return Record{}, false
			}
			record.Number, numbered = parsed, true
		case "series":
			record.Series = text
		case "id":
			record.ID = text
		case "at":
			record.At = text
		case "worktree":
			record.Worktree = text
		case "session":
			if isText {
				session := text
				record.Session = &session
			}
		}
	}
	return record, numbered
}

// Seen is where each number of a series was seen: "handed out", "file", "also_in".
type Seen map[int]map[string]bool

// addPaths adds the numbers the paths hold as files; a number too large to count is an error.
func (seen Seen) addPaths(paths []string, taken *regexp.Regexp) error {
	numbers, err := series.Numbers(paths, taken)
	if err != nil {
		return project.Fail("%v", err)
	}
	for number := range numbers {
		seen.add(number, "file")
	}
	return nil
}

func (seen Seen) add(number int, where string) {
	if seen[number] == nil {
		seen[number] = map[string]bool{}
	}
	seen[number][where] = true
}

// Highest is the highest number of the series anyone can see from this machine (0 for none), and
// where each was seen.
func Highest(ctx context.Context, proj project.Project, entry Series) (int, Seen, error) {
	taken, err := series.Taken(entry.Files)
	if err != nil {
		return 0, nil, project.Fail("ids.%s.files: %v", entry.Name, err)
	}
	seen := Seen{}
	records, err := Recorded(proj, entry.Name)
	if err != nil {
		return 0, nil, err
	}
	for _, record := range records {
		seen.add(record.Number, "handed out")
	}
	git := gitcli.Git{Dir: proj.Root}
	folders, err := worktrees(ctx, git)
	if err != nil {
		return 0, nil, err
	}
	for _, folder := range folders {
		paths, err := onDisk(folder, entry.Files, taken)
		if err != nil {
			return 0, nil, err
		}
		if err := seen.addPaths(paths, taken); err != nil {
			return 0, nil, err
		}
	}
	paths, err := inRefs(ctx, git, entry.Files, taken)
	if err != nil {
		return 0, nil, err
	}
	if err := seen.addPaths(paths, taken); err != nil {
		return 0, nil, err
	}
	if len(entry.AlsoIn) > 0 {
		main := proj.Root
		if len(folders) > 0 {
			main = folders[0]
		}
		for _, name := range entry.AlsoIn {
			data, err := project.Repo{Root: main}.ReadFile(name)
			if errors.Is(err, fs.ErrNotExist) {
				return 0, nil, project.Fail("`also_in` names %s, which is not in %s", name, main)
			}
			if err != nil {
				return 0, nil, project.Fail("cannot read %s: %v", filepath.Join(main, name), err)
			}
			mentioned, err := series.Mentions(entry.Name, string(data))
			if err != nil {
				return 0, nil, project.Fail("%s: %v", name, err)
			}
			for _, number := range mentioned {
				seen.add(number, "also_in")
			}
		}
	}
	top := 0
	for number := range seen {
		top = max(top, number)
	}
	return top, seen, nil
}

// worktrees are the folders of every worktree of the repository, the main checkout first; each
// must be there to be looked into.
func worktrees(ctx context.Context, git gitcli.Git) ([]string, error) {
	out, err := ask(ctx, git, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var folders []string
	for _, line := range strings.Split(string(out), "\n") {
		if folder, ok := strings.CutPrefix(line, "worktree "); ok {
			if info, err := os.Stat(folder); err != nil || !info.IsDir() {
				return nil, project.Fail("the worktree %s is listed by git and cannot be looked into (a disk not mounted?); "+
					"a number in it could be handed out again. Mount it, or `git worktree prune` if it is gone", folder)
			}
			folders = append(folders, folder)
		}
	}
	return folders, nil
}

func ask(ctx context.Context, git gitcli.Git, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	out, err := git.RunInput(ctx, input, args...)
	if err != nil {
		return nil, project.Fail("%v", err)
	}
	return out, nil
}

// onDisk are the paths in one worktree's folder that hold a number, committed or not.
func onDisk(folder, pattern string, taken interface{ MatchString(string) bool }) ([]string, error) {
	sub := series.Folder(pattern)
	entries, err := project.Repo{Root: folder}.ReadDir(sub)
	if err != nil {
		return nil, project.Fail("cannot list %s in %s: %v", sub, folder, err)
	}
	var found []string
	for _, item := range entries {
		candidate := item.Name()
		if sub != "" {
			candidate = sub + "/" + candidate
		}
		if taken.MatchString(candidate) {
			found = append(found, candidate)
		}
	}
	return found, nil
}

// inRefs are the paths that hold a number in the tree of every local branch and remote-tracking
// ref. A ref whose tree git cannot read refuses: a number there could be handed out again.
func inRefs(ctx context.Context, git gitcli.Git, pattern string, taken interface{ MatchString(string) bool }) ([]string, error) {
	sub := series.Folder(pattern)
	out, err := ask(ctx, git, nil, "for-each-ref", "--format=%(objectname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	unique := map[string]bool{}
	for _, object := range strings.Fields(string(out)) {
		unique[object] = true
	}
	commits := make([]string, 0, len(unique))
	for commit := range unique {
		commits = append(commits, commit)
	}
	sort.Strings(commits)
	if len(commits) == 0 {
		return nil, nil
	}
	var question bytes.Buffer
	for _, commit := range commits {
		fmt.Fprintf(&question, "%s^{tree}\n", commit)
	}
	answers, err := ask(ctx, git, question.Bytes(), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	for index, line := range strings.Split(strings.TrimRight(string(answers), "\n"), "\n") {
		if fields := strings.Fields(line); len(fields) != 3 || index >= len(commits) {
			short := line
			if index < len(commits) {
				short = commits[index][:min(10, len(commits[index]))]
			}
			return nil, project.Fail("git cannot read the tree of %s (a ref points to it): a number there could be handed out again", short)
		}
	}
	question.Reset()
	for _, commit := range commits {
		if sub == "" {
			fmt.Fprintf(&question, "%s^{tree}\n", commit)
		} else {
			fmt.Fprintf(&question, "%s:%s\n", commit, sub)
		}
	}
	answers, err = ask(ctx, git, question.Bytes(), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	trees := map[string]bool{}
	for _, line := range strings.Split(strings.TrimRight(string(answers), "\n"), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 3 && fields[1] == "tree":
			trees[fields[0]] = true
		case len(fields) == 3:
			// A file of that name: no folder, no numbers.
		case len(fields) >= 1 && fields[len(fields)-1] == "missing":
			// No such folder in that ref.
		default:
			return nil, project.Fail("git could not read a ref's tree: %s", line)
		}
	}
	treeIDs := make([]string, 0, len(trees))
	for tree := range trees {
		treeIDs = append(treeIDs, tree)
	}
	sort.Strings(treeIDs)
	var found []string
	for _, tree := range treeIDs {
		listing, err := ask(ctx, git, nil, "ls-tree", "-z", "--name-only", tree)
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Split(string(listing), "\x00") {
			candidate := name
			if sub != "" {
				candidate = sub + "/" + name
			}
			if name != "" && taken.MatchString(candidate) {
				found = append(found, candidate)
			}
		}
	}
	return found, nil
}

// Take hands out count numbers of a series: each recorded first, all returned after. A number
// that cannot be recorded is not handed out (a Problem naming those recorded before it, gaps).
func Take(ctx context.Context, proj project.Project, name string, count int) ([]string, error) {
	entry, err := SeriesOf(proj, name)
	if err != nil {
		return nil, err
	}
	folder, err := homedir.ProjectPath(proj.Name, "ids")
	if err != nil {
		return nil, project.Fail("%v", err)
	}
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return nil, project.Fail("the counter's folder %s cannot be written (%v); no number was handed out", folder, err)
	}
	lock, err := os.OpenFile(filepath.Join(folder, "lock"), os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- jackstay's own lock
	if err != nil {
		return nil, project.Fail("the counter's folder %s cannot be written (%v); no number was handed out", folder, err)
	}
	defer func() { _ = lock.Close() }()
	if err := flock(lock); err != nil {
		return nil, project.Fail("the counter's lock cannot be taken (%v); no number was handed out", err)
	}
	top, _, err := Highest(ctx, proj, entry)
	if err != nil {
		return nil, err
	}
	place, err := recordPath(proj, name)
	if err != nil {
		return nil, project.Fail("%v", err)
	}
	var session *string
	if own := procs.OwnSession(procs.Table(), os.Getpid()); own.Session != "" {
		session = &own.Session
	}
	var handed []string
	if top > series.MaxNumber-count {
		return nil, project.Fail("ids.%s: the next number would be above %d, which jackstay does not count; no number was handed out", name, series.MaxNumber)
	}
	for number := top + 1; number <= top+count; number++ {
		identifier := series.Format(name, entry.Width, number)
		record := Record{Series: name, Number: number, ID: identifier, At: homedir.NowText(), Epoch: homedir.Epoch(),
			Worktree: proj.Root, Session: session, PID: os.Getppid()}
		if !homedir.AppendLine(place, record) {
			gaps := ""
			if len(handed) > 0 {
				gaps = strings.Join(handed, ", ") + " were recorded and are gaps; "
			}
			return nil, project.Fail("%s cannot be written; %sno number was handed out", place, gaps)
		}
		handed = append(handed, identifier)
	}
	return handed, nil
}

// flock takes the exclusive lock, waiting; the system frees it when its holder dies.
func flock(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX) // #nosec G115 -- a file descriptor fits an int
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	err := command(args, stdout)
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "jst id: %v\n", err)
	var failure *project.Problem
	if errors.As(err, &failure) {
		return failure.Code
	}
	return 2
}

func command(args []string, stdout io.Writer) error {
	asJSON := slices.Contains(args, "--json")
	args = slices.DeleteFunc(slices.Clone(args), func(arg string) bool { return arg == "--json" })
	if len(args) == 0 {
		return project.Fail(usage)
	}
	verb, rest := args[0], args[1:]
	ctx := context.Background()
	proj, err := project.Here([]string{"ids"})
	if err != nil {
		return project.Fail("%v", err)
	}
	switch verb {
	case "next":
		count := 1
		if position := slices.Index(rest, "--count"); position >= 0 {
			count = 0
			if position+1 < len(rest) {
				count, _ = strconv.Atoi(rest[position+1])
			}
			rest = slices.Delete(slices.Clone(rest), position, min(position+2, len(rest)))
			if count < 1 || count > 100 {
				return project.Fail("--count takes 1 to 100")
			}
		}
		if len(rest) != 1 {
			return project.Fail(usage)
		}
		handed, err := Take(ctx, proj, rest[0], count)
		if err != nil {
			return err
		}
		if asJSON {
			return writeJSON(stdout, map[string]any{"series": rest[0], "ids": handed})
		}
		fmt.Fprintln(stdout, strings.Join(handed, "\n"))
		return nil
	case "peek":
		if len(rest) != 1 {
			return project.Fail(usage)
		}
		entry, err := SeriesOf(proj, rest[0])
		if err != nil {
			return err
		}
		top, _, err := Highest(ctx, proj, entry)
		if err != nil {
			return err
		}
		if top >= series.MaxNumber {
			return project.Fail("ids.%s: the next number would be above %d, which jackstay does not count", rest[0], series.MaxNumber)
		}
		fmt.Fprintln(stdout, series.Format(rest[0], entry.Width, top+1))
		return nil
	case "list":
		if len(rest) > 1 {
			return project.Fail(usage)
		}
		return list(ctx, proj, rest, asJSON, stdout)
	}
	return project.Fail(usage)
}

// row is one number handed out, as `jst id list` shows it.
type row struct {
	ID       string  `json:"id"`
	At       string  `json:"at"`
	Worktree string  `json:"worktree"`
	Session  *string `json:"session"`
	File     bool    `json:"file"`
}

func list(ctx context.Context, proj project.Project, names []string, asJSON bool, stdout io.Writer) error {
	if len(names) == 0 {
		for name := range proj.Config.Section("ids") {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	rows := []row{}
	for _, name := range names {
		entry, err := SeriesOf(proj, name)
		if err != nil {
			return err
		}
		_, seen, err := Highest(ctx, proj, entry)
		if err != nil {
			return err
		}
		records, err := Recorded(proj, name)
		if err != nil {
			return err
		}
		for _, record := range records {
			rows = append(rows, row{ID: record.ID, At: record.At, Worktree: record.Worktree, Session: record.Session,
				File: seen[record.Number]["file"]})
		}
	}
	if asJSON {
		return writeJSON(stdout, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no number has been handed out here")
	}
	for _, item := range rows {
		file, session := "no file yet", "session unknown"
		if item.File {
			file = "has a file"
		}
		if item.Session != nil {
			session = *item.Session
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", item.ID, item.At, file, session, item.Worktree)
	}
	return nil
}

func writeJSON(stdout io.Writer, value any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
