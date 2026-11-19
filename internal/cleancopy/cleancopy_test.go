package cleancopy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/pinned"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

func TestASweepRemovesOnlyCopiesWhoseOwnerIsGone(t *testing.T) {
	copies := t.TempDir()
	for name, owner := range map[string]string{"dead": OwnerPrefix + "999999 1", "live": pinned.OwnWords(OwnerPrefix)} {
		if err := os.MkdirAll(filepath.Join(copies, name, "repo"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(copies, name, "owner"), []byte(owner), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(copies, "orphan"), 0o700); err != nil {
		t.Fatal(err)
	}
	removed := Sweep(copies, time.Now(), func(words string) bool { return pinned.Alive(OwnerPrefix, words) })
	if len(removed) != 1 || filepath.Base(removed[0]) != "dead" {
		t.Fatalf("removed %v", removed)
	}
	removed = Sweep(copies, time.Now().Add(2*time.Hour), func(string) bool { return true })
	if len(removed) != 1 || filepath.Base(removed[0]) != "orphan" {
		t.Fatalf("an old folder with no owner: removed %v", removed)
	}
}

func TestCopyKeepsTheSourceObjectFormat(t *testing.T) {
	t.Setenv("GIT_DEFAULT_HASH", "sha256")
	repo := testrepo.New(t, map[string]string{"file": "the committed contents"})
	head := repo.Git("rev-parse", "HEAD")
	t.Setenv("GIT_DEFAULT_HASH", "sha1")
	made, inside, err := Make(Request{
		Here: repo.Root, Commit: "HEAD", Project: "demo", Now: time.Now(),
		Git: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			return gitcli.Git{Dir: dir}.Run(ctx, args...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer made.Remove()
	if got := repo.GitIn(inside, "rev-parse", "--show-object-format"); got != "sha256" {
		t.Fatalf("the copy uses %s object IDs", got)
	}
	if got := repo.GitIn(inside, "rev-parse", "HEAD"); got != head {
		t.Fatalf("the copy checked out %s, want %s", got, head)
	}
}
