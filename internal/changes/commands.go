// Package changes is `jst changes`: one file per changelog entry, typed (docs/design/release.md,
// section 2) or, in a project without a `release` section, as untyped entries
// (docs/design/implementation.md, section 4.3). Designed from those specs, not translated (W-036).
package changes

import (
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

// Hooks are what the release package adds to these commands, set by its init (the release
// package imports this one, not the other way round).
var Hooks struct {
	// Check is the release rules over a project: the `release` section, the override and the
	// request; each problem a sentence.
	Check func(project Project) (problems []string, fatal error)
	// NextVersion is the version the next release would get.
	NextVersion func(project Project) (string, error)
	// Preflight is the release state every command of a project with `release` checks first: the
	// settings, the checkout, the history and the version files (exit 2 when wrong).
	Preflight func(project Project) error
}

// loadChecked is LoadProject with the release preflight in a project with `release`.
func loadChecked() (Project, error) {
	project, err := LoadProject()
	if err != nil {
		return Project{}, err
	}
	if project.Settings.Release && Hooks.Preflight != nil {
		if err := Hooks.Preflight(project); err != nil {
			return Project{}, err
		}
	}
	return project, nil
}

func init() {
	cli.Register(
		cli.Command{Name: "changes new", Summary: "a new changelog entry", Usage: newHelp, Run: runNew},
		cli.Command{Name: "changes check", Summary: "every changelog entry, valid or each problem named", Usage: checkHelp, Run: runCheck},
		cli.Command{Name: "changes view", Summary: "the next release's section, as it would be written", Usage: viewHelp, Run: runView},
		cli.Command{Name: "changes collect", Summary: "write the entries into the changelog (projects without `release`)", Usage: collectHelp, Run: runCollect},
	)
}

// report prints an error from a command to stderr and returns its exit status.
func report(stderr io.Writer, command string, err error) int {
	fmt.Fprintf(stderr, "jst %s: %v\n", command, err)
	return exitCode(err)
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet("jst "+name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

func runNew(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := newFlags("changes new", stderr)
	slug := flags.String("slug", "", "the entry's name, lower-case letters, digits and -")
	kind := flags.String("type", "", "breaking, feature, fix, deprecated, security or note")
	audience := flags.String("audience", "", "consumer or internal")
	title := flags.String("title", "", "the entry's title, one line")
	migration := flags.String("migration", "", "what a consumer changes (required with --type breaking)")
	card := flags.String("card", "", "the card the change is for")
	if flags.Parse(args) != nil || flags.NArg() > 0 {
		return 2
	}
	project, err := loadChecked()
	if err != nil {
		return report(stderr, "changes new", err)
	}
	written, err := New(project, NewRequest{Slug: *slug, Type: *kind, Audience: *audience, Title: *title, Migration: *migration, Card: *card})
	if err != nil {
		return report(stderr, "changes new", err)
	}
	workDir, _ := os.Getwd()
	if relative, err := filepath.Rel(workDir, written); err == nil {
		written = relative
	}
	fmt.Fprintln(stdout, written)
	return 0
}

// NewRequest is what `jst changes new` was asked for.
type NewRequest struct {
	Slug, Type, Audience, Title, Migration, Card string
}

// New writes a new entry and returns its path. In a project with `release` it writes a complete,
// valid typed entry or nothing; without, an untyped entry unless a type is given.
func New(project Project, request NewRequest) (string, error) {
	settings := project.Settings
	if !IsSlug(request.Slug) {
		return "", usageProblem("--slug %q: lower-case letters, digits and -, starting with a letter or digit, at most 80", request.Slug)
	}
	if request.Card != "" {
		if settings.Cards == nil {
			return "", usageProblem("--card needs a `cards` section in jackstay.json")
		}
		if !settings.Cards.ID.MatchString(request.Card) {
			return "", usageProblem("--card %q does not match `cards.id`", request.Card)
		}
	}
	typed := settings.Release || request.Type != "" || request.Audience != ""
	if settings.Release {
		var missing []string
		for flagName, value := range map[string]string{"--type": request.Type, "--audience": request.Audience, "--title": request.Title} {
			if value == "" {
				missing = append(missing, flagName)
			}
		}
		if len(missing) > 0 {
			return "", usageProblem("this project has a `release` section: %s required", strings.Join(sortedStrings(missing), ", "))
		}
	}
	var data []byte
	if typed {
		if request.Type == "breaking" && strings.TrimSpace(request.Migration) == "" {
			return "", usageProblem("--type breaking needs --migration: what a consumer changes")
		}
		entry := Entry{Name: request.Slug + ".md", Typed: true, Type: request.Type, Audience: request.Audience,
			Card: request.Card, Title: strings.TrimSpace(request.Title), Migration: strings.TrimSpace(request.Migration)}
		if settings.Template != "" {
			fromTemplate, err := templateEntry(project, request)
			if err != nil {
				return "", err
			}
			entry.Body = fromTemplate.Body
			entry.Yanks = fromTemplate.Yanks
			if entry.Migration == "" {
				entry.Migration = fromTemplate.Migration
			}
		}
		data = Format(entry)
		if _, problems := Parse(entry.Name, data); len(problems) > 0 {
			return "", &Problem{Code: 1, Message: "the entry would not be valid: " + strings.Join(problems, "; ")}
		}
	} else {
		var err error
		data, err = stageOneEntry(project, request)
		if err != nil {
			return "", err
		}
	}
	target, err := InRepo(project.Root, path.Join(settings.Dir, request.Slug+".md"))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { // #nosec G301 -- a folder of the project's own tree
		return "", &Problem{Code: 2, Message: err.Error()}
	}
	// A complete entry or nothing: written whole to a temporary file, then linked into place, which
	// fails rather than replace an entry of that name.
	temporary, err := os.CreateTemp(filepath.Dir(target), ".jackstay-entry-")
	if err != nil {
		return "", &Problem{Code: 2, Message: err.Error()}
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return "", &Problem{Code: 2, Message: err.Error()}
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", &Problem{Code: 2, Message: err.Error()}
	}
	if err := temporary.Close(); err != nil {
		return "", &Problem{Code: 2, Message: err.Error()}
	}
	if err := os.Chmod(temporary.Name(), 0o644); err != nil { // #nosec G302 -- an entry of the project's own tree
		return "", &Problem{Code: 2, Message: err.Error()}
	}
	if err := os.Link(temporary.Name(), target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", &Problem{Code: 2, Message: path.Join(settings.Dir, request.Slug+".md") + " exists already: choose another --slug"}
		}
		return "", &Problem{Code: 2, Message: err.Error()}
	}
	return target, nil
}

// templateEntry is the project's template with {card} and {slug} filled, read as an entry. In a
// project with `release` it must begin with a front matter.
func templateEntry(project Project, request NewRequest) (Entry, error) {
	data, err := os.ReadFile(filepath.Join(project.Root, filepath.FromSlash(project.Settings.Template)))
	if err != nil {
		return Entry{}, &Problem{Code: 2, Message: "the template: " + err.Error()}
	}
	if problem := TemplateProblem(data); problem != "" {
		return Entry{}, &Problem{Code: 2, Message: "the template " + project.Settings.Template + ": " + problem}
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(data), "{card}", request.Card), "{slug}", request.Slug)
	entry, _ := Parse(request.Slug+".md", []byte(text))
	return entry, nil
}

// TemplateProblem says what keeps a template from being a typed entry's template: it must begin
// with a front matter that is one JSON object of an entry's keys (the values may be left empty for
// `jst changes new` to fill). "" when it is one.
func TemplateProblem(data []byte) string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "it must begin with a front matter (one JSON object between `---` lines) in a project with `release`"
	}
	rest := text[len("---\n"):]
	closing := strings.Index(rest, "\n---\n")
	if closing < 0 && strings.HasSuffix(rest, "\n---") {
		closing = len(rest) - len("\n---")
	}
	if closing < 0 {
		return "its front matter has no closing `---` line"
	}
	matter := strings.TrimSpace(rest[:closing])
	if !strings.HasPrefix(matter, "{") {
		return "its front matter is not one JSON object"
	}
	var read frontMatter
	if err := strictjson.Unmarshal([]byte(matter), &read); err != nil {
		return "its front matter is not one JSON object of type, audience, card and yanks: " + err.Error()
	}
	return ""
}

// stageOneEntry is an untyped entry: the template with {card} and {slug} filled, or one line.
func stageOneEntry(project Project, request NewRequest) ([]byte, error) {
	if project.Settings.Template != "" {
		data, err := os.ReadFile(filepath.Join(project.Root, filepath.FromSlash(project.Settings.Template)))
		if err != nil {
			return nil, &Problem{Code: 2, Message: "the template " + project.Settings.Template + ": " + err.Error()}
		}
		return []byte(strings.ReplaceAll(strings.ReplaceAll(string(data), "{card}", request.Card), "{slug}", request.Slug)), nil
	}
	line := "- "
	if request.Card != "" {
		line += "(" + request.Card + ") "
	}
	title := strings.ReplaceAll(request.Slug, "-", " ")
	if request.Title != "" {
		title = request.Title
	}
	return []byte(line + title + "\n"), nil
}

func runCheck(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := newFlags("changes check", stderr)
	asJSON := flags.Bool("json", false, "print the result as one JSON object")
	if flags.Parse(args) != nil || flags.NArg() > 0 {
		return 2
	}
	project, err := LoadProject()
	if err != nil {
		return report(stderr, "changes check", err)
	}
	problems, count, err := Check(project)
	if err != nil {
		return report(stderr, "changes check", err)
	}
	if *asJSON {
		encoded, _ := json.Marshal(struct {
			Entries  int      `json:"entries"`
			Problems []string `json:"problems"`
		}{Entries: count, Problems: append([]string{}, problems...)})
		fmt.Fprintln(stdout, string(encoded))
		if len(problems) > 0 {
			return 1
		}
		return 0
	}
	for _, problem := range problems {
		fmt.Fprintln(stdout, problem)
	}
	if len(problems) > 0 {
		fmt.Fprintf(stderr, "jst changes check: %d problem(s)\n", len(problems))
		return 1
	}
	fmt.Fprintf(stdout, "jst changes check: %d entr%s, all valid\n", count, plural(count, "y", "ies"))
	return 0
}

// Check judges every entry in the working tree, and, through the release hooks, the release
// rules. It returns the problems (each "path: sentence") and how many entries there are.
func Check(project Project) ([]string, int, error) {
	listed, err := ListEntries(project.Worktree(), project.Settings.Dir)
	if err != nil {
		return nil, 0, &Problem{Code: 2, Message: err.Error()}
	}
	var problems []string
	for _, listing := range listed.Entries {
		for _, problem := range EntryProblems(listing, project.Settings) {
			problems = append(problems, listing.File.Path+": "+problem)
		}
	}
	if Hooks.Check != nil {
		more, fatal := Hooks.Check(project)
		if fatal != nil {
			return nil, 0, fatal
		}
		problems = append(problems, more...)
	}
	return problems, len(listed.Entries), nil
}

// EntryProblems are what makes a listed entry invalid in a project with or without `release`.
func EntryProblems(listing Listing, settings Settings) []string {
	problems := append([]string{}, listing.Problems...)
	if listing.Entry.Card != "" && (settings.Cards == nil || !settings.Cards.ID.MatchString(listing.Entry.Card)) {
		problems = append(problems, fmt.Sprintf("its card %q does not match `cards.id`", listing.Entry.Card))
	}
	if settings.Release {
		if !listing.Entry.Typed {
			problems = append(problems, "it has no front matter: a project with `release` needs typed entries ("+
				`---`+"\n"+`{"type": "fix", "audience": "consumer"}`+"\n"+`---`+" before its title)")
		}
		if !listing.File.Regular() {
			problems = append(problems, "it is not a regular file (a link's content is its target's name, not the text released)")
		}
	}
	return problems
}

func runView(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := newFlags("changes view", stderr)
	version := flags.String("version", "", "the version in the heading")
	date := flags.String("date", "", "the date in the heading (default today)")
	audience := flags.String("audience", Consumer, "consumer, internal or all")
	if flags.Parse(args) != nil || flags.NArg() > 0 {
		return 2
	}
	if *audience != Consumer && *audience != Internal && *audience != "all" {
		fmt.Fprintln(stderr, "jst changes view: --audience is consumer, internal or all")
		return 2
	}
	project, err := loadChecked()
	if err != nil {
		return report(stderr, "changes view", err)
	}
	listed, err := ListEntries(project.Worktree(), project.Settings.Dir)
	if err != nil {
		return report(stderr, "changes view", err)
	}
	if len(listed.Entries) == 0 {
		fmt.Fprintf(stdout, "no entries in %s: the next release's section would be empty\n", project.Settings.Dir)
		return 0
	}
	if *version == "" {
		*version = "next"
		if project.Settings.Release && Hooks.NextVersion != nil {
			next, err := Hooks.NextVersion(project)
			if err != nil {
				return report(stderr, "changes view", err)
			}
			*version = next
		}
	}
	if *date == "" {
		*date = time.Now().Format("2006-01-02") // a command for reading, run by hand (docs/design/implementation.md section 4.3)
	}
	entries := make([]Entry, 0, len(listed.Entries))
	for _, listing := range listed.Entries {
		entries = append(entries, listing.Entry)
	}
	fmt.Fprint(stdout, Section(Heading(project.Settings.Heading, *version, *date), Of(entries, *audience)))
	return 0
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

func sortedStrings(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}
