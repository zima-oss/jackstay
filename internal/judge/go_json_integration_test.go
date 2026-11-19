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
	if facts.Counts.Counted != 1 || facts.Counts.Skipped != 1 || len(facts.Unjudged) != 0 ||
		Evaluate(facts, End{}, ScopePolicy).Outcome != Passed {
		t.Fatalf("real go test -json evidence: %+v", facts)
	}
}
