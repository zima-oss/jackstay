package guard

import (
	"bufio"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// The corpus (tests/guard/corpus.jsonl) is the authority for the guard (guard.md section
// 8): each case is a command line and the decision the guard owes it, with what git answers
// where the decision depends on the repository's state.

type corpusCase struct {
	ID          string                     `json:"id"`
	Source      string                     `json:"source"`
	Line        string                     `json:"line"`
	Cwd         *string                    `json:"cwd"`
	Env         map[string]string          `json:"env"`
	Scratch     json.RawMessage            `json:"scratch"`
	Guard       map[string]any             `json:"guard"`
	ProjectRoot string                     `json:"project_root"`
	Answers     map[string]json.RawMessage `json:"answers"`
	Expect      struct {
		Rules   *[]string `json:"rules"`
		Has     string    `json:"has"`
		Refused bool      `json:"refused"`
	} `json:"expect"`
	Warnings []string `json:"warnings"`
	Fix      string   `json:"fix"`
}

type injected struct {
	Default json.RawMessage            `json:"default"`
	By      map[string]json.RawMessage `json:"by"`
}

// corpusGit answers git's questions as a case says, with these defaults: another
// ref holds nothing, no branch exists, no merge is under way, git cannot say what is uncommitted.
type corpusGit struct {
	*Git
	answers map[string]injected
}

func (git corpusGit) pick(kind, key string) (json.RawMessage, bool) {
	given, ok := git.answers[kind]
	if !ok {
		return nil, false
	}
	if found, ok := given.By[key]; ok {
		return found, true
	}
	return given.Default, true
}

func triOf(raw json.RawMessage, fallback answer) answer {
	switch strings.Trim(string(raw), `"`) {
	case "yes":
		return answerYes
	case "no":
		return answerNo
	case "unsure":
		return answerUnsure
	}
	return fallback
}

func (git corpusGit) Held(folder, branch string) answer {
	raw, _ := git.pick("held", branch)
	return triOf(raw, answerNo)
}

func (git corpusGit) BranchExists(folder, name string) answer {
	raw, _ := git.pick("branch", name)
	return triOf(raw, answerNo)
}

func (git corpusGit) Conflicts(folder string) bool {
	raw, _ := git.pick("conflicts", folder)
	return string(raw) == "true"
}

func listOf(raw json.RawMessage, ok bool, fallback []string, fallbackOK bool) ([]string, bool) {
	if !ok {
		return fallback, fallbackOK
	}
	if string(raw) == "null" {
		return nil, false
	}
	var found []string
	_ = json.Unmarshal(raw, &found)
	if found == nil {
		found = []string{}
	}
	return found, true
}

func (git corpusGit) Unrelated(folder string, paths []string) ([]string, bool) {
	raw, ok := git.pick("unrelated", strings.Join(paths, " "))
	return listOf(raw, ok, []string{}, true)
}

func (git corpusGit) Changed(folder string, paths []string, mode string) ([]string, bool) {
	raw, ok := git.pick("changed", strings.Join(paths, " "))
	return listOf(raw, ok, nil, false)
}

func (git corpusGit) AddsNothing(folder, branch string) bool {
	raw, _ := git.pick("adds_nothing", branch)
	return string(raw) == "true"
}

func readCorpus(t *testing.T) []corpusCase {
	t.Helper()
	file, err := os.Open("../../tests/guard/corpus.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var cases []corpusCase
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
	for scanner.Scan() {
		var one corpusCase
		if err := json.Unmarshal(scanner.Bytes(), &one); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, one)
	}
	return cases
}

func settingsOf(one corpusCase) Settings {
	folder := "/Users/someone/project"
	if one.Cwd != nil {
		folder = *one.Cwd
	}
	env := map[string]string{"TMPDIR": "/var/folders/ab/cdef/T/"}
	if one.Env != nil {
		env = one.Env
	}
	scratch := []string{"/tmp", "/var/folders/ab/cdef/T/"}
	if string(one.Scratch) == "null" {
		scratch = nil
	} else if len(one.Scratch) > 0 {
		_ = json.Unmarshal(one.Scratch, &scratch)
	}
	answers := map[string]injected{}
	for kind, raw := range one.Answers {
		var given injected
		_ = json.Unmarshal(raw, &given)
		answers[kind] = given
	}
	root := one.ProjectRoot
	if root == "" && one.Guard != nil {
		root = folder
	}
	// The corpus checks what the guard decides, not how fast it reads: a generous budget keeps a
	// loaded machine from turning a case into `too-long`. TestTheReadingDeadline covers the bound.
	return Settings{Folder: folder, Environ: env, Scratch: scratch, Home: "/Users/someone", ProjectRoot: root,
		Guard: one.Guard, Git: corpusGit{Git: NewGit(nil), answers: answers}, Deadline: corpusDeadline}
}

// corpusDeadline is the reading's budget in the corpus, far above any machine's reading time.
const corpusDeadline = time.Minute

func TestCorpus(t *testing.T) {
	failed := 0
	for _, one := range readCorpus(t) {
		decision := Decide(one.Line, settingsOf(one))
		got := decision.Rules()
		ok := true
		switch {
		case one.Expect.Rules != nil:
			ok = slices.Equal(got, *one.Expect.Rules) || (len(got) == 0 && len(*one.Expect.Rules) == 0)
		case one.Expect.Has != "":
			ok = slices.Contains(got, one.Expect.Has)
		case one.Expect.Refused:
			ok = len(got) > 0
		}
		for _, id := range one.Warnings {
			if !slices.ContainsFunc(decision.Warnings, func(warning Warning) bool { return warning.ID == id }) {
				ok = false
			}
		}
		if !ok {
			failed++
			line := one.Line
			if len(line) > 160 {
				line = line[:160] + "…"
			}
			t.Errorf("%s %s: %q: got %v %v, want %+v %v", one.ID, one.Source, line, got, decision.Warnings, one.Expect, one.Warnings)
		}
	}
	if failed > 0 {
		t.Logf("%d cases failed", failed)
	}
}
