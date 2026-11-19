package scopes

import (
	"strings"
	"testing"
)

func selectionSection() map[string]any {
	return map[string]any{
		"judge": "steps",
		"selection": map[string]any{
			"order": []any{"quick", "full", "gate"},
			"rules": []any{
				map[string]any{"glob": "docs/**", "scope": "quick"},
				map[string]any{"glob": "Sources/**", "scope": "full"},
			},
		},
		"scopes": map[string]any{
			"quick": map[string]any{"run": argv("sh", "-c", "echo ran 1 tests"), "evidence": "ran ([0-9]+) tests", "when": "for docs"},
			"full":  map[string]any{"run": argv("sh", "-c", "echo ran 2 tests"), "evidence": "ran ([0-9]+) tests", "when": "for code"},
			"gate":  map[string]any{"run": argv("sh", "-c", "echo ran 3 tests"), "evidence": "ran ([0-9]+) tests", "when": "before merge"},
		},
	}
}

func TestSelectionPlanAndRun(t *testing.T) {
	for _, test := range []struct {
		name       string
		files      map[string]string
		wantScope  string
		wantReason string
	}{
		{"markdown", map[string]string{"docs/guide.md": "guide\n"}, "quick", "docs/** -> quick"},
		{"code", map[string]string{"Sources/a.swift": "let a = 2\n"}, "full", "Sources/** -> full"},
		{"mixed", map[string]string{"docs/guide.md": "guide\n", "Sources/a.swift": "let a = 2\n"}, "full", "docs/** -> quick"},
		{"unknown", map[string]string{"assets/logo.txt": "image\n"}, "gate", "unmatched: assets/logo.txt -> gate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := newWorld(t, selectionSection(), nil)
			w.repo.Commit("change", test.files)
			w.expectStatus(w.jst("plan", "auto", "--base", "main"), 0)
			for _, want := range []string{"selection: scope=" + test.wantScope + " base=main", test.wantReason} {
				if !strings.Contains(w.stdout.String(), want) {
					t.Fatalf("plan missing %q:\n%s", want, w.stdout.String())
				}
			}
			if !strings.Contains(w.stdout.String(), "when: ") {
				t.Fatalf("missing selected scope's when: %s", w.stdout.String())
			}
			if len(w.launches) != 0 {
				t.Fatal("plan ran a command")
			}
			w.expectStatus(w.jst("auto", "--base", "main"), 0)
			w.expectLast("outcome=passed")
			if got := w.lastRecord("run").Text("scope"); got != test.wantScope {
				t.Fatalf("run scope %q, want %q", got, test.wantScope)
			}
		})
	}
}

func TestSelectionCountsBothRenamePathsAndDeletion(t *testing.T) {
	for _, test := range []struct {
		name       string
		change     func(*world)
		wantScope  string
		wantReason string
	}{
		{"rename", func(w *world) { w.repo.Git("mv", "docs/guide.md", "Sources/guide.md"); w.commit() }, "full", "docs/guide.md"},
		{"deletion", func(w *world) { w.repo.Git("rm", "docs/guide.md"); w.commit() }, "quick", "docs/guide.md"},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := newWorld(t, selectionSection(), nil)
			w.repo.Commit("before", map[string]string{"docs/guide.md": "a reusable guide with enough words for a rename\n"})
			base := w.repo.Git("rev-parse", "HEAD")
			test.change(w)
			w.expectStatus(w.jst("plan", "auto", "--base", base), 0)
			if !strings.Contains(w.stdout.String(), "selection: scope="+test.wantScope) || !strings.Contains(w.stdout.String(), test.wantReason) {
				t.Fatalf("unexpected selection: %s", w.stdout.String())
			}
			if test.name == "rename" && !strings.Contains(w.stdout.String(), "Sources/guide.md") {
				t.Fatalf("new name not counted: %s", w.stdout.String())
			}
		})
	}
}

func TestSelectionRequiresBaseAndFallsBackForEmptyRange(t *testing.T) {
	w := newWorld(t, selectionSection(), nil)
	w.expectStatus(w.jst("plan", "auto"), statusRefused)
	w.expectSaid("no known default ref")
	w.expectStatus(w.jst("plan", "auto", "--base", "main"), 0)
	if !strings.Contains(w.stdout.String(), "empty committed range -> gate") {
		t.Fatal(w.stdout.String())
	}
	w.repo.Git("update-ref", "refs/remotes/origin/main", w.repo.Git("rev-parse", "main"))
	w.repo.Git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	w.repo.Commit("docs", map[string]string{"docs/new.md": "new\n"})
	w.expectStatus(w.jst("plan", "auto"), 0)
	if !strings.Contains(w.stdout.String(), "selection: scope=quick base=refs/remotes/origin/main") {
		t.Fatal(w.stdout.String())
	}
}

func TestSelectionIgnoresUncommittedPaths(t *testing.T) {
	w := newWorld(t, selectionSection(), nil)
	w.repo.Commit("docs", map[string]string{"docs/new.md": "new\n"})
	w.repo.Write(map[string]string{"assets/uncommitted.txt": "unknown\n"})
	w.expectStatus(w.jst("plan", "auto", "--base", "main"), 0)
	if !strings.Contains(w.stdout.String(), "selection: scope=quick") {
		t.Fatal(w.stdout.String())
	}
}

func TestSelectionRejectsBadOrderAndPatterns(t *testing.T) {
	for _, test := range []struct {
		change func(map[string]any)
		want   string
	}{
		{func(selection map[string]any) { selection["order"] = []any{"quick", "quick", "gate"} }, "repeats `quick`"},
		{func(selection map[string]any) { selection["order"] = []any{"quick", "full"} }, "must end with `gate`"},
		{func(selection map[string]any) { selection["order"] = []any{"quick", "gate"} }, "outside `test.selection.order`"},
		{func(selection map[string]any) {
			selection["rules"] = []any{map[string]any{"glob": "../docs/**", "scope": "quick"}}
		}, "path segments are not allowed"},
	} {
		section := selectionSection()
		test.change(objectOf(section["selection"]))
		if problems := strings.Join(Problems(section), "; "); !strings.Contains(problems, test.want) {
			t.Errorf("problems %q do not include %q", problems, test.want)
		}
	}
}

func TestSelectionStatusParser(t *testing.T) {
	paths, err := changedPaths([]byte("R100\x00old name\x00new name\x00D\x00gone\x00"))
	if err != nil || strings.Join(paths, ",") != "old name,new name,gone" {
		t.Fatalf("paths=%q error=%v", paths, err)
	}
	if _, err := changedPaths([]byte("R100\x00old\x00")); err == nil {
		t.Fatal("accepted incomplete rename")
	}
}

func TestSelectionMatchesRepeatedDoubleStars(t *testing.T) {
	deepPath := strings.Repeat("nested/", 40) + "result.go"
	for _, test := range []struct {
		name    string
		pattern string
		file    string
		want    bool
	}{
		{"deep match", "**/**/**/result.go", deepPath, true},
		{"deep mismatch", "**/**/**/missing.go", deepPath, false},
		{"adjacent empty segments", "src/**/**/result.go", "src/result.go", true},
		{"segment boundary", "src/**/**/result.go", "sources/result.go", false},
		{"single star stays in one segment", "src/*/result.go", "src/one/two/result.go", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := selectionMatches(test.pattern, test.file); got != test.want {
				t.Fatalf("selectionMatches(%q, %q) = %t, want %t", test.pattern, test.file, got, test.want)
			}
		})
	}
}

func TestSelectionCommitUsesCopy(t *testing.T) {
	w := newWorld(t, selectionSection(), nil)
	w.repo.Commit("docs", map[string]string{"docs/new.md": "new\n"})
	commit := w.repo.Git("rev-parse", "HEAD")
	w.repo.Commit("code", map[string]string{"Sources/a.swift": "let a = 2\n"})
	w.expectStatus(w.jst("auto", "--base", "main", "--commit", commit), 0)
	if !strings.Contains(w.stdout.String(), "selection: scope=quick") {
		t.Fatal(w.stdout.String())
	}
}

func TestChangedSelectionRangeUsesRevisionCopy(t *testing.T) {
	section := selectionSection()
	objectOf(objectOf(section["scopes"])["quick"])["run"] = argv("sh", "-c", `git rev-parse HEAD > "$SIDE/head"; echo ran 1 tests`)
	w := newWorld(t, section, nil)
	w.repo.Commit("docs", map[string]string{"docs/new.md": "new\n"})
	selectedRevision := w.repo.Git("rev-parse", "HEAD")
	w.repo.Commit("code", map[string]string{"Sources/a.swift": "let a = 2\n"})
	w.expectStatus(w.jst("plan", "changed", "main.."+selectedRevision), 0)
	if !strings.Contains(w.stdout.String(), "selection: scope=quick") || len(w.launches) != 0 {
		t.Fatalf("plan selected incorrectly or launched: %s", w.stdout.String())
	}
	if copies := w.copies(); len(copies) != 0 {
		t.Fatalf("plan left copies: %v", copies)
	}
	w.expectStatus(w.jst("changed", "main.."+selectedRevision), 0)
	w.expectLast("outcome=passed")
	if head := w.seen("head"); head != selectedRevision {
		t.Fatalf("ran at %q, want %q", head, selectedRevision)
	}
	if got := w.lastRecord("run").Text("scope"); got != "quick" {
		t.Fatalf("ran scope %q, want quick", got)
	}
	if copies := w.copies(); len(copies) != 0 {
		t.Fatalf("run left copies: %v", copies)
	}
	w.expectStatus(w.jst("changed", "--base", "main"), 0)
	if !strings.Contains(w.stdout.String(), "selection: scope=full") {
		t.Fatalf("no-range alias selected incorrectly: %s", w.stdout.String())
	}
}

func TestChangedSelectionRejectsAmbiguousRange(t *testing.T) {
	w := newWorld(t, selectionSection(), nil)
	w.expectStatus(w.jst("plan", "changed", "main...HEAD"), statusRefused)
	w.expectSaid("range must be BASE..REV")
	w.expectStatus(w.jst("changed", "main..HEAD", "--base", "main"), statusRefused)
	w.expectSaid("without --base or --commit")
}

func TestChangedSelectionUsesTheCommittedConfiguration(t *testing.T) {
	w := newWorld(t, selectionSection(), nil)
	w.repo.Commit("docs", map[string]string{"docs/new.md": "new\n"})
	w.configure(scopesSection(), nil)
	w.expectStatus(w.jst("plan", "changed", "main..HEAD"), 0)
	if !strings.Contains(w.stdout.String(), "selection: scope=quick") {
		t.Fatalf("range used the live configuration: %s", w.stdout.String())
	}
	w.expectStatus(w.jst("changed", "--base", "main", "--commit", "HEAD"), 0)
	if got := w.lastRecord("run").Text("scope"); got != "quick" {
		t.Fatalf("committed selection chose %q, want quick", got)
	}
}

func TestChangedSelectionUsesTheCommittedDefaultRef(t *testing.T) {
	w := newWorld(t, selectionSection(), nil)
	w.repo.Git("update-ref", "refs/remotes/origin/main", w.repo.Git("rev-parse", "main"))
	w.repo.Git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	w.repo.Commit("docs", map[string]string{"docs/new.md": "new\n"})
	w.expectStatus(w.jst("changed", "--commit", "HEAD"), 0)
	if !strings.Contains(w.stdout.String(), "selection: scope=quick base=refs/remotes/origin/main") {
		t.Fatalf("copy lost origin/HEAD: %s", w.stdout.String())
	}
}

func TestNamedChangedScopeWorksWithoutSelection(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("changed"), 0)
	if got := w.lastRecord("run").Text("scope"); got != "changed" {
		t.Fatalf("legacy scope %q, want changed", got)
	}
}
