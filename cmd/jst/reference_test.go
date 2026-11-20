package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
)

// TestReferencesAreCurrent: the command and configuration references in docs/reference are what the
// code makes of them (`just reference`, or this test with -update, writes them anew), so the site
// never says other than the binary.
func TestReferencesAreCurrent(t *testing.T) {
	configuration, err := config.Reference()
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"commands.md":      cli.Reference(),
		"configuration.md": configuration,
	} {
		path, err := filepath.Abs(filepath.Join("..", "..", "docs", "reference", name))
		if err != nil {
			t.Fatal(err)
		}
		if *update {
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path) // #nosec G304 -- a page of this repository
		if err != nil {
			t.Fatalf("%v (just reference writes it)", err)
		}
		if string(want) != content {
			t.Errorf("%s is not what the code makes of it; run just reference and commit the result", path)
		}
	}
}
