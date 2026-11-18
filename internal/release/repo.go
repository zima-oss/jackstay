package release

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/config"
)

// gitRunner is what the release reads and writes git through (internal/gitcli satisfies it).
type gitRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// Repo reads a repository's commits for the release: trees, messages, first-parent history.
type Repo struct {
	Root    string
	Git     gitRunner
	Format  string // the object format, "sha1" or "sha256"
	Timeout time.Duration
}

// NewRepo is the repository at root.
func NewRepo(root string) Repo {
	repo := Repo{Root: root, Git: changes.LiteralGit(root), Format: "sha1", Timeout: 60 * time.Second}
	if format, err := repo.line("rev-parse", "--show-object-format"); err == nil && format != "" {
		repo.Format = format
	}
	return repo
}

func (repo Repo) run(args ...string) ([]byte, error) {
	timeout := repo.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return repo.Git.Run(ctx, args...)
}

func (repo Repo) line(args ...string) (string, error) {
	out, err := repo.run(args...)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line), nil
}

// Resolve is the commit id a name gives.
func (repo Repo) Resolve(name string) (string, error) {
	return repo.line("rev-parse", "--verify", "--quiet", name+"^{commit}")
}

// MergeBase is the best common ancestor of two commits.
func (repo Repo) MergeBase(left, right string) (string, error) {
	return repo.line("merge-base", left, right)
}

// Shallow says whether the clone is shallow.
func (repo Repo) Shallow() (bool, error) {
	answer, err := repo.line("rev-parse", "--is-shallow-repository")
	return answer == "true", err
}

// Parent is a commit's first parent, "" for a root commit.
func (repo Repo) Parent(commit string) (string, error) {
	out, err := repo.line("rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return "", nil
	}
	return fields[1], nil
}

// CommitterDate is a commit's committer date in UTC, YYYY-MM-DD.
func (repo Repo) CommitterDate(commit string) (string, error) {
	out, err := repo.line("log", "-1", "--format=%ct", commit)
	if err != nil {
		return "", err
	}
	var seconds int64
	if _, err := fmt.Sscan(out, &seconds); err != nil {
		return "", fmt.Errorf("the committer date of %s: %w", commit, err)
	}
	return time.Unix(seconds, 0).UTC().Format("2006-01-02"), nil
}

// Message is a commit's whole message.
func (repo Repo) Message(commit string) (string, error) {
	out, err := repo.run("log", "-1", "--format=%B", commit)
	return string(out), err
}

// FirstParentWith is the commits on tip's first-parent line whose message has a line matching
// pattern (git's --grep, extended), oldest first.
func (repo Repo) FirstParentWith(tip, pattern string) ([]string, error) {
	out, err := repo.run("log", "--first-parent", "--format=%H", "-E", "--grep="+pattern, tip)
	if err != nil {
		return nil, err
	}
	commits := strings.Fields(string(out))
	for left, right := 0, len(commits)-1; left < right; left, right = left+1, right-1 {
		commits[left], commits[right] = commits[right], commits[left]
	}
	return commits, nil
}

// LastTouch is the newest commit on tip's first-parent line, at or before tip, that changed
// filePath ("" when none did).
func (repo Repo) LastTouch(tip, filePath string) (string, error) {
	return repo.line("log", "--first-parent", "-1", "--format=%H", tip, "--", filePath)
}

// Change is one path a commit changed, by git's name-status letter.
type Change struct {
	Status string // A, M, D, T
	Path   string
}

// Changes are what changed between two commits, without rename detection.
func (repo Repo) Changes(from, to string) ([]Change, error) {
	out, err := repo.run("diff", "--name-status", "-z", "--no-renames", from, to)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	var found []Change
	for index := 0; index+1 < len(fields); index += 2 {
		found = append(found, Change{Status: fields[index][:1], Path: fields[index+1]})
	}
	sort.Slice(found, func(left, right int) bool { return found[left].Path < found[right].Path })
	return found, nil
}

// ConfigAt is the jackstay.json of a commit, read without its problems judged (history is read as it
// was written).
func (repo Repo) ConfigAt(commit string) (config.Config, error) {
	data, err := repo.run("show", commit+":"+config.Name)
	if err != nil {
		return nil, err
	}
	return config.Parse(data)
}

// ConfigIfAt is ConfigAt, with exists false (and no error) when the commit has no jackstay.json:
// the base of the pull request that adopts jackstay. Both read the file at the repository's top
// (--full-tree), wherever in the checkout the command runs.
func (repo Repo) ConfigIfAt(commit string) (loaded config.Config, exists bool, err error) {
	listed, err := repo.run("ls-tree", "--full-tree", "--name-only", commit, "--", config.Name)
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(string(listed)) == "" {
		return nil, false, nil
	}
	loaded, err = repo.ConfigAt(commit)
	return loaded, true, err
}

// Tree is a commit's tree as a changes.Tree.
func (repo Repo) Tree(commit string) changes.Tree { return commitTree{repo: repo, commit: commit} }

// commitTree reads a commit's files.
type commitTree struct {
	repo   Repo
	commit string
}

// List implements changes.Tree.
func (tree commitTree) List(dir string) ([]changes.File, error) {
	out, err := tree.repo.run("ls-tree", "-z", tree.commit, "--", strings.TrimSuffix(dir, "/")+"/")
	if err != nil {
		return nil, err
	}
	var files []changes.File
	for _, record := range strings.Split(string(out), "\x00") {
		meta, filePath, found := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		if !found || len(fields) != 3 || fields[1] == "tree" || path.Dir(filePath) != path.Clean(dir) {
			continue
		}
		files = append(files, changes.File{Path: filePath, Mode: fields[0], Blob: fields[2]})
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return files, nil
}

// Read implements changes.Tree.
func (tree commitTree) Read(filePath string) ([]byte, bool, error) {
	out, err := tree.repo.run("ls-tree", tree.commit, "--", filePath)
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil, false, nil
	}
	if fields := strings.Fields(string(out)); len(fields) < 2 || fields[1] != "blob" {
		return nil, false, fmt.Errorf("%s in %s is not a file", filePath, short(tree.commit))
	}
	// The entry is there: a failure to read its blob is an error, never "absent".
	data, err := tree.repo.run("cat-file", "blob", tree.commit+":"+filePath)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}
