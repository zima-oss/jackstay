// Package remote is the rule `jst sync`, `jst start`, `jst tidy` and `jst gh` share for which remote a
// project's work goes to, which branch is its default as fetched, and which GitHub repository the
// remote names (docs/design/implementation.md sections 8.1 and 8.2).
//
// The remote: jackstay.json's `github.remote` when it is set; else `origin`; else the remote the local
// `main` (then `master`) tracks; else the only remote. A project's remote need not be called
// `origin` (some projects use another name).
package remote

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Runner asks git a question in one repository and returns its standard output
// (internal/gitcli.Git is one).
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ErrNoRemote is a repository with no remote at all.
var ErrNoRemote = errors.New("the repository has no remote")

// UnclearError is several remotes and none the rule picks.
type UnclearError struct {
	Remotes []string
}

func (unclear *UnclearError) Error() string {
	return fmt.Sprintf("cannot tell which remote to use: %s; set github.remote in jackstay.json, or main's upstream "+
		"(`git branch --set-upstream-to <remote>/main main`)", strings.Join(unclear.Remotes, ", "))
}

// UnknownError is a `github.remote` that names no remote of the repository.
type UnknownError struct {
	Name    string
	Remotes []string
}

func (unknown *UnknownError) Error() string {
	listed := strings.Join(unknown.Remotes, ", ")
	if listed == "" {
		listed = "none"
	}
	return fmt.Sprintf("github.remote in jackstay.json is %q, which is not a remote here (the remotes: %s)", unknown.Name, listed)
}

// NoDefaultBranchError is a repository where no default branch can be named.
type NoDefaultBranchError struct {
	// Remote is the remote looked at ("" when there is none).
	Remote string
	// Fetched says only the remote's refs were looked at (a local branch would not do).
	Fetched bool
}

func (missing *NoDefaultBranchError) Error() string {
	if missing.Fetched {
		return fmt.Sprintf("%s has no default branch here that git can name (no %s/HEAD, %s/main or %s/master fetched): give --from REF",
			missing.Remote, missing.Remote, missing.Remote, missing.Remote)
	}
	if missing.Remote == "" {
		return "no default branch (no remote, and no local main or master)"
	}
	return fmt.Sprintf("no default branch (%s/HEAD, %s/main, %s/master, main, master)", missing.Remote, missing.Remote, missing.Remote)
}

// ConfiguredRemote is `github.remote` of a project's `github` section (nil when there is none), or "".
func ConfiguredRemote(github map[string]any) string {
	name, _ := github["remote"].(string)
	return name
}

// Name is the remote the work goes to, by the rule above; configured is `github.remote` ("" when
// not set). ErrNoRemote, *UnclearError or *UnknownError when the rule names none.
func Name(ctx context.Context, git Runner, configured string) (string, error) {
	listed, err := git.Run(ctx, "remote")
	if err != nil {
		return "", err
	}
	remotes := strings.Fields(string(listed))
	if configured != "" {
		if slices.Contains(remotes, configured) {
			return configured, nil
		}
		return "", &UnknownError{Name: configured, Remotes: remotes}
	}
	if len(remotes) == 0 {
		return "", ErrNoRemote
	}
	if slices.Contains(remotes, "origin") {
		return "origin", nil
	}
	for _, branch := range []string{"main", "master"} {
		tracked, err := git.Run(ctx, "config", "--get", "branch."+branch+".remote")
		if err != nil {
			continue // not set: git answers 1
		}
		if name := strings.TrimSpace(string(tracked)); slices.Contains(remotes, name) {
			return name, nil
		}
	}
	if len(remotes) == 1 {
		return remotes[0], nil
	}
	return "", &UnclearError{Remotes: remotes}
}

// Default is a default branch: the ref compared against and the branch name it stands for.
type Default struct {
	// Ref is the full ref (`refs/remotes/origin/main`, or `refs/heads/main` when not fetched).
	Ref string
	// Short is the branch name (`main`; `release/stable` for `refs/remotes/origin/release/stable`).
	Short string
	// Fetched says Ref is the remote's, as last fetched.
	Fetched bool
}

// FetchedDefault is the remote's default branch as fetched: what `refs/remotes/<remote>/HEAD`
// points to, else `<remote>/main`, else `<remote>/master`. A local branch never stands in for it
// (it may be days old): *NoDefaultBranchError asking for --from.
func FetchedDefault(ctx context.Context, git Runner, remote string) (Default, error) {
	if remote != "" {
		prefix := "refs/remotes/" + remote + "/"
		var candidates []string
		if head, err := git.Run(ctx, "symbolic-ref", "--quiet", prefix+"HEAD"); err == nil {
			if target := strings.TrimSpace(string(head)); strings.HasPrefix(target, prefix) {
				candidates = append(candidates, target)
			}
		}
		candidates = append(candidates, prefix+"main", prefix+"master")
		for _, ref := range candidates {
			if isCommit(ctx, git, ref) {
				return Default{Ref: ref, Short: strings.TrimPrefix(ref, prefix), Fetched: true}, nil
			}
		}
	}
	return Default{}, &NoDefaultBranchError{Remote: remote, Fetched: true}
}

// DefaultBranch is FetchedDefault, else the local `main`, else `master` (a repository with no
// remote, or a remote with none fetched). *NoDefaultBranchError when there is none.
func DefaultBranch(ctx context.Context, git Runner, remote string) (Default, error) {
	if found, err := FetchedDefault(ctx, git, remote); err == nil {
		return found, nil
	}
	for _, branch := range []string{"main", "master"} {
		if ref := "refs/heads/" + branch; isCommit(ctx, git, ref) {
			return Default{Ref: ref, Short: branch}, nil
		}
	}
	return Default{}, &NoDefaultBranchError{Remote: remote}
}

func isCommit(ctx context.Context, git Runner, ref string) bool {
	_, err := git.Run(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}
