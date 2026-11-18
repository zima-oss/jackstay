package changes

import (
	"regexp"
	"strings"
)

// Heading is a version's heading from the `changes.heading` template.
func Heading(template, version, date string) string {
	return strings.ReplaceAll(strings.ReplaceAll(template, "{version}", version), "{date}", date)
}

// HeadingPattern matches any version's heading of the template, so a changelog's existing
// sections can be found.
func HeadingPattern(template string) *regexp.Regexp {
	var pattern strings.Builder
	pattern.WriteString("^")
	rest := template
	for rest != "" {
		next := len(rest)
		var placeholder string
		for _, candidate := range []string{"{version}", "{date}"} {
			if at := strings.Index(rest, candidate); at >= 0 && at < next {
				next, placeholder = at, candidate
			}
		}
		pattern.WriteString(regexp.QuoteMeta(rest[:next]))
		if placeholder == "" {
			break
		}
		pattern.WriteString(".+")
		rest = rest[next+len(placeholder):]
	}
	pattern.WriteString(`\s*$`)
	return regexp.MustCompile(pattern.String())
}

// Body renders entries as a section's body (without its heading): typed entries by type in the
// fixed order, each type under its own heading, entries in the order given (callers pass them in
// file-name order); then the untyped ones (docs/design/implementation.md section 4.3). Empty when there are none.
func Body(entries []Entry) string {
	var blocks []string
	for _, kind := range Types {
		var items []string
		for _, entry := range entries {
			if entry.Typed && entry.Type == kind {
				items = append(items, item(entry))
			}
		}
		if len(items) > 0 {
			blocks = append(blocks, "### "+typeHeadings[kind]+"\n\n"+strings.Join(items, "\n"))
		}
	}
	for _, entry := range entries {
		if !entry.Typed && entry.Text != "" {
			blocks = append(blocks, entry.Text)
		}
	}
	return strings.Join(blocks, "\n\n")
}

// Section is a whole section: the heading, a blank line, the body and a final line end.
func Section(heading string, entries []Entry) string {
	body := Body(entries)
	if body == "" {
		return heading + "\n"
	}
	return heading + "\n\n" + body + "\n"
}

// item is one typed entry as a list item: the title in bold (with its card), then the body and
// the migration note indented under it.
func item(entry Entry) string {
	title := "- **" + entry.Title + "**"
	if entry.Card != "" {
		title += " (" + entry.Card + ")"
	}
	lines := []string{title}
	if entry.Body != "" {
		lines = append(lines, "", indent(entry.Body))
	}
	if entry.Migration != "" {
		lines = append(lines, "", indent("Migration: "+entry.Migration))
	}
	return strings.Join(lines, "\n")
}

func indent(text string) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[index] = "  " + line
		} else {
			lines[index] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// Of picks the entries of an audience: "consumer", "internal", or "all" for both. Untyped
// entries count as consumer ones (an untyped entry has no audience).
func Of(entries []Entry, audience string) []Entry {
	var out []Entry
	for _, entry := range entries {
		entryAudience := entry.Audience
		if !entry.Typed {
			entryAudience = Consumer
		}
		if audience == "all" || entryAudience == audience {
			out = append(out, entry)
		}
	}
	return out
}

// Insert puts a section into a changelog's text: before the first existing version heading when
// newest is "first" (or at the end when there is none), at the end when it is "last".
func Insert(changelog, section, newest string, heading *regexp.Regexp) string {
	if newest == "first" {
		lines := strings.Split(changelog, "\n")
		for index, line := range lines {
			if heading.MatchString(line) {
				before := strings.Join(lines[:index], "\n")
				if before != "" {
					before += "\n"
				}
				return before + section + "\n" + strings.Join(lines[index:], "\n")
			}
		}
	}
	trimmed := strings.TrimRight(changelog, "\n")
	if strings.TrimSpace(trimmed) == "" {
		return section
	}
	return trimmed + "\n\n" + section
}
