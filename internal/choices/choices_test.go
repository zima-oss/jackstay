package choices

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/adopt/jsondoc"
)

func TestEveryQuestionRecommendsItsFirstOptionAndMarksIt(t *testing.T) {
	for _, question := range append(All(), ScopeQuestions([]string{"x-go"})...) {
		if len(question.Options) < 2 || !strings.Contains(question.Options[0].Label, "recommended") {
			t.Errorf("%s: the first option is not the marked recommendation: %+v", question.ID, question.Options)
		}
		if question.ID == "" || question.Text == "" {
			t.Errorf("a question without an ID or text: %+v", question)
		}
	}
	if Reviewer.Recommended() != "codex" || Autonomy.Recommended() != "merge-when-green" || Chat.Recommended() != "off" {
		t.Fatal("a default other than the owner's (W-090, W-091)")
	}
}

func TestTheTerminalAskerTakesEnterANumberOrAValue(t *testing.T) {
	for input, want := range map[string]string{"\n": "claude-code", "1\n": "codex", "nonsense\n3\n": "codex,claude-code", "codex\n": "codex"} {
		var shown bytes.Buffer
		asker := TerminalAsker{In: bufio.NewReader(strings.NewReader(input)), Out: &shown}
		got, err := asker.Ask(Reviewer, "claude-code")
		if err != nil || got != want {
			t.Errorf("%q: %q %v", input, got, err)
		}
		if !strings.Contains(shown.String(), "* 2. Claude Code") {
			t.Errorf("the offered answer is not marked:\n%s", &shown)
		}
	}
	asker := TerminalAsker{In: bufio.NewReader(strings.NewReader("")), Out: &bytes.Buffer{}}
	if _, err := asker.Ask(Reviewer, ""); err != ErrNoAnswer {
		t.Fatalf("an ended input: %v", err)
	}
}

func TestOfferedPrefersTheProjectThenTheMachine(t *testing.T) {
	if value, _ := Offered(Reviewer, "claude-code", true, Defaults{"reviewer": "codex,claude-code"}); value != "claude-code" {
		t.Fatal(value)
	}
	if value, _ := Offered(Reviewer, "", false, Defaults{"reviewer": "codex,claude-code"}); value != "codex,claude-code" {
		t.Fatal(value)
	}
	if value, from := Offered(Reviewer, "", false, nil); value != "codex" || from != "recommended" {
		t.Fatal(value, from)
	}
}

func TestMachineDefaultsKeepOnlyKnownAnswers(t *testing.T) {
	store := FileStore{Path: filepath.Join(t.TempDir(), ".all", "choices.json")}
	if loaded, err := store.Load(); err != nil || len(loaded) != 0 {
		t.Fatalf("no file: %v %v", loaded, err)
	}
	if err := store.Save(Defaults{"reviewer": "claude-code", "autonomy": "prepare"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path, []byte(`{"reviewer":"claude-code","autonomy":"prepare","chat":"maybe","old":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded) != 2 || loaded["reviewer"] != "claude-code" || loaded["autonomy"] != "prepare" {
		t.Fatalf("loaded %v %v", loaded, err)
	}
}

func TestParseAnswer(t *testing.T) {
	if id, value, err := ParseAnswer("reviewer=claude-code", All()); err != nil || id != "reviewer" || value != "claude-code" {
		t.Fatal(id, value, err)
	}
	for _, bad := range []string{"reviewer", "reviewer=", "reviewer=gpt", "colour=red"} {
		if _, _, err := ParseAnswer(bad, All()); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestKeysReadAndWriteTheirConfiguration(t *testing.T) {
	parsed, err := jsondoc.Parse([]byte(`{"project":"p","review":{"brief":"b"}}`))
	if err != nil {
		t.Fatal(err)
	}
	document := parsed.(*jsondoc.Object)
	if _, set := Reviewer.Key.Read(document); set {
		t.Fatal("review.use read from a file without it")
	}
	Reviewer.Key.Write(document, "codex,claude-code")
	Chat.Key.Write(document, "off")
	if value, set := Reviewer.Key.Read(document); !set || value != "codex,claude-code" {
		t.Fatal(value)
	}
	if _, set := Chat.Key.Read(document); set {
		t.Fatal("answering off wrote a chat section")
	}
	Chat.Key.Write(document, "on")
	text := string(jsondoc.Format(document))
	if !strings.Contains(text, `"use": [`) || !strings.Contains(text, `"enabled": true`) ||
		strings.Index(text, `"brief"`) > strings.Index(text, `"use"`) {
		t.Fatalf("written:\n%s", text)
	}
}
