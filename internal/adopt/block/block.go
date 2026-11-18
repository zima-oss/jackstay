// Package block is the AGENTS block `jst adopt` writes (docs/design/adopt.md section 8): one item per
// task an agent has in hand, printed from the project's jackstay.json — an item appears only when the
// sections it needs are there and every command it names is in this jackstay — then the profile's
// rules. It also finds the block in a file, between its jackstay-adopt markers.
package block

import (
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/adopt/templates"
)

// Inputs are what the block is printed from.
type Inputs struct {
	// Config is the project's prospective jackstay.json (encoding/json's model).
	Config map[string]any
	// Has says a command of that name is in this jackstay.
	Has func(command string) bool
	// Rules are the profile's lines; LeaveOut the item IDs the profile drops.
	Rules    []string
	LeaveOut []string
}

type item struct {
	id       string
	commands []string
	wanted   func(config map[string]any) bool
	text     func(config map[string]any) string
}

func always(map[string]any) bool { return true }

func section(config map[string]any, name string) map[string]any {
	members, _ := config[name].(map[string]any)
	return members
}

func hasSection(name string) func(map[string]any) bool {
	return func(config map[string]any) bool { return section(config, name) != nil }
}

var items = []item{
	{id: "test", commands: []string{"test", "test list", "test last"}, wanted: always, text: testText},
	{id: "wait", commands: []string{"wait"}, wanted: always, text: fixed(
		"- **Wait** for a process, a file or a log line: `jst wait … --timeout S`, never a `sleep` loop.")},
	{id: "bg", commands: []string{"bg", "procs", "procs stop"}, wanted: always, text: fixed(
		"- **Run something in the background**: `jst bg --for MIN --name N -- COMMAND`, never `&` or\n" +
			"  `nohup`; `jst procs` lists them, `jst procs stop NAME` stops yours.")},
	{id: "lock", commands: []string{"lock"}, wanted: func(config map[string]any) bool {
		enabled, _ := section(config, "lock")["enabled"].(bool)
		return enabled
	}, text: fixed("- **A heavy command** others on this machine must not overlap: `jst lock -- COMMAND`.")},
	{id: "branches", commands: []string{"start", "sync", "tidy"}, wanted: always, text: fixed(
		"- **Branches**: `jst start NAME` (a branch and its worktree from the fetched default branch),\n" +
			"  `jst sync` (merges the default branch in; never rebases), `jst tidy` (what is finished;\n" +
			"  `--fix --branch NAME` removes only what you made).")},
	{id: "discard", commands: []string{"discard", "trash"}, wanted: always, text: fixed(
		"- **Drop uncommitted changes**: `jst discard PATH…` — what it drops goes to jackstay's trash\n" +
			"  (`jst trash restore`); never `git restore` or `git checkout -- PATH`.")},
	{id: "guard", commands: []string{"guard"}, wanted: always, text: guardText},
	{id: "github", commands: []string{"gh"}, wanted: func(config map[string]any) bool {
		account, _ := section(config, "github")["account"].(string)
		return account != ""
	}, text: fixed(
		"- **GitHub**: `jst gh ci-wait` / `ci-status` (a pull request's checks), `run-wait`, `pr-status`,\n" +
			"  `pr-land` (push, open, wait, squash-merge the head that was checked). jackstay acts as the account in\n" +
			"  `jackstay.json` (`github.account`).")},
	// Review only where the project says it reviews with jackstay (a `review` section, even empty):
	// a project without Codex would otherwise be told to run what exits 6.
	{id: "review", commands: []string{"review", "review check"}, wanted: hasSection("review"), text: fixed(
		"- **Review a change**: `jst review BASE..REV --task NAME` — a cold review by Codex, in rounds;\n" +
			"  `jst review check BASE..REV --task NAME` says whether it is clear.")},
	{id: "cards", commands: []string{"lint range", "cards"}, wanted: hasSection("cards"), text: cardsText},
	{id: "ids", commands: []string{"id"}, wanted: hasSection("ids"), text: idsText},
	{id: "changes", commands: []string{"changes new", "changes check"}, wanted: hasSection("changes"), text: fixed(
		"- **A user-facing change** gets a changelog entry: `jst changes new --slug S`; `jst changes check`.")},
	{id: "release", commands: []string{"release prepare"}, wanted: hasSection("release"), text: fixed(
		"- **Releases** are made by the release workflow's pull request, never a tag by hand;\n" +
			"  `jst release prepare` shows the next one.")},
	{id: "log", commands: []string{"log", "stats"}, wanted: always, text: fixed(
		"- **What happened**: `jst log --session ID` (or `--trace ID`) and `jst stats REPORT` — after a\n" +
			"  surprising failure, before asking.")},
	{id: "owner", commands: []string{"owner"}, wanted: always, text: fixed(
		"- **The owner's words**: `jst owner --grep RE` to find a ruling to quote.")},
	{id: "doctor", commands: []string{"doctor", "check", "adopt"}, wanted: always, text: fixed(
		"- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is\n" +
			"  this project up to date with jackstay).")},
}

func fixed(text string) func(map[string]any) string {
	return func(map[string]any) string { return text }
}

func testText(config map[string]any) string {
	scopes := section(section(config, "test"), "scopes")
	if len(scopes) == 0 {
		return "- **Run tests**: `jst test -- COMMAND` runs any test command, judged and recorded; only `passed`\n" +
			"  is a pass — read the last `jst test:` line. After a failure, `jst test last`, not a rerun."
	}
	text := "- **Run tests**: `jst test <scope>`; `jst test list` shows the scopes and when each is required.\n" +
		"  Only `passed` is a pass — read the last `jst test:` line. After a failure, `jst test last`, not\n" +
		"  a rerun."
	if _, gate := scopes["gate"]; gate {
		text += " Before a pull request, `jst test gate` (`--commit HEAD`: on a clean copy jackstay makes and\n" +
			"  removes)."
	}
	return text + " No scope fits: `jst test -- COMMAND`."
}

func guardText(config map[string]any) string {
	text := "- **The guard** refuses destructive commands and names what to run instead: run that, never a\n" +
		"  workaround. A line `jst guard let this command run, with a warning` means read it before going\n" +
		"  on."
	if section(config, "guard") != nil {
		text += " This project sets the guard's rules in `jackstay.json` (`guard`)."
	}
	return text
}

func cardsText(config map[string]any) string {
	branch, _ := section(config, "cards")["branch"].(string)
	if branch == "" {
		branch = "main"
	}
	return "- **Commits to the default branch** carry `Card: <ID>` or `Card: none (why)`; check with\n" +
		"  `jst lint range origin/" + branch + "..HEAD` before pushing; `jst cards ID` shows what landed per card."
}

func idsText(config map[string]any) string {
	text := "- **Numbers** come from `jst id next SERIES`, never by hand"
	if section(config, "records") != nil {
		return text + "; `jst new SERIES --slug S` makes a\n  record, `jst view SERIES` prints their table."
	}
	return text + "."
}

// ItemIDs are the IDs a profile's `agents.leave_out` may name.
func ItemIDs() []string {
	ids := make([]string, len(items))
	for index, entry := range items {
		ids[index] = entry.id
	}
	return ids
}

// Render is the block's text between the markers, ending in a newline.
func Render(inputs Inputs) string {
	var out strings.Builder
	out.WriteString("## jst: the tools for working on this project\n\n")
	out.WriteString("`jst` is installed on this machine. Reach for it before a hand-written shell loop:\n")
	out.WriteString("`jst <command> --help` gives a command's details, `jst help` lists every command.\n\n")
	for _, entry := range items {
		if slices.Contains(inputs.LeaveOut, entry.id) || !entry.wanted(inputs.Config) {
			continue
		}
		present := true
		for _, command := range entry.commands {
			if !inputs.Has(command) {
				present = false
				break
			}
		}
		if !present {
			continue
		}
		out.WriteString(entry.text(inputs.Config))
		out.WriteString("\n")
	}
	if len(inputs.Rules) > 0 {
		out.WriteString("\n### This project's organisation\n\n")
		for _, rule := range inputs.Rules {
			out.WriteString("- " + rule + "\n")
		}
	}
	return out.String()
}

const (
	beginPrefix = "<!-- jackstay-adopt: begin agents-block"
	// End is the line that closes the block.
	End = "<!-- jackstay-adopt: end agents-block -->"
)

// Begin is the line that opens a block whose text is body.
func Begin(body string) string {
	return beginPrefix + " sha256:" + templates.Hash(body) +
		" — written by `jst adopt` from jackstay.json and the profile; change those, not this text -->"
}

// Marked is the block with its markers, ending in a newline.
func Marked(body string) string {
	return Begin(body) + "\n" + body + End + "\n"
}

// Kind is what Locate found.
type Kind int

// What a file can hold.
const (
	None      Kind = iota // no block
	Current               // a marked block: Start and Stop are the begin and end lines
	Malformed             // markers that do not make one block
)

// Found is where the block is in a file's lines.
type Found struct {
	Kind        Kind
	Start, Stop int
	// Intact says a Current block's text still matches its begin line's hash.
	Intact bool
	// Body is a Current block's text between the markers.
	Body string
}

// Locate finds the block in a file's lines (the text split at "\n").
func Locate(lines []string) Found {
	var begins, ends []int
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, beginPrefix):
			begins = append(begins, index)
		case trimmed == End:
			ends = append(ends, index)
		}
	}
	switch {
	case len(begins) == 1 && len(ends) == 1 && begins[0] < ends[0]:
		start, stop := begins[0], ends[0]
		body := strings.Join(lines[start+1:stop], "\n") + "\n"
		if start+1 == stop {
			body = ""
		}
		return Found{Kind: Current, Start: start, Stop: stop, Body: body, Intact: hashOf(lines[start]) == templates.Hash(body)}
	case len(begins) > 0 || len(ends) > 0:
		return Found{Kind: Malformed}
	}
	return Found{Kind: None}
}

func hashOf(beginLine string) string {
	_, after, found := strings.Cut(beginLine, "sha256:")
	if !found || len(after) < 12 {
		return ""
	}
	return after[:12]
}
