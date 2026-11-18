package branch

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantPos  []string
		wantFlag bool
		wantFrom string
		wantErr  string
		help     bool
	}{
		{name: "nothing", args: nil},
		{name: "flags anywhere", args: []string{"x", "--no-fetch", "--from", "v1"}, wantPos: []string{"x"}, wantFlag: true, wantFrom: "v1"},
		{name: "equals form", args: []string{"--from=origin/dev", "x"}, wantPos: []string{"x"}, wantFrom: "origin/dev"},
		{name: "after --", args: []string{"--", "--no-fetch"}, wantPos: []string{"--no-fetch"}},
		{name: "unknown", args: []string{"--rebase"}, wantErr: "unknown option"},
		{name: "a value missing", args: []string{"--from"}, wantErr: "needs a value"},
		{name: "a switch with a value", args: []string{"--no-fetch=1"}, wantErr: "unknown option"},
		{name: "help", args: []string{"x", "-h"}, help: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var noFetch bool
			var from string
			got, err := parseArgs(c.args, map[string]*bool{"--no-fetch": &noFetch}, map[string]*string{"--from": &from})
			switch {
			case c.help:
				if !errors.Is(err, errHelp) {
					t.Fatalf("got %v, want errHelp", err)
				}
			case c.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("got %v, want %q", err, c.wantErr)
				}
			case err != nil || !slices.Equal(got, c.wantPos) || noFetch != c.wantFlag || from != c.wantFrom:
				t.Fatalf("got %q %v %v %q", got, err, noFetch, from)
			}
		})
	}
}

func TestStatusPaths(t *testing.T) {
	status := []byte(" M a.txt\x00R  new name\x00old name\x00?? \tloose\x00A  added\x00")
	want := []string{"a.txt", "new name", "\tloose", "added"}
	if got := statusPaths(status); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHiddenPaths(t *testing.T) {
	entries := []byte("H kept\x00h assumed\x00S skipped\x00s both\x00M x\x00")
	if got, want := hiddenPaths(entries, false), []string{"assumed", "skipped", "both"}; !slices.Equal(got, want) {
		t.Fatalf("not sparse: got %q, want %q", got, want)
	}
	if got, want := hiddenPaths(entries, true), []string{"assumed", "both"}; !slices.Equal(got, want) {
		t.Fatalf("sparse: got %q, want %q", got, want)
	}
}

func TestSlug(t *testing.T) {
	for name, want := range map[string]string{"feat/new-thing": "feat-new-thing", "x": "x", "-.a b.-": "a-b", "///": "work",
		"v1.2_rc": "v1.2_rc"} {
		if got := slug(name); got != want {
			t.Errorf("slug(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestListed(t *testing.T) {
	if got := listed([]string{" .env", "\t.env"}); got != `" .env", "\t.env"` {
		t.Fatalf("got %s", got)
	}
	if got := listed([]string{"a", "b", "c", "d", "e", "f", "g"}); !strings.HasSuffix(got, "and 2 more") {
		t.Fatalf("got %s", got)
	}
}

func TestObstacles(t *testing.T) {
	root := t.TempDir()
	write := func(name string) {
		place := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(place), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(place, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".env")         // ignored, arrives as a file
	write(" .env")        // a name beginning with a space
	write("tracked.txt")  // tracked here and arriving (not possible after a clean diff, but harmless)
	write("notes")        // an ignored file where a folder arrives
	write("dir/kept")     // a tracked folder becoming a file: tracked content only
	write("held/.secret") // a folder becoming a file, holding an untracked file
	write("held/tracked") // …and a tracked one
	write("trackedfile")  // a tracked file where a folder arrives
	if err := os.Symlink("elsewhere", filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	tracked := map[string]bool{"tracked.txt": true, "dir/kept": true, "held/tracked": true, "trackedfile": true}
	added := []string{".env", " .env", "tracked.txt", "notes/x", "dir", "held", "trackedfile/y", "linked/z", "linked", "fresh/new", "free"}
	got := obstacles(root, added, tracked)
	want := []string{".env", " .env", "notes/x", "held/.secret", "linked/z", "linked"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
