package reviewpool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

func TestLinkedMCPWorktreesShareOnlyEquivalentPinnedReviews(t *testing.T) {
	ctx := context.Background()
	repo := testrepo.New(t, map[string]string{
		"jackstay.json": `{"jackstay_schema":2,"project":"linked-pool"}`,
		"sample.txt":    "first\n",
	})
	repo.Commit("second", map[string]string{"sample.txt": "second\n"})
	linked := filepath.Join(t.TempDir(), "linked")
	repo.Git("worktree", "add", "--detach", linked, "HEAD")
	common, _, _, _, _, err := repositoryIdentity(ctx, repo.Root)
	if err != nil {
		t.Fatal(err)
	}
	pool := Pool{Root: filepath.Join(repo.Home, "pool")}
	request := Request{Repository: repo.Root, Base: "HEAD^", Head: "HEAD", Task: "shared", Reviewers: []string{"codex"}}
	firstSession, secondSession := strings.Repeat("a", 64), strings.Repeat("b", 64)
	first, created, err := pool.EnqueueForSession(ctx, request, firstSession, common)
	if err != nil || !created {
		t.Fatalf("first enqueue: %t, %v", created, err)
	}
	request.Repository = linked
	shared, created, err := pool.EnqueueForSession(ctx, request, secondSession, common)
	if err != nil || created || shared.ID != first.ID || shared.Repository != first.Repository {
		t.Fatalf("linked enqueue did not share the original worker checkout: %+v, %t, %v", shared, created, err)
	}
	if err := os.WriteFile(filepath.Join(linked, "jackstay.json"),
		[]byte(`{"jackstay_schema":2,"project":"linked-pool","review":{"reviewers":{"codex":{"model":"different"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	different, created, err := pool.EnqueueForSession(ctx, request, secondSession, common)
	if err != nil || !created || different.ID == first.ID {
		t.Fatalf("distinct policy incorrectly deduplicated: %+v, %t, %v", different, created, err)
	}
	calls := 0
	finished, found, err := pool.RunNext(ctx, func(_ context.Context, current Job) (int, error) {
		calls++
		if current.Repository != first.Repository {
			t.Fatalf("worker checkout changed: %s", current.Repository)
		}
		return review.ExitClear, nil
	})
	if err != nil || !found || finished.ID != first.ID || calls != 1 {
		t.Fatalf("shared execution: %+v, %t, %v, calls=%d", finished, found, err, calls)
	}
	for _, recipient := range []string{firstSession, secondSession} {
		events, err := pool.Undelivered(ctx, recipient, common)
		if err != nil || len(events) != 1 || events[0].JobID != first.ID {
			t.Fatalf("shared result for recipient: %+v, %v", events, err)
		}
	}
}
