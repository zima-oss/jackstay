//go:build integration

package judge

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/supervise"
)

func TestGoJSONReadsARealGoTestInvocation(t *testing.T) {
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "go.mod"), []byte("module example.invalid/fixture\n\ngo 1.27.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := `package fixture

import "testing"

func TestParent(t *testing.T) {
	t.Run("child", func(t *testing.T) {})
	t.Run("part/whole", func(t *testing.T) {})
	t.Run("skipped", func(t *testing.T) { t.Skip("fixture skip") })
}
`
	if err := os.WriteFile(filepath.Join(folder, "fixture_test.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-json", "-count=1", ".")
	command.Dir = folder
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("running go test -json: %v", err)
	}
	var reader goJSONReader
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		reader.line(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	facts := reader.facts()
	if facts.Counts.Counted != 2 || facts.Counts.Skipped != 1 || len(facts.Unjudged) != 0 ||
		Evaluate(facts, End{}, ScopePolicy).Outcome != Passed {
		t.Fatalf("real go test -json evidence: %+v", facts)
	}
}

func TestGoJSONSelectedSubtestMatchesARealGoInvocation(t *testing.T) {
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "go.mod"), []byte("module example.invalid/fixture\n\ngo 1.27.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := `package fixture

import "testing"

func TestParent(t *testing.T) {
	t.Run("child", func(t *testing.T) {})
	t.Run("other", func(t *testing.T) {})
}
`
	if err := os.WriteFile(filepath.Join(folder, "fixture_test.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, trial := range []struct {
		name string
		want Outcome
	}{
		{"TestParent/child", Passed},
		{"TestParent/missing", NothingRun},
	} {
		t.Run(trial.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			command := exec.CommandContext(ctx, "go", "test", "-json", "-count=1", "-run", trial.name, ".")
			command.Dir = folder
			command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=")
			output, err := command.Output()
			if err != nil {
				t.Fatalf("running filtered go test -json: %v", err)
			}
			reader := goSelectedReader(t, GoSelection{Package: "example.invalid/fixture", Name: trial.name}, true)
			scanner := bufio.NewScanner(strings.NewReader(string(output)))
			for scanner.Scan() {
				if err := reader.Line(supervise.Stdout, scanner.Text()); err != nil {
					t.Fatal(err)
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if verdict := reader.Verdict(End{}); verdict.Outcome != trial.want {
				t.Fatalf("selected result: %+v", verdict)
			}
			samples, truncated := reader.GoDurations()
			wantSamples := 0
			if trial.want == Passed {
				wantSamples = 1
			}
			if truncated || len(samples) != wantSamples {
				t.Fatalf("selected timing: %+v, truncated %t", samples, truncated)
			}
		})
	}
}

func TestGoJSONReadsARealRunWithAttributesAndArtifacts(t *testing.T) {
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "go.mod"), []byte("module example.invalid/fixture\n\ngo 1.27.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := `package fixture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAttr(t *testing.T) {
	t.Attr("issue", "fixture")
	t.Run("child", func(t *testing.T) { t.Attr("kind", "leaf") })
}

func TestArtifact(t *testing.T) {
	if err := os.WriteFile(filepath.Join(t.ArtifactDir(), "out.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}
`
	if err := os.WriteFile(filepath.Join(folder, "fixture_test.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	outputs := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-json", "-count=1", "-artifacts", "-outputdir", outputs, ".")
	command.Dir = folder
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("running go test -json -artifacts: %v", err)
	}
	for _, action := range []string{`"Action":"attr"`, `"Action":"artifacts"`} {
		if !strings.Contains(string(output), action) {
			t.Fatalf("this Go did not emit %s: %s", action, output)
		}
	}
	var reader goJSONReader
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		reader.line(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	facts := reader.facts()
	if facts.Counts.Counted != 2 || len(facts.Unjudged) != 0 || Evaluate(facts, End{}, ScopePolicy).Outcome != Passed {
		t.Fatalf("real go test -json with attributes and artifacts: %+v", facts)
	}
}

func TestGoJSONRefusesARealCachedCoverRun(t *testing.T) {
	folder := t.TempDir()
	files := map[string]string{
		"go.mod":          "module example.invalid/fixture\n\ngo 1.27.0\n",
		"fixture.go":      "package fixture\n\nfunc Add(a, b int) int { return a + b }\n",
		"fixture_test.go": "package fixture\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"add\")\n\t}\n}\n",
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func() goJSONReader {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, "go", "test", "-json", "-cover", ".")
		command.Dir = folder
		command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOFLAGS=")
		output, err := command.Output()
		if err != nil {
			t.Fatalf("running go test -json -cover: %v", err)
		}
		var reader goJSONReader
		scanner := bufio.NewScanner(strings.NewReader(string(output)))
		for scanner.Scan() {
			reader.line(scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		return reader
	}
	run()
	second := run()
	if !second.cached {
		t.Fatal("the second run was not seen as replayed from Go's cache")
	}
	if verdict := Evaluate(second.facts(), End{}, ScopePolicy); verdict.Outcome != Unjudged || !strings.Contains(verdict.Why, "cache") {
		t.Fatalf("a cached -cover run was judged: %+v", verdict)
	}
}
