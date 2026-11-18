package cleancopy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/pinned"
)

func TestASweepRemovesOnlyCopiesWhoseOwnerIsGone(t *testing.T) {
	copies := t.TempDir()
	for name, owner := range map[string]string{"dead": OwnerPrefix + "999999 1", "live": pinned.OwnWords(OwnerPrefix)} {
		if err := os.MkdirAll(filepath.Join(copies, name, "repo"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(copies, name, "owner"), []byte(owner), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(copies, "orphan"), 0o700); err != nil {
		t.Fatal(err)
	}
	removed := Sweep(copies, time.Now(), func(words string) bool { return pinned.Alive(OwnerPrefix, words) })
	if len(removed) != 1 || filepath.Base(removed[0]) != "dead" {
		t.Fatalf("removed %v", removed)
	}
	removed = Sweep(copies, time.Now().Add(2*time.Hour), func(string) bool { return true })
	if len(removed) != 1 || filepath.Base(removed[0]) != "orphan" {
		t.Fatalf("an old folder with no owner: removed %v", removed)
	}
}
