package sourceview

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectsGitMetadataAndTraversal(t *testing.T) {
	for _, path := range []string{"../escape", ".git/config", "nested/.GIT/config", "nested/../../escape", "nested//file"} {
		record := []byte("100644 blob " + strings.Repeat("a", 40) + "\t" + path)
		if _, err := parseEntry(record); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}

func TestMissingBlobReportsFetchAction(t *testing.T) {
	item := entry{oid: strings.Repeat("a", 40), path: "src/example.go"}
	_, err := blobSize(item, item.oid+" missing\n")
	if err == nil || !strings.Contains(err.Error(), item.path) || !strings.Contains(err.Error(), "partial clone") || !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("missing blob diagnosis: %v", err)
	}
}

func TestCollidingSourcePathReportsCollision(t *testing.T) {
	destination := t.TempDir()
	if err := writeBlob(destination, "README", strings.NewReader("first"), 5, make(map[string]bool)); err != nil {
		t.Fatal(err)
	}
	err := writeBlob(destination, "README", strings.NewReader("second"), 6, make(map[string]bool))
	if err == nil || !errors.Is(err, os.ErrExist) || !strings.Contains(err.Error(), "README") || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("colliding path diagnosis: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(destination, "README"))
	if err != nil || string(first) != "first" {
		t.Fatalf("first path was replaced: %q, %v", first, err)
	}
}

func TestCollidingSourceDirectoryIsReported(t *testing.T) {
	destination := t.TempDir()
	directories := make(map[string]bool)
	if err := writeBlob(destination, "Dir/first", strings.NewReader("first"), 5, directories); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "dir")); os.IsNotExist(err) {
		t.Skip("filesystem distinguishes directory case")
	}
	err := writeBlob(destination, "dir/second", strings.NewReader("second"), 6, directories)
	if err == nil || !strings.Contains(err.Error(), "dir") || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("colliding directory diagnosis: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "Dir/second")); !os.IsNotExist(err) {
		t.Fatalf("colliding file was projected: %v", err)
	}
}
