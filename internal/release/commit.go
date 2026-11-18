package release

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/gitcli"
)

// Identity is who the workflow's commits and tags are by.
type Identity struct {
	Name  string
	Email string
}

// commitTree makes a commit on parent whose tree is parent's with writes put in and removes taken
// out, without touching any checkout: a temporary index, git's plumbing. It returns the commit id.
func (repo Repo) commitOn(parent string, writes map[string][]byte, removes []string, message string, who Identity) (string, error) {
	scratch, err := os.MkdirTemp("", "jackstay-release-index-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	env := append(gitcli.WithoutRepository(os.Environ()), "GIT_INDEX_FILE="+filepath.Join(scratch, "index"), "GIT_LITERAL_PATHSPECS=1")
	if who.Name != "" {
		env = append(env, "GIT_AUTHOR_NAME="+who.Name, "GIT_COMMITTER_NAME="+who.Name,
			"GIT_AUTHOR_EMAIL="+who.Email, "GIT_COMMITTER_EMAIL="+who.Email)
	}
	git := gitcli.Git{Dir: repo.Root, Env: env}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if _, err := git.Run(ctx, "read-tree", parent); err != nil {
		return "", err
	}
	paths := make([]string, 0, len(writes))
	for filePath := range writes {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	headTree := repo.Tree(parent)
	for _, filePath := range paths {
		mode := changes.ModeFile
		if files, err := headTree.List(parentDir(filePath)); err == nil {
			for _, file := range files {
				if file.Path == filePath && file.Mode == changes.ModeExecutable {
					mode = changes.ModeExecutable
				}
			}
		}
		blob, err := repo.writeBlob(writes[filePath])
		if err != nil {
			return "", err
		}
		if _, err := git.Run(ctx, "update-index", "--add", "--cacheinfo", mode+","+blob+","+filePath); err != nil {
			return "", err
		}
	}
	for _, filePath := range removes {
		if _, err := git.Run(ctx, "update-index", "--force-remove", "--", filePath); err != nil {
			return "", err
		}
	}
	tree, err := git.Line(ctx, "write-tree")
	if err != nil {
		return "", err
	}
	messageFile := filepath.Join(scratch, "message")
	if err := os.WriteFile(messageFile, []byte(message), 0o600); err != nil {
		return "", err
	}
	return git.Line(ctx, "commit-tree", strings.TrimSpace(tree), "-p", parent, "-F", messageFile)
}

func parentDir(filePath string) string {
	if at := strings.LastIndex(filePath, "/"); at >= 0 {
		return filePath[:at]
	}
	return "."
}

// writeBlob stores content as a blob and returns its id.
func (repo Repo) writeBlob(data []byte) (string, error) {
	scratch, err := os.CreateTemp("", "jackstay-release-blob-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(scratch.Name()) }()
	if _, err := scratch.Write(data); err != nil {
		_ = scratch.Close()
		return "", err
	}
	if err := scratch.Close(); err != nil {
		return "", err
	}
	return repo.line("hash-object", "-w", "--no-filters", scratch.Name())
}

// RemoteRefs are a remote's refs as `git ls-remote` gives them: name -> commit (tags peeled).
func (repo Repo) RemoteRefs(remote string, patterns ...string) (map[string]string, error) {
	out, err := repo.run(append([]string{"ls-remote", remote}, patterns...)...)
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	peeled := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if name, isPeeled := strings.CutSuffix(fields[1], "^{}"); isPeeled {
			peeled[name] = fields[0]
			continue
		}
		refs[fields[1]] = fields[0]
	}
	for name, commit := range peeled {
		refs[name] = commit
	}
	return refs, nil
}

// pushLeased pushes commit to a remote branch only if the branch is exactly expected ("" for
// absent). It returns refused true when git refused for the lease (the branch moved).
func (repo Repo) pushLeased(remote, branch, commit, expected string) (refused bool, err error) {
	ref := "refs/heads/" + branch
	// Not --porcelain: its per-ref report goes to standard output, which a failed run's error does
	// not carry; git's own refusal ("stale info", "rejected") is on standard error.
	_, err = repo.run("push", "--force-with-lease="+ref+":"+expected, remote, commit+":"+ref)
	if err == nil {
		return false, nil
	}
	if exitErr, ok := err.(*gitcli.ExitError); ok && (strings.Contains(exitErr.Stderr, "stale info") ||
		strings.Contains(exitErr.Stderr, "rejected") || strings.Contains(exitErr.Stderr, "non-fast-forward")) {
		return true, nil
	}
	return false, err
}

// deleteLeased deletes a remote branch only if it is exactly expected.
func (repo Repo) deleteLeased(remote, branch, expected string) error {
	ref := "refs/heads/" + branch
	_, err := repo.run("push", "--porcelain", "--force-with-lease="+ref+":"+expected, remote, ":"+ref)
	return err
}

// tagAndPush makes an annotated tag on commit and pushes it without force.
func (repo Repo) tagAndPush(remote, tag, commit, message string, who Identity) error {
	ref := "refs/tags/" + tag
	if existing, err := repo.line("rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil && existing != "" {
		if existing != commit {
			return fmt.Errorf("a local tag %s is on %s, not on %s", tag, short(existing), short(commit))
		}
		if kind, err := repo.line("cat-file", "-t", ref); err != nil || kind != "tag" {
			return fmt.Errorf("a local tag %s exists and is not annotated; remove it, the release's tag is annotated", tag)
		}
	} else {
		env := gitcli.WithoutRepository(os.Environ())
		if who.Name != "" {
			env = append(env, "GIT_COMMITTER_NAME="+who.Name, "GIT_COMMITTER_EMAIL="+who.Email)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := (gitcli.Git{Dir: repo.Root, Env: env}).Run(ctx, "tag", "-a", "-m", message, tag, commit); err != nil {
			return err
		}
	}
	_, err := repo.run("push", "--porcelain", remote, ref)
	return err
}

// fetchBase fetches the base branch and returns its tip as the remote has it.
func (repo Repo) fetchBase(remote, base string) (string, error) {
	tracking := "refs/remotes/" + remote + "/" + base
	if _, err := repo.run("fetch", "--no-tags", "--quiet", remote, "+refs/heads/"+base+":"+tracking); err != nil {
		return "", err
	}
	return repo.line("rev-parse", "--verify", tracking+"^{commit}")
}
