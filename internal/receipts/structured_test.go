package receipts

import (
	"os"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/testreport"
)

func TestDirectStructuredReportsRecordAndKeyTheEvidence(t *testing.T) {
	const settings = `{"jackstay_schema":1,"project":"demo","receipts":{"inputs":["src"],"toolchain":[],"env":[],"commands":[
		{"argv":["go","test","-json"],"judge":"go-json","reuse":true},
		{"argv":["pytest-wrapper"],"judge":"junit","report":"report.xml","empty_exit":5,"reuse":true}]}}`
	world := newWorld(t, settings)
	world.fake = &fakeRun{stdout: []string{`{"Action":"start","Package":"demo"}`, `{"Action":"run","Package":"demo","Test":"TestIt"}`,
		`{"Action":"pass","Package":"demo","Test":"TestIt"}`, `{"Action":"pass","Package":"demo"}`}}
	if status := world.test("--", "go", "test", "-json"); status != 0 {
		t.Fatalf("Go status %d: %s", status, world.stderr.String())
	}
	if record := world.lastRecord(EventRun); record.Text("judge") != "go-json" || record["evidence_count"] != 1.0 || record["stored"] != true {
		t.Fatalf("Go record %v", record)
	}
	world.fake = &fakeRun{}
	var reportPath string
	world.fake.during = func() {
		for _, pair := range world.fake.spec.Env {
			if path, found := strings.CutPrefix(pair, testreport.PathVariable+"="); found {
				reportPath = path
				break
			}
		}
		if err := os.WriteFile(reportPath, []byte(`<testsuite tests="1"><testcase name="case"/></testsuite>`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if status := world.test("--", "pytest-wrapper"); status != 0 {
		t.Fatalf("JUnit status %d: %s", status, world.stderr.String())
	}
	if record := world.lastRecord(EventRun); record.Text("judge") != "junit" || record["evidence_count"] != 1.0 || record["stored"] != true {
		t.Fatalf("JUnit record %v", record)
	}
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatalf("report path survived the attempt: %q (%v)", reportPath, err)
	}
	world.fake = &fakeRun{}
	if status := world.test("--", "pytest-wrapper"); status != 0 || world.fake.calls != 0 {
		t.Fatalf("structured receipt not reused: status %d, calls %d, %s", status, world.fake.calls, world.stderr.String())
	}
	world.repo.Write(map[string]string{"jackstay.json": strings.Replace(settings, "report.xml", "new.xml", 1)})
	if status := world.test("--", "pytest-wrapper"); status != StatusUnjudged || world.fake.calls != 1 {
		t.Fatalf("changed config reused stale evidence: status %d, calls %d, %s", status, world.fake.calls, world.stderr.String())
	}
}
