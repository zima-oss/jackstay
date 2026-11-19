//go:build integration

package judge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/testreport"
)

// VITEST_ROOT points to a disposable directory containing node_modules/vitest.
// The ordinary Go gate uses the checked-in reports and needs no Node installation.
func TestVitestJUnitProducer(t *testing.T) {
	root := os.Getenv("VITEST_ROOT")
	if root == "" {
		t.Skip("set VITEST_ROOT to a directory with Vitest 5.1.1 installed")
	}
	vitest := filepath.Join(root, "node_modules", ".bin", "vitest")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	version, err := exec.CommandContext(ctx, vitest, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "vitest/5.1.1 ") {
		t.Fatalf("expected Vitest 5.1.1, got %q: %v", version, err)
	}
	for _, sample := range []struct {
		name, file, pattern string
		status              int
		counted, skipped    int
		outcome             Outcome
	}{
		{name: "pass", file: "pass", counted: 1, skipped: 1, outcome: Passed},
		{name: "fail", file: "fail", status: 1, counted: 1, outcome: Failed},
		{name: "empty", file: "pass", pattern: "absent", skipped: 2, outcome: NothingRun},
		{name: "zero", file: "missing", outcome: NothingRun},
	} {
		t.Run(sample.name, func(t *testing.T) {
			project, err := os.MkdirTemp(root, "vitest-report-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(project) })
			source := sample.file + ".test.mjs"
			if sample.name != "zero" {
				contents, err := os.ReadFile(filepath.Join("testdata", "vitest", source))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(project, source), contents, 0644); err != nil {
					t.Fatal(err)
				}
			}
			reportPath := filepath.Join(project, "report.xml")
			args := []string{"run", source, "--reporter=junit", "--outputFile=" + reportPath}
			if sample.name == "zero" {
				args = append(args, "--passWithNoTests")
			}
			if sample.pattern != "" {
				args = append(args, "--testNamePattern="+sample.pattern)
			}
			command := exec.CommandContext(ctx, vitest, args...)
			command.Dir = project
			output, err := command.CombinedOutput()
			status := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("Vitest could not run: %v: %s", err, output)
				}
				status = exit.ExitCode()
			}
			if status != sample.status {
				t.Fatalf("Vitest exit %d, want %d: %s", status, sample.status, output)
			}
			report, err := os.Open(reportPath)
			if err != nil {
				t.Fatalf("Vitest report: %v: %s", err, output)
			}
			defer report.Close()
			facts := readJUnit(report)
			if len(facts.Unjudged) != 0 || facts.Counts.Counted != sample.counted ||
				facts.Counts.Skipped != sample.skipped {
				t.Fatalf("Vitest report facts %+v: %s", facts, output)
			}
			if verdict := Evaluate(facts, End{Status: status}, ScopePolicy); verdict.Outcome != sample.outcome {
				t.Fatalf("Vitest verdict %+v: %s", verdict, output)
			}
		})
	}
}

// Shards are a complete result only when every declared output exists and cases do not repeat.
// A retry that succeeds produces one final JUnit case; its intermediate attempts are not
// represented by Vitest's JUnit reporter.
func TestVitestJUnitRetryAndShards(t *testing.T) {
	root := os.Getenv("VITEST_ROOT")
	if root == "" {
		t.Skip("set VITEST_ROOT to a directory with Vitest 5.1.1 installed")
	}
	vitest := filepath.Join(root, "node_modules", ".bin", "vitest")
	version, err := exec.Command(vitest, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "vitest/5.1.1 ") {
		t.Fatalf("expected Vitest 5.1.1, got %q: %v", version, err)
	}
	project := t.TempDir()
	for _, name := range []string{"retry.test.mjs", "pass.test.mjs", "shard.test.mjs"} {
		contents, err := os.ReadFile(filepath.Join("testdata", "vitest", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, name), contents, 0644); err != nil {
			t.Fatal(err)
		}
	}
	report, err := testreport.OpenReports(project, []string{"shard-1.xml", "shard-2.xml"})
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	run := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, vitest, append([]string{"run", "--reporter=junit"}, args...)...)
		command.Dir = project
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("Vitest %v: %v: %s", args, err, output)
		}
	}
	retryPath := filepath.Join(project, "retry.xml")
	run("retry.test.mjs", "--retry=1", "--outputFile="+retryPath)
	retry, err := os.ReadFile(retryPath)
	if err != nil {
		t.Fatal(err)
	}
	if facts := readJUnit(bytes.NewReader(retry)); facts.Counts.Counted != 1 || facts.Failure != "" || len(facts.Unjudged) != 0 {
		t.Fatalf("Vitest retry report did not represent one final result: %+v", facts)
	}
	for shard := 1; shard <= 2; shard++ {
		path := filepath.Join(project, "shard-"+strconv.Itoa(shard)+".xml")
		run("pass.test.mjs", "shard.test.mjs", "--shard="+strconv.Itoa(shard)+"/2", "--outputFile="+path)
	}
	reader, err := New(Options{Kind: JUnit})
	if err != nil {
		t.Fatal(err)
	}
	reader.ReadReport(report)
	verdict := reader.Verdict(End{})
	if verdict.Outcome != Passed || verdict.Counted != 2 || verdict.Skipped != 1 {
		t.Fatalf("complete Vitest shards: %+v", verdict)
	}
	first, err := os.ReadFile(filepath.Join(project, "shard-1.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "shard-2.xml"), first, 0644); err != nil {
		t.Fatal(err)
	}
	reader.ReadReport(report)
	if verdict := reader.Verdict(End{}); verdict.Outcome != Unjudged {
		t.Fatalf("duplicate Vitest shard: %+v", verdict)
	}
	if err := os.Remove(filepath.Join(project, "shard-2.xml")); err != nil {
		t.Fatal(err)
	}
	reader.ReadReport(report)
	if verdict := reader.Verdict(End{}); verdict.Outcome != Unjudged {
		t.Fatalf("missing Vitest shard: %+v", verdict)
	}
}
