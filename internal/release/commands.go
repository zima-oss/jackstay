package release

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/cli"
)

func init() {
	cli.Register(
		cli.Command{Name: "release prepare", Summary: "print the next release as the workflow would make it; writes nothing", Usage: prepareHelp, Run: runPrepare},
		cli.Command{Name: "release override", Summary: "a version given by hand, with its reason, as a file for a pull request", Usage: overrideHelp, Run: runOverride},
		cli.Command{Name: "release notes", Summary: "a release's notes: markdown, text, TestFlight, Sparkle", Usage: notesHelp, Run: runNotes},
		cli.Command{Name: "release yank", Summary: "mark a release yanked, as an entry for a pull request", Usage: yankHelp, Run: runYank},
		cli.Command{Name: "release pr", Summary: "the release workflow's step: create, refresh or close the release pull request", Usage: pullRequestHelp, Run: runPullRequest},
		cli.Command{Name: "release publish", Summary: "the release workflow's step: make, tag and publish a landed release", Usage: publishHelp, Run: runPublish},
	)
	changes.Hooks.Check = checkHook
	changes.Hooks.NextVersion = nextVersionHook
	changes.Hooks.Preflight = func(loaded changes.Project) error {
		_, _, _, err := preflightAt(loaded)
		return err
	}
}

// Problem is a failure with its exit status (shared with changes).
type Problem = changes.Problem

func fail(stderr io.Writer, command string, err error) int {
	fmt.Fprintf(stderr, "jst release %s: %v\n", command, err)
	var problem *Problem
	if errors.As(err, &problem) {
		return problem.Code
	}
	var invalid *InvalidEntries
	if errors.As(err, &invalid) || errors.Is(err, ErrNothing) {
		return 1
	}
	return 2
}

func flagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet("jst release "+name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

// project is the project around the working folder, with its release settings.
func project() (changes.Project, Settings, Repo, error) {
	loaded, err := changes.LoadProject()
	if err != nil {
		return changes.Project{}, Settings{}, Repo{}, err
	}
	settings, problems := SettingsOf(loaded.Config)
	if len(problems) > 0 {
		return changes.Project{}, Settings{}, Repo{}, &Problem{Code: 2, Message: strings.Join(problems, "; ")}
	}
	repo := NewRepo(loaded.Root)
	return loaded, settings, repo, nil
}

// checkHook is `jst changes check`'s release part. The settings, the checkout's validation and the
// version-file invariant are fatal (exit 2); the override and the request are problems (exit 1).
func checkHook(loaded changes.Project) ([]string, error) {
	if loaded.Config.Section("release") == nil {
		return nil, nil
	}
	settings, repo, state, err := preflightAt(loaded)
	if err != nil {
		return nil, err
	}
	var problems []string
	worktree := loaded.Worktree()
	listed, err := changes.ListEntries(worktree, settings.Changes.Dir)
	if err != nil {
		return nil, &Problem{Code: 2, Message: err.Error()}
	}
	last := state.History.Last()
	problems = append(problems, state.History.YankProblems(listed)...)
	if listed.Override != nil {
		data, _, _ := worktree.Read(listed.Override.Path)
		if override, err := DecodeOverride(data); err != nil {
			problems = append(problems, listed.Override.Path+": "+err.Error())
		} else if version, _ := ParseVersion(override.Version); !last.Less(version) {
			problems = append(problems, fmt.Sprintf("%s: %s is not greater than the last release %s", listed.Override.Path, version, last))
		}
		if !listed.Override.Regular() {
			problems = append(problems, listed.Override.Path+": not a regular file")
		}
	}
	if listed.Request != nil {
		data, _, _ := worktree.Read(listed.Request.Path)
		if _, err := DecodeRequest(data, repo.Format); err != nil {
			problems = append(problems, listed.Request.Path+": "+err.Error())
		}
		if !listed.Request.Regular() {
			problems = append(problems, listed.Request.Path+": not a regular file")
		}
	}
	return problems, nil
}

// preflight is what every local release command checks first: the settings, the checkout
// (Validate), the history at HEAD and the version-file invariant. Any failure is exit 2.
func preflight() (changes.Project, Settings, Repo, State, error) {
	loaded, err := changes.LoadProject()
	if err != nil {
		return changes.Project{}, Settings{}, Repo{}, State{}, err
	}
	settings, repo, state, err := preflightAt(loaded)
	return loaded, settings, repo, state, err
}

func preflightAt(loaded changes.Project) (Settings, Repo, State, error) {
	settings, problems := SettingsOf(loaded.Config)
	if len(problems) > 0 {
		return Settings{}, Repo{}, State{}, &Problem{Code: 2, Message: "jackstay.json: " + strings.Join(problems, "; ")}
	}
	repo := NewRepo(loaded.Root)
	if problems := Validate(loaded.Root, repo, settings); len(problems) > 0 {
		return Settings{}, Repo{}, State{}, &Problem{Code: 2, Message: "jackstay.json: " + strings.Join(problems, "; ")}
	}
	head, err := repo.Resolve("HEAD")
	if err != nil {
		return Settings{}, Repo{}, State{}, &Problem{Code: 2, Message: "no commit at HEAD"}
	}
	state, err := repo.StateAt(head)
	if err != nil {
		return Settings{}, Repo{}, State{}, &Problem{Code: 2, Message: err.Error()}
	}
	if err := repo.CheckVersionFiles(state); err != nil {
		return Settings{}, Repo{}, State{}, err
	}
	return settings, repo, state, nil
}

func nextVersionHook(loaded changes.Project) (string, error) {
	settings, _, state, err := preflightAt(loaded)
	if err != nil {
		return "", err
	}
	version, _, _, _, err := Next(loaded.Worktree(), settings, state.History.Last())
	if err != nil {
		return "", err
	}
	return version.String(), nil
}

func runOverride(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flagSet("override", stderr)
	versionText := flags.String("version", "", "the version the next release gets")
	reason := flags.String("reason", "", "why (recorded in the release)")
	if flags.Parse(args) != nil || flags.NArg() > 0 || *versionText == "" || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stderr, "usage: jst release override --version V --reason TEXT")
		return 2
	}
	loaded, settings, _, state, err := preflight()
	if err != nil {
		return fail(stderr, "override", err)
	}
	version, err := ParseVersion(*versionText)
	if err != nil {
		return fail(stderr, "override", &Problem{Code: 2, Message: err.Error()})
	}
	history := state.History
	if !history.Last().Less(version) {
		return fail(stderr, "override", &Problem{Code: 1, Message: fmt.Sprintf("%s is not greater than the last release %s", version, history.Last())})
	}
	data := []byte(`{"version": ` + jsonString(version.String()) + `, "reason": ` + jsonString(strings.TrimSpace(*reason)) + "}\n")
	target, err := changes.InRepo(loaded.Root, settings.OverridePath())
	if err != nil {
		return fail(stderr, "override", err)
	}
	if existing, err := os.ReadFile(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(stderr, "override", &Problem{Code: 2, Message: err.Error()})
	} else if err == nil && !bytes.Equal(existing, data) {
		return fail(stderr, "override", &Problem{Code: 1, Message: settings.OverridePath() + " exists with another override; change it by hand or remove it first"})
	}
	if err := changes.WriteFileAtomic(target, data); err != nil {
		return fail(stderr, "override", err)
	}
	fmt.Fprintln(stdout, settings.OverridePath())
	return 0
}

func runYank(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flagSet("yank", stderr)
	reason := flags.String("reason", "", "why the release is yanked")
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: jst release yank X.Y.Z --reason TEXT")
		return 2
	}
	versionText := args[0]
	if flags.Parse(args[1:]) != nil || flags.NArg() > 0 || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stderr, "usage: jst release yank X.Y.Z --reason TEXT")
		return 2
	}
	loaded, settings, _, state, err := preflight()
	if err != nil {
		return fail(stderr, "yank", err)
	}
	version, err := ParseVersion(versionText)
	if err != nil {
		return fail(stderr, "yank", &Problem{Code: 2, Message: err.Error()})
	}
	history := state.History
	released := false
	for _, release := range history.Releases {
		released = released || release.Version == version
	}
	if !released {
		return fail(stderr, "yank", &Problem{Code: 1, Message: version.String() + " is not a release on this branch"})
	}
	entry := changes.Entry{Typed: true, Type: "note", Audience: changes.Consumer, Yanks: version.String(),
		Title: version.String() + " is yanked", Body: strings.TrimSpace(*reason)}
	data := changes.Format(entry)
	name := "yank-" + strings.ReplaceAll(version.String(), ".", "-") + ".md"
	filePath := path.Join(settings.Changes.Dir, name)
	target, err := changes.InRepo(loaded.Root, filePath)
	if err != nil {
		return fail(stderr, "yank", err)
	}
	if existing, err := os.ReadFile(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(stderr, "yank", &Problem{Code: 2, Message: err.Error()})
	} else if err == nil && !bytes.Equal(existing, data) {
		return fail(stderr, "yank", &Problem{Code: 1, Message: filePath + " exists with other content"})
	}
	if err := changes.WriteFileAtomic(target, data); err != nil {
		return fail(stderr, "yank", err)
	}
	fmt.Fprintf(stdout, "%s\njst release yank: land it in a pull request; the release workflow then marks %s yanked on GitHub\n", filePath, version)
	return 0
}

// preparedJSON is prepare's plan as one object (--json), what the workflow reads.
type preparedJSON struct {
	Last     string   `json:"last"`
	Version  string   `json:"version"`
	Bump     string   `json:"bump"`
	Override string   `json:"override_reason,omitzero"`
	Build    string   `json:"build,omitzero"`
	Entries  []string `json:"entries"`
	Writes   []string `json:"writes"`
	Removes  []string `json:"removes"`
	Consumer string   `json:"consumer"`
	Internal string   `json:"internal"`
	Message  string   `json:"message"`
}

func planOf(prepared Prepared) preparedJSON {
	plan := preparedJSON{Last: prepared.Last.String(), Version: prepared.Version.String(), Bump: prepared.Bump,
		Build: prepared.Build, Entries: prepared.EntryNames(), Removes: prepared.Removes,
		Consumer: prepared.Consumer, Internal: prepared.Internal, Message: prepared.Message()}
	if prepared.Override != nil {
		plan.Override = prepared.Override.Reason
	}
	for filePath := range prepared.Writes {
		plan.Writes = append(plan.Writes, filePath)
	}
	sortStrings(plan.Writes)
	return plan
}

func printPlan(stdout io.Writer, prepared Prepared, asJSON bool) {
	if asJSON {
		encoded, _ := json.Marshal(planOf(prepared))
		fmt.Fprintln(stdout, string(encoded))
		return
	}
	plan := planOf(prepared)
	fmt.Fprintf(stdout, "last release %s, bump %q -> %s\n", plan.Last, plan.Bump, plan.Version)
	if plan.Override != "" {
		fmt.Fprintf(stdout, "override: %s\n", plan.Override)
	}
	for _, entry := range prepared.Entries {
		fmt.Fprintf(stdout, "  %s: %s, %s, %q\n", entry.Name, entry.Type, entry.Audience, entry.Bump())
	}
	fmt.Fprintf(stdout, "writes %s; removes %d file(s)\n\n", strings.Join(plan.Writes, ", "), len(plan.Removes))
	fmt.Fprint(stdout, prepared.Consumer)
}
