package release

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/changes"
)

// ErrNothing is a prepare with no bumping entry and no override: nothing to release.
var ErrNothing = errors.New("nothing to release: no entry bumps the version and there is no override")

// InvalidEntries is a prepare refused because entries are invalid; each problem "path: sentence".
type InvalidEntries struct{ Problems []string }

func (invalid *InvalidEntries) Error() string {
	return "invalid entries:\n  " + strings.Join(invalid.Problems, "\n  ")
}

// Inputs are what the prepared tree P(X) is a function of (docs/design/release.md, section 3):
// X's files, X's settings, X's committer date and the last release at X.
type Inputs struct {
	Tree     changes.Tree
	Settings Settings
	Last     Version
	Date     string // YYYY-MM-DD, X's committer date in UTC
	// ReleaseHeadings are each earlier release's heading template, by version, so a yank marks a
	// heading written in an older format (default: today's template).
	ReleaseHeadings map[string]string
}

// Prepared is P(X): what prepare writes and removes, and what it computed.
type Prepared struct {
	Base     string // the commit P was computed on ("" when computed from a tree alone)
	Last     Version
	Version  Version
	Bump     string
	Override *Override
	Build    string // the new build number, "" without release.build
	Entries  []changes.Entry
	Listed   changes.Listed
	Heading  string
	Writes   map[string][]byte // path -> new content, every output prepare changes
	Removes  []string          // paths prepare removes: the entries, the override, the request
	Consumer string            // the consumer section as written
	Internal string            // the internal section ("" when there is no internal entry)
}

// Next is the version the entries of a tree give after last, and the bump: the override's version
// when there is one. It is an error when the folder holds invalid entries.
func Next(tree changes.Tree, settings Settings, last Version) (Version, string, *Override, changes.Listed, error) {
	listed, err := changes.ListEntries(tree, settings.Changes.Dir)
	if err != nil {
		return Version{}, "", nil, changes.Listed{}, err
	}
	var problems []string
	entries := make([]changes.Entry, 0, len(listed.Entries))
	for _, listing := range listed.Entries {
		for _, problem := range changes.EntryProblems(listing, settings.Changes) {
			problems = append(problems, listing.File.Path+": "+problem)
		}
		entries = append(entries, listing.Entry)
	}
	var override *Override
	if listed.Override != nil {
		if !listed.Override.Regular() {
			problems = append(problems, listed.Override.Path+": not a regular file")
		} else {
			data, _, err := tree.Read(listed.Override.Path)
			if err != nil {
				return Version{}, "", nil, listed, err
			}
			read, err := DecodeOverride(data)
			if err != nil {
				problems = append(problems, listed.Override.Path+": "+err.Error())
			} else {
				override = &read
			}
		}
	}
	if len(problems) > 0 {
		return Version{}, "", nil, listed, &InvalidEntries{Problems: problems}
	}
	bump := HighestBump(entries)
	if override != nil {
		version, _ := ParseVersion(override.Version)
		if !last.Less(version) {
			return Version{}, "", nil, listed, &InvalidEntries{Problems: []string{
				fmt.Sprintf("%s: the override's version %s is not greater than the last release %s", listed.Override.Path, version, last)}}
		}
		return version, bump, override, listed, nil
	}
	if bump == "" {
		return Version{}, "", nil, listed, ErrNothing
	}
	return last.Next(bump), bump, nil, listed, nil
}

// Prepare computes P(X): every output's new bytes in memory, each version file read back by its
// own pattern, before anything is written.
func Prepare(inputs Inputs) (Prepared, error) {
	settings := inputs.Settings
	version, bump, override, listed, err := Next(inputs.Tree, settings, inputs.Last)
	if err != nil {
		return Prepared{}, err
	}
	prepared := Prepared{Last: inputs.Last, Version: version, Bump: bump, Override: override, Listed: listed, Writes: map[string][]byte{}}
	for _, listing := range listed.Entries {
		prepared.Entries = append(prepared.Entries, listing.Entry)
		prepared.Removes = append(prepared.Removes, listing.File.Path)
	}
	if listed.Override != nil {
		prepared.Removes = append(prepared.Removes, listed.Override.Path)
	}
	if listed.Request != nil {
		prepared.Removes = append(prepared.Removes, listed.Request.Path)
	}
	for _, file := range settings.Files {
		data, exists, err := inputs.Tree.Read(file.Path)
		if err != nil {
			return Prepared{}, err
		}
		if !exists {
			return Prepared{}, fmt.Errorf("%s: the version file is missing", file.Path)
		}
		held, err := file.captures(data)
		if err != nil {
			return Prepared{}, err
		}
		if held != inputs.Last.String() {
			return Prepared{}, fmt.Errorf("the version files say %s, the last release is %s (%s)", held, inputs.Last, file.Path)
		}
		written, err := file.replace(data, version.String())
		if err != nil {
			return Prepared{}, err
		}
		prepared.Writes[file.Path] = written
	}
	if settings.Build != nil {
		data, exists, err := inputs.Tree.Read(settings.Build.Path)
		if err != nil {
			return Prepared{}, err
		}
		if !exists {
			return Prepared{}, fmt.Errorf("%s: the build file is missing", settings.Build.Path)
		}
		held, err := settings.Build.captures(data)
		if err != nil {
			return Prepared{}, err
		}
		number, err := strconv.ParseUint(held, 10, 63)
		if err != nil {
			return Prepared{}, fmt.Errorf("%s: the build number %q is not a decimal integer", settings.Build.Path, held)
		}
		prepared.Build = strconv.FormatUint(number+1, 10)
		written, err := settings.Build.replace(data, prepared.Build)
		if err != nil {
			return Prepared{}, err
		}
		prepared.Writes[settings.Build.Path] = written
	}
	prepared.Heading = changes.Heading(settings.Changes.Heading, version.String(), inputs.Date)
	consumer := changes.Of(prepared.Entries, changes.Consumer)
	internal := changes.Of(prepared.Entries, changes.Internal)
	prepared.Consumer = changes.Section(prepared.Heading, consumer)
	changelog, _, err := inputs.Tree.Read(settings.Changes.Into)
	if err != nil {
		return Prepared{}, err
	}
	text := markYanked(string(changelog), settings.Changes.Heading, inputs.ReleaseHeadings, prepared.Entries)
	prepared.Writes[settings.Changes.Into] = []byte(changes.Insert(text, prepared.Consumer, settings.Changes.Newest,
		changes.HeadingPattern(settings.Changes.Heading)))
	if len(internal) > 0 {
		prepared.Internal = changes.Section(prepared.Heading, internal)
		if settings.Changes.InternalInto != "" {
			internalLog, _, err := inputs.Tree.Read(settings.Changes.InternalInto)
			if err != nil {
				return Prepared{}, err
			}
			prepared.Writes[settings.Changes.InternalInto] = []byte(changes.Insert(string(internalLog), prepared.Internal,
				settings.Changes.Newest, changes.HeadingPattern(settings.Changes.Heading)))
		}
	}
	sort.Strings(prepared.Removes)
	return prepared, nil
}

// markYanked adds " [YANKED]" to the heading of each version a yank note names.
func markYanked(text, template string, headings map[string]string, entries []changes.Entry) string {
	for _, entry := range entries {
		if entry.Yanks == "" {
			continue
		}
		own := template
		if historical, ok := headings[entry.Yanks]; ok && historical != "" {
			own = historical
		}
		heading := changes.VersionHeading(text, own, entry.Yanks)
		if heading == "" || strings.HasSuffix(heading, "[YANKED]") {
			continue
		}
		lines := strings.Split(text, "\n")
		for index, line := range lines {
			if strings.TrimRight(line, " \t") == heading {
				lines[index] = heading + " [YANKED]"
				break
			}
		}
		text = strings.Join(lines, "\n")
	}
	return text
}

// Message is the release commit's message (docs/design/release.md, section 8).
func (prepared Prepared) Message() string {
	var out strings.Builder
	fmt.Fprintf(&out, "Release %s\n\n", prepared.Version)
	out.WriteString(strings.TrimRight(prepared.Consumer, "\n"))
	out.WriteString("\n")
	if prepared.Internal != "" {
		out.WriteString("\nInternal:\n\n")
		out.WriteString(strings.TrimRight(changes.Body(changes.Of(prepared.Entries, changes.Internal)), "\n"))
		out.WriteString("\n")
	}
	if prepared.Override != nil {
		fmt.Fprintf(&out, "\nOverride: %s\n", prepared.Override.Reason)
	}
	fmt.Fprintf(&out, "\nCard: none (release %s)\n\nJackstay-Release: %s\n", prepared.Version, prepared.Version)
	return out.String()
}

// Paths are every path prepare touches: what it writes and what it removes.
func (prepared Prepared) Paths() []string {
	paths := append([]string{}, prepared.Removes...)
	for filePath := range prepared.Writes {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	return paths
}

// EntryNames are the file names of the entries prepared, for a request.
func (prepared Prepared) EntryNames() []string {
	names := make([]string, 0, len(prepared.Listed.Entries))
	for _, listing := range prepared.Listed.Entries {
		names = append(names, path.Base(listing.File.Path))
	}
	return names
}
