package licences

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The modules of the real jst: each third-party module has its licence collected, and the main
// module and the test-only ones are not among them.
func TestEveryModuleInTheBinaryHasItsLicenceCollected(t *testing.T) {
	modules, err := Modules(context.Background(), "../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) == 0 {
		t.Fatal("no module found")
	}
	out := filepath.Join(t.TempDir(), "licenses")
	written, err := Collect(modules, out)
	if err != nil {
		t.Fatal(err)
	}
	for _, module := range modules {
		if strings.HasPrefix(module.Path, "github.com/zima-oss/jackstay") || module.Path == "google.golang.org/protobuf" {
			t.Errorf("%s is listed", module.Path)
		}
		licence, err := filepath.Glob(filepath.Join(out, filepath.FromSlash(module.Path), "LICENSE*"))
		if err != nil || len(licence) == 0 {
			t.Errorf("%s: no LICENSE collected", module.Path)
		}
	}
	if len(written) < len(modules) {
		t.Fatalf("%d files for %d modules", len(written), len(modules))
	}
}

func TestAModuleWithoutALicenceFileIsRefusedAndNothingIsLeft(t *testing.T) {
	with, without := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(with, "LICENSE.txt"), []byte("MIT\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(without, "README.md"), []byte("no licence\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "licenses")
	written, err := Collect([]Module{{Path: "example.com/a", Version: "v1.0.0", Dir: with}}, out)
	if err != nil || len(written) != 1 || filepath.Base(written[0]) != "LICENSE.txt" {
		t.Fatalf("%v %v", written, err)
	}
	if _, err := Collect([]Module{{Path: "example.com/a", Dir: with}}, out); err == nil || !strings.Contains(err.Error(), "must be a new folder") {
		t.Fatalf("an existing folder: %v", err)
	}
	if _, err := os.Stat(written[0]); err != nil {
		t.Fatalf("an existing folder was changed: %v", err)
	}
	if err := os.RemoveAll(out); err != nil {
		t.Fatal(err)
	}
	_, err = Collect([]Module{{Path: "example.com/a", Dir: with}, {Path: "example.com/b", Version: "v2.0.0", Dir: without}}, out)
	if err == nil || !strings.Contains(err.Error(), "example.com/b v2.0.0 has no licence file") {
		t.Fatalf("%v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("left %s: %v", out, statErr)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {""}, {"--help"}, {"a", "b"}} {
		if code := Main(args, &strings.Builder{}, &strings.Builder{}); code != 2 {
			t.Errorf("%q: %d", args, code)
		}
	}
}
