// Package block is the AGENTS block `jst adopt` writes (docs/design/adopt.md section 8): one item per
// task an agent has in hand, printed from the project's jackstay.json — an item appears only when the
// sections it needs are there and every command it names is in this jackstay — then the profile's
// rules. It also finds the block in a file, between its jackstay-adopt markers.
package block

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/adopt/templates"
	scopes "github.com/zima-oss/jackstay/internal/testing"
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
	id string
	// first puts the item in the block's first list: a substitution, "instead of X, run Y", which
	// agents most need and least follow (measured over 13 sessions: raw test runners 675 times, gh
	// 119, sleep 55, git worktree 30). The list keeps the order of items, the most frequent first;
	// landing (`jst land`, the substitute for `gh pr merge` and a raw push) has its own item.
	first    bool
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
	{id: "test", commands: []string{"test", "test list", "test last"}, wanted: always, first: true, text: testText},
	{id: "test-split", commands: []string{"test", "test uncovered"}, wanted: func(config map[string]any) bool {
		return len(coveredScopes(config)) > 0
	}, first: true, text: splitText},
	{id: "checks", commands: []string{"checks"}, wanted: func(config map[string]any) bool {
		account, _ := section(config, "github")["account"].(string)
		return account != ""
	}, first: true, text: fixed(
		"- **Instead of `gh pr checks --watch` or `gh run watch`**, run `jst checks wait` or\n" +
			"  `jst checks run-wait`; `jst checks status` reads them once. jackstay acts as `github.account`.")},
	{id: "wait", commands: []string{"wait"}, wanted: always, first: true, text: fixed(
		"- **Instead of `sleep N`**, run `jst wait … --timeout S`: until a process ends, a file appears or a\n" +
			"  log line matches.")},
	{id: "branches", commands: []string{"start", "sync", "tidy"}, wanted: always, first: true, text: fixed(
		"- **Instead of `git worktree add` or `remove`**, run `jst start NAME` and, once it is merged,\n" +
			"  `jst tidy --fix --branch NAME`. `jst sync` merges the default branch in; never rebase.")},
	{id: "bg", commands: []string{"bg", "procs", "procs stop"}, wanted: always, first: true, text: fixed(
		"- **Instead of `&` or `nohup`**, run `jst bg --for MIN --name N -- COMMAND`; `jst procs stop N`\n" +
			"  stops it.")},
	{id: "discard", commands: []string{"discard", "trash"}, wanted: always, first: true, text: fixed(
		"- **Instead of `git restore` or `git checkout -- PATH`**, run `jst discard PATH…`;\n" +
			"  `jst trash restore` brings the changes back.")},
	{id: "evidence", commands: []string{"receipts export", "receipts verify"}, wanted: hasSection("receipts"), text: fixed(
		"- **Share a counted pass**: `jst receipts export KEY`. Check one with `jst receipts verify`, taking\n" +
			"  the expected digest, revision and key from a trusted source, never from the receipt.")},
	{id: "lock", commands: []string{"lock"}, wanted: func(config map[string]any) bool {
		enabled, _ := section(config, "lock")["enabled"].(bool)
		return enabled
	}, text: fixed("- **A heavy command** others on this machine must not overlap: `jst lock -- COMMAND`.")},
	{id: "guard", commands: []string{"guard"}, wanted: always, text: guardText},
	{id: "land", commands: []string{"land"}, wanted: hasSection("land"), text: landText},
	// Advertise review where the project declares a review section.
	{id: "review", commands: []string{"review", "review check"}, wanted: hasSection("review"), text: fixed(
		"- **Review**, outside the agent sandbox (it records the round): `jst review BASE..REV --task NAME`\n" +
			"  (the project's reviewers; `--reviewer claude-code` or `codex,claude-code` for others);\n" +
			"  `--background` returns at once and the result comes as a chat\n" +
			"  event; `jst review check BASE..REV --task NAME` says if it is clear.\n" +
			"  Before calling a finding a blocker, check its rating: an unusual one blocks only with an ordinary-work path.")},
	{id: "cards", commands: []string{"lint range", "cards"}, wanted: hasSection("cards"), text: cardsText},
	{id: "ids", commands: []string{"id"}, wanted: hasSection("ids"), text: idsText},
	{id: "changes", commands: []string{"changes new", "changes check"}, wanted: hasSection("changes"), text: fixed(
		"- **A user-facing change** gets a changelog entry: `jst changes new --slug S`; `jst changes check`.")},
	{id: "release", commands: []string{"release prepare"}, wanted: hasSection("release"), text: fixed(
		"- **Releases** are made by the release workflow's pull request, never a tag by hand;\n" +
			"  `jst release prepare` shows the next one.")},
	{id: "log", commands: []string{"log", "stats"}, wanted: always, text: fixed(
		"- **After a surprising failure**, read `jst log --session ID` (or `--trace ID`) and\n" +
			"  `jst stats REPORT` before asking.")},
	{id: "status", commands: []string{"status"}, wanted: always, text: fixed(
		"- **What is going on here**: `jst status` shows worktrees, agent sessions, recent events, chat,\n" +
			"  the brief and background jobs; `jst status PATH…` gives one row per repository.")},
	{id: "context-brief", commands: []string{"brief save", "brief show"}, wanted: always, text: fixed(
		"- **Carry task context through compaction** with `jst brief save` and `jst brief show`. Keep the brief short and free of secrets.")},
	{id: "owner", commands: []string{"owner"}, wanted: always, text: fixed(
		"- **The owner's words**: `jst owner --grep RE` to find a ruling to quote.")},
	{id: "profiles", commands: []string{"profile export", "profile verify"}, wanted: hasSection("adopt"), text: fixed(
		"- **Share an adoption profile** with `jst profile export`; verify its digest before adopting it.")},
	{id: "chat", commands: []string{"chat serve", "setup"}, wanted: func(config map[string]any) bool {
		return section(config, "chat")["enabled"] == true
	}, text: fixed(
		"- **Coordinate with other sessions**: `jst setup --chat` shows the MCP host setup. Use chat tools\n" +
			"  to send a request, read your inbox and acknowledge messages you process.")},
	{id: "chat-codex", commands: []string{"chat codex-bridge"}, wanted: func(config map[string]any) bool {
		chat := section(config, "chat")
		return chat["enabled"] == true && section(chat, "codex_bridge")["enabled"] == true
	}, text: fixed(
		"- **Deliver chat to managed Codex**: `jst chat codex-bridge` uses the owner's local socket, thread and session.\n" +
			"  Use `chat.ack` after processing a message.")},
	{id: "chat-claude", commands: []string{"chat claude-hook"}, wanted: func(config map[string]any) bool {
		chat := section(config, "chat")
		return chat["enabled"] == true && section(chat, "claude_code")["enabled"] == true
	}, text: fixed(
		"- **Check chat at a Claude Code boundary** with the owner-configured `jst chat claude-hook`.\n" +
			"  When notified, use the MCP inbox and acknowledge each processed message.")},
	{id: "doctor", commands: []string{"doctor", "check", "adopt", "setup"}, wanted: always, text: fixed(
		"- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is\n" +
			"  this project up to date with jackstay). For machine PATH repairs, preview `jst setup --plan`.")},
}

func fixed(text string) func(map[string]any) string {
	return func(map[string]any) string { return text }
}

func testText(config map[string]any) string {
	scopes := section(section(config, "test"), "scopes")
	if len(scopes) == 0 {
		return "- **Instead of `go test`, `pytest`, `npm test` or another runner**, run `jst test -- COMMAND`.\n" +
			"  Only `passed` is a pass: read the last `jst test:` line. After a failure, `jst test last`\n" +
			"  (a run not logged names its output file).\n" + evidenceText
	}
	text := "- **Instead of `go test`, `pytest`, `npm test` or another runner**, run `jst test SCOPE`\n" +
		"  (`jst test list` names them and when each is due). Only `passed` is a pass: read the last\n" +
		"  `jst test:` line. After a failure, `jst test last`, not a rerun (a run not logged names its\n" +
		"  output file)."
	if _, gate := scopes["gate"]; gate && !splitsChanges(config) {
		text += "\n  Before a pull request: `jst test gate --commit HEAD` (on a clean copy of the commit)."
	}
	if section(section(config, "test"), "selection") != nil {
		text += "\n  Preview path-based selection with `jst test plan changed --base REF`."
	}
	return text + " No scope fits: `jst test -- COMMAND`.\n" + evidenceText
}

// systemNames are the systems a scope's `ci` names, as people call them.
var systemNames = map[string]string{"linux": "Linux", "darwin": "macOS"}

// coveredScopes are the nodes with `run` that CI covers, through their own `ci` or as a dep of a
// scope that has one, each with the systems it is covered on (docs/design/land.md section 7.4).
func coveredScopes(config map[string]any) map[string][]string {
	// The graph reads the section as encoding/json gives it; a Go literal is brought to that form.
	var test map[string]any
	if data, err := json.Marshal(section(config, "test")); err != nil || json.Unmarshal(data, &test) != nil {
		return nil
	}
	return scopes.CoveredSystems(test)
}

// splitsChanges says whether `jst test changed` can split a change between here and CI: some
// scope is covered and the project selects scopes by path (`test.selection`).
func splitsChanges(config map[string]any) bool {
	return len(coveredScopes(config)) > 0 && section(section(config, "test"), "selection") != nil
}

// splitText states where tests run (docs/design/land.md section 7.4): what CI runs, on which
// systems, and what an agent runs before a pull request (when the project selects by path) and
// before an install or a tag.
func splitText(config map[string]any) string {
	covered := coveredScopes(config)
	var order []string
	groups := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(covered)) {
		var words []string
		for _, system := range covered[name] {
			word, known := systemNames[system]
			if !known {
				word = system
			}
			words = append(words, word)
		}
		where := strings.Join(words, " and ")
		if _, seen := groups[where]; !seen {
			order = append(order, where)
		}
		groups[where] = append(groups[where], name)
	}
	parts := make([]string, 0, len(order))
	for _, where := range order {
		parts = append(parts, strings.Join(groups[where], ", ")+" on "+where)
	}
	text := "- **Where tests run**: CI runs " + strings.Join(parts, "; ") + " and is the gate. "
	// Without `test.selection`, `jst test changed` cannot split a change: the test item's gate line
	// says what to run before a pull request.
	if splitsChanges(config) {
		text += "Before a pull request: `jst test changed`. "
	}
	return wrap(text + "Before an install or a tag: `jst test uncovered`.")
}

// wrap breaks an item's text at spaces outside code spans into lines of at most 100 characters,
// the later ones indented by two spaces.
func wrap(text string) string {
	var words []string
	for _, field := range strings.Fields(text) {
		if last := len(words) - 1; last >= 0 && strings.Count(words[last], "`")%2 == 1 {
			words[last] += " " + field
			continue
		}
		words = append(words, field)
	}
	var lines []string
	line := ""
	for _, word := range words {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) > 100:
			lines = append(lines, line)
			line = "  " + word
		default:
			line += " " + word
		}
	}
	return strings.Join(append(lines, line), "\n")
}

// evidenceText says where the commands that record evidence run (#59).
const evidenceText = "  `jst test` records evidence the agent sandbox may only read: run it outside the sandbox."

func guardText(config map[string]any) string {
	text := "- **When the guard refuses** a command, run what it names instead, never a workaround. Read a\n" +
		"  `jst guard let this command run, with a warning` line before going on."
	if section(config, "guard") != nil {
		text += " Its rules: `jackstay.json` (`guard`)."
	}
	return text
}

// landText follows land.autonomy: only merge-when-green lets the agent merge; any other value,
// or none, is read as prepare.
func landText(config map[string]any) string {
	if autonomy, _ := section(config, "land")["autonomy"].(string); autonomy == "merge-when-green" {
		return "- **Land a change**: `jst land --title T` pushes, opens the change request, waits for the required\n" +
			"  checks and merges. It stops at `jst land: ready for a person` for `jackstay.json`, CI workflows,\n" +
			"  `.claude/settings.json` and `land.human_paths`: say so and stop; never merge on the host\n" +
			"  (`gh pr merge` is refused), never run `--confirm`."
	}
	return "- **Land a change**: `jst land --title T` pushes, opens the change request and waits for the\n" +
		"  required checks; a person merges. At `jst land: ready for a person`, say so and stop; never merge\n" +
		"  on the host (`gh pr merge` is refused), never run `--confirm`."
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
	out.WriteString("`jst` is installed here; use it before a hand-written shell loop. `jst help` lists the commands,\n")
	out.WriteString("`jst <command> --help` gives a command's flags, an example and its exit codes.\n\n")
	out.WriteString("### Instead of a hand-written command\n\n")
	for _, first := range []bool{true, false} {
		if !first {
			out.WriteString("\n### The other tools\n\n")
		}
		for _, entry := range items {
			if entry.first == first {
				writeItem(&out, entry, inputs)
			}
		}
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

// writeItem writes an item when the project wants it and this jackstay has every command it names.
func writeItem(out *strings.Builder, entry item, inputs Inputs) {
	if slices.Contains(inputs.LeaveOut, entry.id) || !entry.wanted(inputs.Config) {
		return
	}
	for _, command := range entry.commands {
		if !inputs.Has(command) {
			return
		}
	}
	out.WriteString(entry.text(inputs.Config))
	out.WriteString("\n")
}

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
