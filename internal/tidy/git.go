package tidy

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/guard"
)

// gitTimeout bounds each call to git (a fetch or a worktree removal over a slow disk included).
const gitTimeout = 5 * time.Minute

// repo asks git about one folder.
type repo struct {
	dir string
}

func (here repo) git() gitcli.Git { return gitcli.Git{Dir: here.dir} }

func (here repo) run(ctx context.Context, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	return here.git().Run(bounded, args...)
}

// line is git's answer without its surrounding space ("" with the error when git refused). Never
// for a NUL-separated list of paths: a name may begin with a space.
func (here repo) line(ctx context.Context, args ...string) (string, error) {
	out, err := here.run(ctx, args...)
	return strings.TrimSpace(string(out)), err
}

func (here repo) succeeds(ctx context.Context, args ...string) bool {
	_, err := here.run(ctx, args...)
	return err == nil
}

// gitSaid is what git said on its standard error when it refused, cut to limit characters.
func gitSaid(err error, limit int) string {
	var exitErr *gitcli.ExitError
	said := err.Error()
	if errors.As(err, &exitErr) {
		said = exitErr.Diagnostic()
	}
	if len(said) > limit {
		said = said[:limit] + " …"
	}
	return said
}

// tipOf is the commit a local branch points to, "" when there is no such branch.
func (here repo) tipOf(ctx context.Context, branch string) string {
	tip, err := here.line(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return ""
	}
	return tip
}

// ahead is how many commits of tip are not on ref; false when git cannot say.
func (here repo) ahead(ctx context.Context, ref, tip string) (int, bool) {
	text, err := here.line(ctx, "rev-list", "--count", ref+".."+tip)
	if err != nil {
		return 0, false
	}
	count, err := strconv.Atoi(text)
	return count, err == nil
}

// holds says whether commit head is here and has tip among its ancestors (or is it).
func (here repo) holds(ctx context.Context, head, tip string) bool {
	kind, err := here.line(ctx, "cat-file", "-t", head)
	if err != nil || kind != "commit" {
		return false
	}
	return here.succeeds(ctx, "merge-base", "--is-ancestor", tip, head)
}

// addsNothing is the guard's test (docs/design/guard.md section 0, "A branch that adds nothing"),
// asked of a given default ref and the tip tidy judged; any doubt is false.
func (here repo) addsNothing(_ context.Context, base, tip string) bool {
	return guard.NewGit(nil).AddsNothingAt(here.dir, base, tip)
}

// realPath is where a path really leads: the links of its longest existing part followed (a
// worktree whose folder is gone is still named by its parent's real path).
func realPath(target string) string {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return filepath.Clean(target)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return absolute
	}
	return filepath.Join(realPath(parent), filepath.Base(absolute))
}

// inside says whether place is folder or inside it (both real paths).
func inside(place, folder string) bool {
	return place == folder || strings.HasPrefix(place, strings.TrimSuffix(folder, "/")+"/")
}

// matchName is a shell pattern against one name, `[!…]` read as `[^…]` (fnmatch's spelling).
func matchName(pattern, name string) bool {
	matched, err := path.Match(strings.ReplaceAll(pattern, "[!", "[^"), name)
	return err == nil && matched
}
