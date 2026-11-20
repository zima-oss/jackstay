//go:build integration

package judge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// SUREFIRE_MAVEN points to Maven 3.9.11. Its local repository may be set with
// SUREFIRE_MAVEN_REPOSITORY. The ordinary Go gate uses checked-in XML fixtures.
func TestSurefireJUnitProducer(t *testing.T) {
	maven := os.Getenv("SUREFIRE_MAVEN")
	if maven == "" {
		t.Skip("set SUREFIRE_MAVEN to Maven 3.9.11")
	}
	version, err := exec.Command(maven, "-version").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "Apache Maven 3.9.11") {
		t.Fatalf("expected Maven 3.9.11, got %q: %v", version, err)
	}
	for _, sample := range []struct {
		name, selection  string
		empty            bool
		status           int
		reports          []string
		counted, skipped int
		retried          int
		outcome          Outcome
		unjudged         bool
		// rerun runs the JUnit 5 project with surefire.rerunFailingTestsCount=1.
		rerun bool
	}{
		{name: "pass", selection: "PassingTest", reports: []string{"PassingTest"}, counted: 1, skipped: 1, outcome: Passed},
		{name: "fail", selection: "FailingTest", status: 1, reports: []string{"FailingTest"}, counted: 1, outcome: Failed},
		{name: "skipped", selection: "SkippedTest", reports: []string{"SkippedTest"}, skipped: 1, outcome: NothingRun},
		{name: "multiple", reports: []string{"PassingTest-pass", "SkippedTest-skip"}, counted: 1, skipped: 2, outcome: Passed},
		{name: "retry", selection: "RetryTest", reports: []string{"RetryTest"}, unjudged: true, outcome: Unjudged},
		{name: "no-match", selection: "DoesNotExist", empty: true},
		{name: "passed-on-rerun", selection: "FlakyTest", reports: []string{"FlakyTest"}, counted: 2, retried: 1,
			outcome: PassedOnRetry, rerun: true},
		{name: "failed-on-rerun", selection: "AlwaysFailingTest", status: 1, reports: []string{"AlwaysFailingTest"},
			counted: 1, outcome: Failed, rerun: true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			project := t.TempDir()
			source := "project"
			if sample.rerun {
				source = "rerun-project"
			}
			if err := os.CopyFS(project, os.DirFS(filepath.Join("testdata", "surefire", source))); err != nil {
				t.Fatal(err)
			}
			selections := []string{sample.selection}
			if sample.name == "multiple" {
				selections = []string{"PassingTest:pass", "SkippedTest:skip"}
			}
			for _, selection := range selections {
				class, suffix, _ := strings.Cut(selection, ":")
				arguments := []string{"-B", "-ntp", "-f", filepath.Join(project, "pom.xml"), "test", "-Dtest=" + class}
				if repository := os.Getenv("SUREFIRE_MAVEN_REPOSITORY"); repository != "" {
					arguments = append(arguments, "-Dmaven.repo.local="+repository)
				}
				if suffix != "" {
					arguments = append(arguments, "-Dsurefire.reportNameSuffix="+suffix)
				}
				if sample.empty {
					arguments = append(arguments, "-Dsurefire.failIfNoSpecifiedTests=false")
				}
				if sample.rerun {
					arguments = append(arguments, "-Dsurefire.rerunFailingTestsCount=1")
				}
				context, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				output, err := exec.CommandContext(context, maven, arguments...).CombinedOutput()
				cancel()
				status := 0
				if err != nil {
					var exit *exec.ExitError
					if !errors.As(err, &exit) {
						t.Fatalf("Maven could not run: %v: %s", err, output)
					}
					status = exit.ExitCode()
				}
				if status != sample.status {
					t.Fatalf("Maven exit %d, want %d: %s", status, sample.status, output)
				}
			}
			reportDirectory := filepath.Join(project, "target", "surefire-reports")
			matches, err := filepath.Glob(filepath.Join(reportDirectory, "TEST-*.xml"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != len(sample.reports) {
				t.Fatalf("Maven wrote %d XML reports, want %d: %v", len(matches), len(sample.reports), matches)
			}
			if sample.empty {
				return // A successful no-match run writes no XML and cannot prove test execution.
			}
			var total Facts
			total.Kind = JUnit
			total.Counts.Counting = true
			for _, class := range sample.reports {
				path := filepath.Join(reportDirectory, "TEST-dev.jackstay.fixtures."+class+".xml")
				if !slices.Contains(matches, path) {
					t.Fatalf("Maven omitted %s: %v", path, matches)
				}
				report, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				facts := readJUnit(report)
				report.Close()
				if sample.unjudged {
					if len(facts.Unjudged) != 1 || facts.Unjudged[0] != "the JUnit XML report repeats a test case" ||
						Evaluate(facts, End{Status: sample.status}, ScopePolicy).Outcome != Unjudged {
						t.Fatalf("Surefire retry report should remain unjudged: %+v", facts)
					}
					continue
				}
				if len(facts.Unjudged) != 0 {
					t.Fatalf("Surefire report %s was unjudged: %+v", class, facts)
				}
				total.Counts.Counted += facts.Counts.Counted
				total.Counts.Skipped += facts.Counts.Skipped
				total.Retried += facts.Retried
				if facts.Failure != "" {
					total.Failure = facts.Failure
				}
			}
			if sample.unjudged {
				return
			}
			if total.Counts.Counted != sample.counted || total.Counts.Skipped != sample.skipped || total.Retried != sample.retried {
				t.Fatalf("Surefire facts %+v, want counted=%d skipped=%d retried=%d", total, sample.counted, sample.skipped, sample.retried)
			}
			if verdict := Evaluate(total, End{Status: sample.status}, ScopePolicy); verdict.Outcome != sample.outcome {
				t.Fatalf("Surefire verdict %+v", verdict)
			}
		})
	}
}
