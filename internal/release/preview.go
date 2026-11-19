package release

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zima-oss/jackstay/internal/gitcli"
)

// previewGit uses the checkout's working directory and configuration for transport, while all
// objects, refs, and transient Git files belong to the scratch repository. It does not copy
// configuration, which may include credentials.
type previewGit struct {
	gitcli.Git
	dir    string
	config string
}

func (preview previewGit) Run(ctx context.Context, args ...string) ([]byte, error) {
	flags := []string{"--git-dir=" + preview.dir, "--work-tree=" + filepath.Join(preview.dir, "work"),
		"-c", "include.path=" + preview.config, "-c", "core.bare=false", "-c", "core.hooksPath=/dev/null"}
	return preview.Git.Run(ctx, append(flags, args...)...)
}

func (repo Repo) commitGit(env []string) gitRunner {
	git := gitcli.Git{Dir: repo.Root, Env: env}
	if preview, ok := repo.Git.(previewGit); ok {
		preview.Git = git
		return preview
	}
	return git
}

// inPreview keeps dry-run proof identical to the live path, with a separate object store.
func (flow *Flow) inPreview() (func(), error) {
	if !flow.DryRun {
		return func() {}, nil
	}
	common, err := flow.Repo.line("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "jackstay-release-preview-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(scratch) }
	git := gitcli.Git{Dir: scratch}
	if _, err := git.Run(flow.context(), "init", "--quiet", "--bare", "--template=", "--object-format="+flow.Repo.Format); err != nil {
		cleanup()
		return nil, fmt.Errorf("creating the release preview repository: %w", err)
	}
	if err := os.Mkdir(filepath.Join(scratch, "work"), 0o700); err != nil {
		cleanup()
		return nil, err
	}
	flow.Repo.Git = previewGit{
		Git: gitcli.Git{Dir: flow.Repo.Root, Env: append(gitcli.WithoutRepository(os.Environ()), "GIT_LITERAL_PATHSPECS=1")},
		dir: scratch, config: filepath.Join(common, "config"),
	}
	return cleanup, nil
}
