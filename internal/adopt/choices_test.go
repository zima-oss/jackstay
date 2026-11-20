package adopt

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/choices"
)

func TestAdoptAsksOnlyWhatTheProjectDoesNotSettle(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "review": {"use": ["claude-code"]}}`})
	options := optionsFor(root)
	var asked []string
	options.Defaults = choices.Defaults{"chat": "on"}
	options.Ask = func(question choices.Question, offered string) (string, error) {
		asked = append(asked, question.ID+"="+offered)
		return "off", nil
	}
	made := apply(t, options)
	if strings.Join(asked, " ") != "chat=on" {
		t.Fatalf("asked %v: the project's reviewer was asked again, or the machine default was not offered", asked)
	}
	if len(made.Choices) != 2 || made.Choices[0].Value != "claude-code" || made.Choices[0].From != "this project's jackstay.json" ||
		made.Choices[1].Value != "off" || made.Choices[1].From != "asked" || !made.Choices[1].Written {
		t.Fatalf("choices %+v", made.Choices)
	}
	settings, err := os.ReadFile(filepath.Join(root, "jackstay.json"))
	if err != nil || !strings.Contains(string(settings), `"enabled": false`) {
		t.Fatalf("the chat answer was not kept: %s %v", settings, err)
	}
	asked = nil
	again := apply(t, options)
	if len(asked) != 0 || again.Choices[1].From != "this project's jackstay.json" {
		t.Fatalf("a second apply asked %v again: %+v", asked, again.Choices)
	}
}

func TestAnOlderPinIsNotAskedAboutChat(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "adopt": {"pin": "v0.5.11"}}`})
	options := optionsFor(root)
	options.Ask = func(question choices.Question, _ string) (string, error) {
		if question.ID == "chat" {
			t.Fatal("chat was asked of a project pinned to 0.5.11")
		}
		return question.Recommended(), nil
	}
	made := apply(t, options)
	if chat := made.Choices[1]; chat.Written || !strings.Contains(chat.From, "pinned to v0.5.11") {
		t.Fatalf("chat %+v", chat)
	}
}

func TestAProfileValueIsNamedAsTheProfiles(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "review": {"use": []}}`})
	made := plan(t, optionsFor(root))
	if made.Choices[0].From != "this project's jackstay.json" || made.Choices[0].Value != "" {
		t.Fatalf("an empty review.use is the project's choice: %+v", made.Choices[0])
	}
}

func TestAdoptWritesAnAnswerGivenAndSaysWhatAppliesWithoutOne(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p"}`})
	options := optionsFor(root)
	options.Answers = map[string]string{"reviewer": "codex,claude-code"}
	made := apply(t, options)
	settings, err := os.ReadFile(filepath.Join(root, "jackstay.json"))
	if err != nil || !strings.Contains(string(settings), `"use": [`) || !strings.Contains(string(settings), `"claude-code"`) {
		t.Fatalf("review.use not written: %s %v", settings, err)
	}
	chat := made.Choices[1]
	if chat.Written || chat.Value != "off" || !strings.Contains(chat.From, "--answer chat=VALUE") {
		t.Fatalf("chat without an answer or a terminal: %+v", chat)
	}
	var text bytes.Buffer
	writeText(&text, made)
	if !strings.Contains(text.String(), "reviewer") || !strings.Contains(text.String(), "review.use = codex,claude-code: --answer") {
		t.Fatalf("the plan does not show the choices:\n%s", &text)
	}
}

func TestAnAnswerOutsideTheOptionsIsAUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--answer", "reviewer=gpt"}, nil, &stdout, &stderr); code != 2 ||
		!strings.Contains(stderr.String(), "codex, claude-code, codex,claude-code") {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
	if code := Run([]string{"--answer", "autonomy=prepare"}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("a question with no key yet was accepted: exit %d", code)
	}
}
