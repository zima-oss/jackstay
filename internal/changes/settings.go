package changes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cards"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
)

// Settings are a project's `changes` section, and what the commands need around it.
type Settings struct {
	Dir          string
	Into         string
	Heading      string
	Template     string
	Newest       string // "first" or "last"
	InternalInto string
	// Release says the project has a `release` section: entries must be typed.
	Release bool
	// Cards is the `cards` section's rules, when there is one (to check an entry's card).
	Cards *cards.Rules
}

// Problem is a failure a command reports with an exit status.
type Problem struct {
	Code    int
	Message string
}

func (problem *Problem) Error() string { return problem.Message }

// usageProblem is exit 2 with the command's usage.
func usageProblem(format string, args ...any) error {
	return &Problem{Code: 2, Message: fmt.Sprintf(format, args...)}
}

// SettingsOf reads the sections of a loaded jackstay.json.
func SettingsOf(loaded config.Config) (Settings, error) {
	section := loaded.Section("changes")
	if section == nil {
		return Settings{}, &Problem{Code: 2, Message: "jackstay.json has no `changes` section"}
	}
	text := func(key string) string { value, _ := section[key].(string); return value }
	settings := Settings{
		Dir: text("dir"), Into: text("into"), Heading: text("heading"), Template: text("template"),
		Newest: text("newest"), InternalInto: text("internal_into"),
		Release: loaded.Section("release") != nil,
	}
	if settings.Newest == "" {
		settings.Newest = "last"
	}
	if cardsSection := loaded.Section("cards"); cardsSection != nil {
		rules, err := cards.FromSection(cardsSection)
		if err != nil {
			return Settings{}, &Problem{Code: 2, Message: err.Error()}
		}
		settings.Cards = &rules
	}
	return settings, nil
}

// Project is the repository a command runs in: its root, its jackstay.json and its `changes` settings.
type Project struct {
	Root     string
	Config   config.Config
	Settings Settings
	Git      gitcli.Git
	Format   string // the repository's object format, "sha1" or "sha256"
}

// LoadProject finds the project around the working folder.
func LoadProject() (Project, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return Project{}, &Problem{Code: 2, Message: err.Error()}
	}
	return LoadProjectAt(workDir)
}

// LoadProjectAt finds the project around start.
func LoadProjectAt(start string) (Project, error) {
	root, loaded, err := config.Load(start, []string{"changes", "release", "cards"})
	if err != nil {
		return Project{}, &Problem{Code: 2, Message: err.Error()}
	}
	if root == "" {
		return Project{}, &Problem{Code: 2, Message: "no " + config.Name + " at or above " + start}
	}
	settings, err := SettingsOf(loaded)
	if err != nil {
		return Project{}, err
	}
	project := Project{Root: root, Config: loaded, Settings: settings, Git: LiteralGit(root), Format: "sha1"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if format, err := project.Git.Line(ctx, "rev-parse", "--show-object-format"); err == nil && strings.TrimSpace(format) != "" {
		project.Format = strings.TrimSpace(format)
	}
	return project, nil
}

// LiteralGit is git in root with every path taken literally (GIT_LITERAL_PATHSPECS): a configured
// folder named `notes*` is that folder, never a pattern over others.
func LiteralGit(root string) gitcli.Git {
	return gitcli.Git{Dir: root, Env: append(gitcli.WithoutRepository(os.Environ()), "GIT_LITERAL_PATHSPECS=1")}
}

// Worktree is the project's working tree as a Tree.
func (project Project) Worktree() Worktree {
	return Worktree{Root: project.Root, Format: project.Format}
}

// exitCode is the status of an error from a command: its Problem's code, else 2.
func exitCode(err error) int {
	var problem *Problem
	if errors.As(err, &problem) {
		return problem.Code
	}
	return 2
}
