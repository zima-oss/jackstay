package golden

import (
	"context"
	"errors"
	"os"

	"github.com/zima-oss/jackstay/internal/review/sourceview"
)

// fakeRepo is the review's repository for the scenarios: commit c1 on c0, AGENTS.md in it.
type fakeRepo struct{}

func newFakeRepo() *fakeRepo { return &fakeRepo{} }

var commits = map[string]string{"HEAD": "c1", "c1": "c1", "c0": "c0", "main": "c0"}

func (repo *fakeRepo) Resolve(_ context.Context, name string) (string, error) {
	if id, ok := commits[name]; ok {
		return id, nil
	}
	return "", errors.New(name + " is not a commit here")
}
func (repo *fakeRepo) FirstParent(_ context.Context, commit string) (string, error) {
	if commit == "c1" {
		return "c0", nil
	}
	return "", nil
}
func (repo *fakeRepo) Branch(context.Context) (string, error)          { return "golden", nil }
func (repo *fakeRepo) ValidBranchName(context.Context, string) bool    { return true }
func (repo *fakeRepo) Dirty(context.Context) (bool, error)             { return false, nil }
func (repo *fakeRepo) Subject(context.Context, string) (string, error) { return "subject", nil }
func (repo *fakeRepo) Show(_ context.Context, _, path string) ([]byte, error) {
	if path == "AGENTS.md" {
		return []byte("rules"), nil
	}
	return nil, errors.New("missing")
}
func (repo *fakeRepo) Exists(_ context.Context, _, path string) bool { return path == "AGENTS.md" }
func (repo *fakeRepo) BuildSource(_ context.Context, path string, _ sourceview.Revisions) error {
	return os.MkdirAll(path, 0o700)
}
func (repo *fakeRepo) RemoveTree(_ context.Context, path string) error {
	return os.RemoveAll(path)
}
func (repo *fakeRepo) LockedTrees(context.Context) (map[string]string, error) { return nil, nil }
