package expected

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestReadAndChange(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	if names, err := Read(); err != nil || names != nil {
		t.Fatalf("no list: %v %v", names, err)
	}
	var wait sync.WaitGroup
	for _, name := range []string{"widgets", "storefront", "jackstay", "^translator"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := Change(func(names map[string]bool) { names[name] = true }); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	names, err := Read()
	if err != nil || strings.Join(names, ",") != "^translator,jackstay,storefront,widgets" {
		t.Fatalf("after four changes at once: %v %v", names, err)
	}
	if _, err := Change(func(names map[string]bool) { delete(names, "jackstay") }); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	data, _ := os.ReadFile(path)
	if string(data) != "^translator\nstorefront\nwidgets\n" {
		t.Fatalf("file %q", data)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.partial"))
	if len(leftovers) > 0 {
		t.Fatalf("left %v", leftovers)
	}
}

func TestUnreadableListIsAnError(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	path, _ := Path()
	if err := os.MkdirAll(path, 0o700); err != nil { // a folder where the list should be
		t.Fatal(err)
	}
	if _, err := Read(); err == nil {
		t.Fatal("a list that cannot be read was taken for an empty one")
	}
}
