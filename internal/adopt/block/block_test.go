package block

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func everyCommand(string) bool { return true }

var everySection = map[string]any{
	"jackstay_schema": 2, "project": "sample",
	"lock":     map[string]any{"enabled": true},
	"guard":    map[string]any{"rules": map[string]any{}},
	"github":   map[string]any{"account": "someone"},
	"cards":    map[string]any{"branch": "trunk"},
	"ids":      map[string]any{"S": map[string]any{}},
	"records":  map[string]any{"S": map[string]any{}},
	"changes":  map[string]any{},
	"release":  map[string]any{},
	"review":   map[string]any{},
	"receipts": map[string]any{},
	"adopt":    map[string]any{},
	"chat":     map[string]any{"enabled": true},
	"queue":    map[string]any{"enabled": true},
	"test": map[string]any{"scopes": map[string]any{"gate": map[string]any{}, "quick": map[string]any{}},
		"selection": map[string]any{"order": []string{"quick", "gate"}}},
}

func TestTheBlockGolden(t *testing.T) {
	cases := map[string]Inputs{
		"every-section": {Config: everySection, Has: everyCommand},
		"no-section":    {Config: map[string]any{"jackstay_schema": 1, "project": "bare"}, Has: everyCommand},
		"scopes-no-gate": {Config: map[string]any{"test": map[string]any{"scopes": map[string]any{"quick": map[string]any{}}}},
			Has: everyCommand},
		"profile": {Config: everySection, Has: everyCommand,
			Rules: []string{"`just gate` is the gate.", "Ask before merging."}, LeaveOut: []string{"owner", "review"}},
	}
	for name, inputs := range cases {
		t.Run(name, func(t *testing.T) {
			got := Render(inputs)
			path := filepath.Join("testdata", name+".md")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path) // #nosec G304 -- the test's own golden file
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("%s differs from the golden file (go test -run TestTheBlockGolden -update):\n%s", path, got)
			}
		})
	}
}

func TestAnItemNamingACommandThisJackstayLacksIsLeftOut(t *testing.T) {
	withoutLog := func(command string) bool { return command != "log" }
	if text := Render(Inputs{Config: everySection, Has: withoutLog}); strings.Contains(text, "jst log") {
		t.Fatal("the block names `jst log`, which this jackstay does not have")
	}
	if text := Render(Inputs{Config: everySection, Has: everyCommand}); !strings.Contains(text, "jst log") {
		t.Fatal("with `jst log` and `jst stats` present, their item is missing")
	}
}

func TestEveryItemIsPrintedForSomeConfig(t *testing.T) {
	text := Render(Inputs{Config: everySection, Has: everyCommand})
	for _, word := range []string{"jst test gate", "jst wait", "jst bg", "jst lock", "jst start", "jst discard", "The guard",
		"jst gh", "jst review", "origin/trunk..HEAD", "jst id next", "jst new", "jst changes new", "jst release prepare",
		"jst log", "jst timeline", "jst inventory", "jst receipts export", "jst profile export", "jst owner", "jst adopt --plan",
		"jst setup --plan", "jst setup --chat", "jst queue handoff", "jst test plan changed"} {
		if !strings.Contains(text, word) {
			t.Errorf("the block lacks %q", word)
		}
	}
	if len(ItemIDs()) != len(items) {
		t.Fatal("ItemIDs")
	}
}

func lines(text string) []string { return strings.Split(text, "\n") }

func TestLocateFindsAMarkedBlockAndWhetherItWasEdited(t *testing.T) {
	body := "## jackstay\n\n- an item\n"
	text := "# Title\n\nintro\n\n" + Marked(body) + "\n## Next\n"
	found := Locate(lines(text))
	if found.Kind != Current || !found.Intact || found.Body != body || found.Start != 4 || found.Stop != 8 {
		t.Fatalf("got %+v", found)
	}
	edited := strings.Replace(text, "an item", "an item, edited", 1)
	if found := Locate(lines(edited)); found.Kind != Current || found.Intact {
		t.Fatalf("an edit was not seen: %+v", found)
	}
}

func TestLocateRefusesMarkersThatMakeNoBlock(t *testing.T) {
	body := "x\n"
	for name, text := range map[string]string{
		"two begins":   Begin(body) + "\n" + Begin(body) + "\n" + End + "\n",
		"end first":    End + "\n" + Begin(body) + "\n",
		"begin no end": Begin(body) + "\nx\n",
	} {
		if found := Locate(lines(text)); found.Kind != Malformed {
			t.Errorf("%s: got %+v", name, found)
		}
	}
	if found := Locate(lines("# nothing here\n")); found.Kind != None {
		t.Fatalf("got %+v", found)
	}
}
