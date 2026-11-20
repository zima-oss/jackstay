package status

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/contextbrief"
)

func TestStatusWithoutPathsIsThisRepositoryInDetail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	root := repository(t, t.TempDir(), "app")
	t.Chdir(root)
	var output, diagnostics bytes.Buffer
	if code := run(nil, nil, &output, &diagnostics); code != 0 {
		t.Fatalf("exit %d: %s", code, &diagnostics)
	}
	text := output.String()
	for _, want := range []string{"shared at " + root + " (main)", "worktrees  1: main", "sessions   0 in the last 24h",
		"brief      none", "chat       0 session(s), 0 pending", "background none", "gate       none", "cards      1 active (T-1 ready)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Private card body") {
		t.Fatal("a card body was printed")
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("status wrote into the jackstay home: %v %v", entries, err)
	}
}

func TestStatusShowsTheBriefAndBackgroundJobsOfTheRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	parent := t.TempDir()
	root := repository(t, parent, "app")
	other := repository(t, parent, "other")
	store, err := contextbrief.StoreFor(root)
	if err != nil {
		t.Fatal(err)
	}
	identity := contextbrief.Identity{Branch: "elsewhere", Head: "abc", RepositoryID: "id"}
	if _, err := store.Save(contextbrief.Brief{TaskID: "task-7", Task: "Private brief text", Next: []string{"x"}}, 0, 168*time.Hour, identity); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	procsFolder := filepath.Join(home, ".all", "procs")
	for _, job := range []struct{ id, cwd string }{{"1800000000-1", filepath.Join(root, "sub")}, {"1800000000-2", other}} {
		write(t, filepath.Join(procsFolder, job.id+".json"),
			`{"id":"`+job.id+`","name":"build","supervisor":999999,"supervisor_start":1,"group":999998,"started":1,"leader_start":1,`+
				`"deadline":null,"keep":true,"session":null,"watched":null,"cwd":"`+job.cwd+`","command":["make"],"status":"running"}`)
	}
	var output, diagnostics bytes.Buffer
	run([]string{"--json", root}, nil, &output, &diagnostics)
	var report Report
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	found := report.Repositories[0]
	if report.Kind != KindRepository || found.Brief.Status != "saved" || found.Brief.TaskID != "task-7" ||
		found.Brief.Freshness != "repository-changed" || len(found.Background.Jobs) != 1 || found.Background.Jobs[0].ID != "1800000000-1" {
		t.Fatalf("brief and jobs: %+v %+v", found.Brief, found.Background)
	}
	if strings.Contains(output.String(), "Private brief text") || strings.Contains(output.String(), "make") {
		t.Fatalf("private text in the report: %s", &output)
	}
	if _, err := os.Stat(filepath.Join(procsFolder, "1800000000-1.json")); err != nil {
		t.Fatalf("status removed a job entry: %v", err)
	}
}

func TestStatusReadsTheConfiguredRepositoriesAsABoard(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	parent := t.TempDir()
	root := repository(t, parent, "hub")
	service := repository(t, parent, "service")
	write(t, filepath.Join(root, "jackstay.json"), `{"jackstay_schema":3,"project":"shared","status":{"repos":[".","../service"]}}`)
	t.Chdir(root)
	var output, diagnostics bytes.Buffer
	if code := run([]string{"--json"}, nil, &output, &diagnostics); code != 0 {
		t.Fatalf("exit %d: %s", code, &diagnostics)
	}
	var report Report
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Kind != KindBoard || len(report.Repositories) != 2 || report.Repositories[1].Path != service ||
		report.Repositories[0].Worktrees != nil || report.Repositories[0].Counts.Worktrees != 1 {
		t.Fatalf("board: %s", &output)
	}
	output.Reset()
	if code := run([]string{"."}, nil, &output, &diagnostics); code != 0 || !strings.Contains(output.String(), "worktrees  1: main") {
		t.Fatalf("one path is the detail: exit %d\n%s", code, &output)
	}
	output.Reset()
	run(nil, nil, &output, &diagnostics)
	if lines := strings.Count(output.String(), "\n"); lines != 2 || !strings.Contains(output.String(), "(shared): worktrees 1, sessions 0") {
		t.Fatalf("board text:\n%s", &output)
	}
}
