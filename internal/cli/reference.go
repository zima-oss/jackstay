package cli

import (
	"fmt"
	"strings"
)

// Reference is the command reference, docs/reference/commands.md: every command's --help as one
// Markdown page, under the headings and in the order of `jst help`. It is generated (`just reference`) and checked
// against the file by cmd/jst's tests, so the page never says other than the binary.
func Reference() string {
	var page strings.Builder
	page.WriteString("# Command reference\n\n")
	page.WriteString("Every `jst` command, as its `--help` prints it, under the headings of `jst help`. This page is\n" +
		"generated from the binary by `just reference`; change a command's help in its package, not here.\n")
	for _, heading := range Grouped() {
		if len(heading.Names) == 0 {
			continue
		}
		fmt.Fprintf(&page, "\n## %s\n\n| Command | What it does |\n|---|---|\n", heading.Title)
		for _, name := range heading.Names {
			fmt.Fprintf(&page, "| [`jst %s`](#%s) | %s |\n", name, anchor("jst "+name), tableCell(commands[name].Summary))
		}
		for _, name := range heading.Names {
			entry := commands[name]
			fmt.Fprintf(&page, "\n### jst %s\n\n%s\n\n", name, sentence(entry.Summary))
			page.WriteString("```text\n" + strings.TrimRight(entry.Usage, "\n") + "\n```\n")
		}
	}
	return page.String()
}

// anchor is the id GitHub and the site give a heading: lower case, spaces made hyphens.
func anchor(heading string) string {
	return strings.ReplaceAll(strings.ToLower(heading), " ", "-")
}

// tableCell escapes what would break a Markdown table cell.
func tableCell(text string) string {
	return strings.ReplaceAll(prose(text), "|", `\|`)
}

// prose escapes the angle brackets of a summary (`<path>`), which Markdown would read as HTML; inside
// code spans they stay as written.
func prose(text string) string {
	var out strings.Builder
	inCode := false
	for _, character := range text {
		switch {
		case character == '`':
			inCode = !inCode
		case !inCode && (character == '<' || character == '>'):
			out.WriteByte('\\')
		}
		out.WriteRune(character)
	}
	return out.String()
}

// sentence is a summary as a sentence of prose: its first letter in capitals, a full stop at its end.
func sentence(summary string) string {
	if summary == "" {
		return summary
	}
	text := prose(strings.ToUpper(summary[:1]) + summary[1:])
	if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "?") && !strings.HasSuffix(text, ")") {
		text += "."
	}
	return text
}
