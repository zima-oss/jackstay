package receipts

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLatestRunsPicksTheNewestOfTheCheckoutAndReadsStrictly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	if _, _, err := LatestRuns("demo", "/work/a"); !errors.Is(err, ErrNoRunLog) {
		t.Fatalf("no log: %v", err)
	}
	log := filepath.Join(home, ".all", "runs.jsonl")
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		t.Fatal(err)
	}
	lines := `{"event":"gate","project":"demo","worktree":"/work/a","epoch":2,"outcome":"failed"}
{"event":"gate","project":"demo","worktree":"/work/a","epoch":1,"outcome":"passed"}
{"event":"gate","project":"demo","worktree":"/work/b","epoch":9,"outcome":"passed"}
{"event":"gate","project":"other","worktree":"/work/a","epoch":9,"outcome":"passed"}
{"event":"run","project":"demo","worktree":"/work/a","epoch":3,"outcome":"passed","stored":true,"key":"unit"}
{"event":"run","project":"demo","worktree":"/work/a","epoch":4,"outcome":"passed","stored":false,"key":"lint"}
{"event":"run","project":"demo","worktree":"/work/a","epoch":5,"outcome":"passed","stored":true,"key":"inner","inside":"outer"}
`
	if err := os.WriteFile(log, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	gate, pass, err := LatestRuns("demo", "/work/a")
	if err != nil || gate.Text("outcome") != "failed" || pass.Text("key") != "unit" {
		t.Fatalf("gate %v pass %v: %v", gate, pass, err)
	}
	if gate, pass, err := LatestRuns("demo", "/work/c"); err != nil || gate != nil || pass != nil {
		t.Fatalf("another checkout: %v %v %v", gate, pass, err)
	}
	for name, broken := range map[string]string{"unfinished": lines + `{"event":"gate"`, "not an object": lines + "[1]\n"} {
		if err := os.WriteFile(log, []byte(broken), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LatestRuns("demo", "/work/a"); err == nil {
			t.Fatalf("%s: a broken log read as complete", name)
		}
	}
}
