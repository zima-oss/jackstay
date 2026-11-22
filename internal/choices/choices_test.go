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
	for _, question := range All() {
		if len(question.Options) < 2 || !strings.Contains(question.Options[0].Label, "recommended") {
			t.Errorf("%s: the first option is not the marked recommendation: %+v", question.ID, question.Options)
		}
		if question.ID == "" || question.Text == "" {
			t.Errorf("a question without an ID or text: %+v", question)
		}
	}
	if Reviewer.Recommended() != "codex" || Chat.Recommended() != "off" {
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

func TestMachineAnswersShareSetupJSONWithLandAutonomy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	if kept, err := MachineAnswers(); err != nil || len(kept) != 0 {
		t.Fatalf("no file: %v %v", kept, err)
	}
	path := filepath.Join(home, ".all", MachineFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"land_autonomy": "prepare", "chat": "on", "reviewer": "gpt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if kept, err := MachineAnswers(); err != nil || len(kept) != 0 {
		t.Fatalf("an unknown reviewer or a chat answer was offered: %v %v", kept, err)
	}
	if err := SaveMachineAnswers(Defaults{"reviewer": "claude-code"}); err != nil {
		t.Fatal(err)
	}
	kept, err := MachineAnswers()
	if err != nil || len(kept) != 1 || kept["reviewer"] != "claude-code" {
		t.Fatalf("kept %v %v", kept, err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `"land_autonomy": "prepare"`) {
		t.Fatalf("the autonomy answer was lost: %s", data)
	}
}

func TestCheckNamesTheAnswers(t *testing.T) {
	if Reviewer.Check("claude-code") != nil || Chat.Check("on") != nil {
		t.Fatal("a listed answer refused")
	}
	if err := Reviewer.Check("gpt"); err == nil || !strings.Contains(err.Error(), "--reviewer takes one of: codex, claude-code, codex,claude-code") {
		t.Fatalf("%v", err)
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
	if value, set := Reviewer.Key.Read(document); !set || value != "codex,claude-code" {
		t.Fatal(value)
	}
	Chat.Key.Write(document, "on")
	text := string(jsondoc.Format(document))
	if !strings.Contains(text, `"use": [`) || !strings.Contains(text, `"enabled": true`) ||
		strings.Index(text, `"brief"`) > strings.Index(text, `"use"`) {
		t.Fatalf("written:\n%s", text)
	}
}

func TestAnEmptyReviewerListIsTheProjectsChoice(t *testing.T) {
	parsed, err := jsondoc.Parse([]byte(`{"review":{"use":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if value, set := Reviewer.Key.Read(parsed.(*jsondoc.Object)); !set || value != "" {
		t.Fatalf("an empty review.use: %q %v", value, set)
	}
}

func TestChatOffIsKeptAndTurnsOffTheIntegrations(t *testing.T) {
	parsed, err := jsondoc.Parse([]byte(`{"chat":{"enabled":true,"codex_bridge":{"enabled":true,"socket":"s"},"claude_code":{"enabled":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	document := parsed.(*jsondoc.Object)
	Chat.Key.Write(document, "off")
	if value, set := Chat.Key.Read(document); !set || value != "off" {
		t.Fatalf("chat off not kept: %q %v", value, set)
	}
	text := string(jsondoc.Format(document))
	if strings.Contains(text, `"enabled": true`) || !strings.Contains(text, `"socket": "s"`) {
		t.Fatalf("written:\n%s", text)
	}
}
