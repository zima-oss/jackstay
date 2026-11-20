package background

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/gitcli"
)

func TestJobsReportsStatesWithoutRemovingOrStopping(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 610, 611, "build", "session-a", time.Minute)
	h.listed(t, 620, 621, "", "", time.Minute)
	jobs, err := jobsOf(h.look.host, h.place)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].ID != "1800000000-610" || jobs[0].Name != "build" || jobs[0].State != "running" ||
		jobs[0].Cwd != "/work" || jobs[1].State != stateGone {
		t.Fatalf("jobs %+v", jobs)
	}
	if !h.place.listed("1800000000-620") {
		t.Fatal("reading the jobs removed a gone entry")
	}
	if got := eventPairs(t, h.place); len(got) != 0 {
		t.Fatalf("reading the jobs logged %v", got)
	}
}

func TestTheRepositoryOfAStartIsRecorded(t *testing.T) {
	outside := t.TempDir()
	if id := repositoryOf(outside); id != "" {
		t.Fatalf("a folder outside Git has repository %q", id)
	}
	inside := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", inside).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
	folder := filepath.Join(inside, "sub")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	common, err := filepath.EvalSymlinks(filepath.Join(inside, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := gitcli.CommonFolderInode(common)
	if id := repositoryOf(folder); err != nil || id != want || !strings.HasPrefix(id, "inode:") {
		t.Fatalf("repository %q, want %q (%v)", id, want, err)
	}
}

func TestJobsReportsAnUnreadableEntryInsteadOfLeavingItOut(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 610, 611, "build", "", time.Minute)
	if err := os.WriteFile(h.place.entryPath("1800000000-9"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if jobs, err := jobsOf(h.look.host, h.place); err == nil {
		t.Fatalf("an unreadable entry was left out: %+v", jobs)
	}
	empty := store{folder: filepath.Join(t.TempDir(), "absent")}
	if jobs, err := jobsOf(h.look.host, empty); err != nil || len(jobs) != 0 {
		t.Fatalf("no folder: %+v %v", jobs, err)
	}
}
