package release

import (
	"errors"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/zima-oss/jackstay/internal/changes"
)

// testFlightLimit is App Store Connect's "What to Test" length (inferred from its documented field
// length; docs/design/release.md, section 4).
const testFlightLimit = 4000

func runNotes(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flagSet("notes", stderr)
	format := flags.String("format", "markdown", "markdown, text, testflight or sparkle-html")
	audience := flags.String("audience", changes.Consumer, "consumer, internal or all (markdown and text only)")
	versionText := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		versionText, args = args[0], args[1:]
	}
	if flags.Parse(args) != nil || flags.NArg() > 0 {
		return 2
	}
	switch *format {
	case "markdown", "text":
		if *audience != changes.Consumer && *audience != changes.Internal && *audience != "all" {
			fmt.Fprintln(stderr, "jst release notes: --audience is consumer, internal or all")
			return 2
		}
	case "testflight", "sparkle-html":
		if *audience != changes.Consumer {
			fmt.Fprintf(stderr, "jst release notes: --format %s is for consumers: --audience consumer only\n", *format)
			return 2
		}
	default:
		fmt.Fprintln(stderr, "jst release notes: --format is markdown, text, testflight or sparkle-html")
		return 2
	}
	loaded, settings, repo, _, err := preflight()
	if err != nil {
		return fail(stderr, "notes", err)
	}
	entries, heading, err := notesEntries(loaded, settings, repo, versionText)
	if err != nil {
		return fail(stderr, "notes", err)
	}
	entries = changes.Of(entries, *audience)
	out, err := RenderNotes(entries, heading, *format)
	if err != nil {
		return fail(stderr, "notes", err)
	}
	fmt.Fprint(stdout, out)
	return 0
}

// notesEntries are the entries of a released version (those its release commit removed), or,
// without a version, the working tree's.
func notesEntries(loaded changes.Project, settings Settings, repo Repo, versionText string) ([]changes.Entry, string, error) {
	if versionText == "" {
		listed, err := changes.ListEntries(loaded.Worktree(), settings.Changes.Dir)
		if err != nil {
			return nil, "", err
		}
		var problems []string
		for _, listing := range listed.Entries {
			for _, problem := range changes.EntryProblems(listing, settings.Changes) {
				problems = append(problems, listing.File.Path+": "+problem)
			}
		}
		if len(problems) > 0 {
			return nil, "", &InvalidEntries{Problems: problems}
		}
		// The section prepare would write: the next version, HEAD's committer date.
		state, err := repo.StateAt("HEAD")
		if err != nil {
			return nil, "", err
		}
		// With a bumping entry, the section has the next version's heading; with notes only (no
		// release yet), the entries are exported under no heading.
		next, _, _, _, err := Next(loaded.Worktree(), settings, state.History.Last())
		if errors.Is(err, ErrNothing) {
			return entriesOf(listed), "", nil
		}
		if err != nil {
			return nil, "", err
		}
		return entriesOf(listed), changes.Heading(settings.Changes.Heading, next.String(), state.Date), nil
	}
	version, err := ParseVersion(versionText)
	if err != nil {
		return nil, "", &Problem{Code: 2, Message: err.Error()}
	}
	head, err := repo.Resolve("HEAD")
	if err != nil {
		return nil, "", &Problem{Code: 2, Message: "no commit at HEAD"}
	}
	history, err := repo.HistoryOf(head, settings.First)
	if err != nil {
		return nil, "", err
	}
	for _, release := range history.Releases {
		if release.Version == version {
			entries, err := repo.ReleasedEntries(release)
			if err != nil {
				return nil, "", err
			}
			// The section as the release wrote it: its own heading template, its parent's date.
			date, err := repo.CommitterDate(release.Parent)
			if err != nil {
				return nil, "", err
			}
			return entries, changes.Heading(release.Settings.Changes.Heading, version.String(), date), nil
		}
	}
	return nil, "", &Problem{Code: 1, Message: version.String() + " is not a release on this branch"}
}

// ReleasedEntries are a release's entries: the ones its commit removed, read from its parent with
// its parent's settings.
func (repo Repo) ReleasedEntries(release Landed) ([]changes.Entry, error) {
	loaded, err := repo.ConfigAt(release.Parent)
	if err != nil {
		return nil, err
	}
	settings, problems := SettingsOf(loaded)
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	listed, err := changes.ListEntries(repo.Tree(release.Parent), settings.Changes.Dir)
	if err != nil {
		return nil, err
	}
	return entriesOf(listed), nil
}

func entriesOf(listed changes.Listed) []changes.Entry {
	entries := make([]changes.Entry, 0, len(listed.Entries))
	for _, listing := range listed.Entries {
		entries = append(entries, listing.Entry)
	}
	return entries
}

// RenderNotes renders entries in one of the export formats.
func RenderNotes(entries []changes.Entry, version, format string) (string, error) {
	switch format {
	case "markdown":
		if version != "" {
			return changes.Section(version, entries), nil
		}
		return changes.Body(entries) + "\n", nil
	case "text":
		return plainText(entries, true), nil
	case "testflight":
		full := plainText(entries, true)
		if len([]rune(full)) <= testFlightLimit {
			return full, nil
		}
		titles := plainText(entries, false)
		if len([]rune(titles)) <= testFlightLimit {
			return titles, nil
		}
		return "", errorf("the notes of %s are %d characters with titles only; TestFlight's \"What to Test\" takes %d", version, len([]rune(titles)), testFlightLimit)
	case "sparkle-html":
		return sparkleHTML(entries), nil
	}
	return "", fmt.Errorf("unknown format %q", format)
}

// plainText is the notes without markup: a line per type, then each entry's title (and body).
func plainText(entries []changes.Entry, withBodies bool) string {
	var out strings.Builder
	for _, kind := range changes.Types {
		var items []changes.Entry
		for _, entry := range entries {
			if entry.Typed && entry.Type == kind {
				items = append(items, entry)
			}
		}
		if len(items) == 0 {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(changes.TypeHeading(kind) + ":\n")
		for _, entry := range items {
			out.WriteString("- " + entry.Title + "\n")
			if withBodies && entry.Body != "" {
				out.WriteString("  " + strings.ReplaceAll(entry.Body, "\n", "\n  ") + "\n")
			}
			if withBodies && entry.Migration != "" {
				out.WriteString("  Migration: " + strings.ReplaceAll(entry.Migration, "\n", "\n  ") + "\n")
			}
		}
	}
	return out.String()
}

// sparkleHTML is an HTML fragment for an appcast item's description.
func sparkleHTML(entries []changes.Entry) string {
	var out strings.Builder
	for _, kind := range changes.Types {
		var items []changes.Entry
		for _, entry := range entries {
			if entry.Typed && entry.Type == kind {
				items = append(items, entry)
			}
		}
		if len(items) == 0 {
			continue
		}
		out.WriteString("<h2>" + html.EscapeString(changes.TypeHeading(kind)) + "</h2>\n<ul>\n")
		for _, entry := range items {
			out.WriteString("<li><strong>" + html.EscapeString(entry.Title) + "</strong>")
			if entry.Body != "" {
				out.WriteString("<p>" + html.EscapeString(entry.Body) + "</p>")
			}
			if entry.Migration != "" {
				out.WriteString("<p><strong>Migration:</strong> " + html.EscapeString(entry.Migration) + "</p>")
			}
			out.WriteString("</li>\n")
		}
		out.WriteString("</ul>\n")
	}
	return out.String()
}
