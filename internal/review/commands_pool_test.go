package review

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/testrepo"
)

func TestQueuedReviewChildRejectsPolicyChange(t *testing.T) {
	repository := testrepo.New(t, map[string]string{
		"jackstay.json": `{"jackstay_schema":2,"project":"pool-child"}`,
		"file.txt":      "first\n",
	})
	repository.Commit("change", map[string]string{"file.txt": "second\n"})
	t.Setenv(PoolPolicyIDVariable, "v1:stale")
	var output, diagnostics bytes.Buffer
	status := runReview([]string{"HEAD^..HEAD", "--task", "review-task", "--reviewer", "codex"}, nil, &output, &diagnostics)
	if status != ExitUsage || !strings.Contains(diagnostics.String(), "policy changed before reviewer launch") {
		t.Fatalf("child status %d, diagnostics %q", status, diagnostics.String())
	}
}
