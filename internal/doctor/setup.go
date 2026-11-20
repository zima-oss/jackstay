package doctor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/zima-oss/jackstay/internal/adopt"
)

const setupUsage = "usage: jst setup [--plan] [--json] [--chat] [--prefix DIR] | jst setup --yes --select path|sandbox [--prefix DIR] | jst setup --autonomy MODE"

const setupHelp = setupUsage + `

Plan repairs from doctor findings. Available repairs add the installed bin folder to a supported
POSIX shell profile and allow jackstay's state folder in Claude Code's enabled sandbox.
  --plan          print the plan without changing files (also the nonterminal default)
  --json          print the plan as JSON; implies --plan
  --prefix DIR    inspect an install under DIR (default: this binary's own prefix)
  --chat          show session chat and agent-engine setup steps
  --yes           apply only explicitly selected repair IDs; requires --select
  --select ID     select path or sandbox; repeat to select both
  --autonomy MODE keep merge-when-green or prepare as this machine's answer to who merges

Once per machine, a terminal is also asked who merges a project's changes: the agent when the
required checks pass, or a person. ` + "`jst adopt`" + ` offers that answer first when it writes a project's
land section; the project's jackstay.json decides.

Mail steps report detected engines and their current integration limits. They are guidance only;
setup does not install an unverified bridge or claim active-turn delivery. Without options, a
terminal offers apply, show and skip. An existing profile is copied to a
unique backup beside it before replacement. Unsupported shells and conflicting profiles are
left for the owner. Open a new shell after applying a PATH repair.

exit: 0 planned or applied; 1 an applicable repair failed; 2 the request is wrong.

example: jst setup --plan --json
`

// SetupItem is a doctor finding and the action, if jackstay can safely apply one.
type SetupItem struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Text        string `json:"text"`
	Fix         string `json:"fix"`
	Path        string `json:"path,omitempty"`
	Add         string `json:"add,omitempty"`
	Diff        string `json:"diff,omitempty"`
	original    []byte
	replacement []byte
	mode        os.FileMode
}

// SetupPlan retains doctor's manual findings; setup never guesses repairs for them.
type SetupPlan struct {
	Items []SetupItem `json:"items"`
}

func checkPath(report *Report, inputs Inputs) {
	if !inputs.CheckPath {
		return
	}
	installed := filepath.Join(inputs.Prefix, "bin", "jst")
	if sameExecutableFolder(inputs.ActiveJst, installed) {
		report.Facts["path_jst"] = inputs.ActiveJst
		return
	}
	active := "no jst on PATH"
	if inputs.ActiveJst != "" {
		active = inputs.ActiveJst + " comes first on PATH"
	}
	report.problem("path-not-first", active+"; this install is "+installed+".",
		"put "+filepath.Join(inputs.Prefix, "bin")+" first on PATH in your shell profile; `jst setup --plan` shows an applicable repair")
}

func sameExecutableFolder(active, installed string) bool {
	if active == "" || filepath.Base(active) != "jst" {
		return false
	}
	activeFolder, activeErr := filepath.EvalSymlinks(filepath.Dir(active))
	installedFolder, installedErr := filepath.EvalSymlinks(filepath.Dir(installed))
	return activeErr == nil && installedErr == nil && activeFolder == installedFolder
}

func shellProfile(home, shell string) string {
	return shellProfileForOS(home, shell, runtime.GOOS)
}

func shellProfileForOS(home, shell, operatingSystem string) string {
	switch filepath.Base(shell) {
	case "zsh":
		return filepath.Join(home, ".zshrc")
	case "bash":
		if operatingSystem == "darwin" {
			for _, name := range []string{".bash_profile", ".bash_login", ".profile"} {
				path := filepath.Join(home, name)
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					return path
				}
			}
			return filepath.Join(home, ".bash_profile")
		}
		return filepath.Join(home, ".bashrc")
	case "sh", "dash", "ksh":
		return filepath.Join(home, ".profile")
	default:
		return ""
	}
}

func quotedShellWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func planSetup(report Report, home, shell string) SetupPlan {
	plan := SetupPlan{Items: []SetupItem{}}
	for _, finding := range report.Problems {
		item := SetupItem{ID: finding.Kind, Status: "manual", Text: finding.Text, Fix: finding.Fix}
		if finding.Kind == "path-not-first" {
			item.ID = "path"
			item.Path = shellProfile(home, shell)
			if item.Path != "" {
				prefix, _ := report.Facts["prefix"].(string)
				launcher := filepath.Join(prefix, "bin", "jst")
				launcherInfo, launcherErr := os.Stat(launcher)
				if launcherErr != nil || !launcherInfo.Mode().IsRegular() || launcherInfo.Mode().Perm()&0o111 == 0 {
					item.Fix = "repair the installed jst launcher before changing PATH: " + launcher
					plan.Items = append(plan.Items, item)
					continue
				}
				item.Add = "export PATH=" + quotedShellWord(filepath.Join(prefix, "bin")) + ":\"$PATH\""
				item.Status = "ready"
				item.Fix = "append the shown line to " + item.Path + "; open a new shell"
				if content, _, err := readProfile(item.Path); err != nil {
					item.Status = "manual"
					item.Fix = "inspect " + item.Path + ": " + err.Error()
				} else if bytes.Contains(content, []byte("# jackstay setup: PATH\n"+item.Add+"\n")) {
					item.Status = "done"
					item.Fix = "this profile already has the PATH entry; open a new shell"
				} else if bytes.Contains(content, []byte("# jackstay setup: PATH")) {
					item.Status = "manual"
					item.Fix = "this profile has a different jackstay PATH entry; inspect it"
				}
			} else {
				item.Fix = "put the install's bin folder first on PATH in your shell's configuration"
			}
		}
		if finding.Kind == "sandbox-blocks-state" {
			item.ID = "sandbox"
			item.Path, _ = report.Facts["settings"].(string)
			stateHome, _ := report.Facts["home"].(string)
			if item.Path != "" && stateHome != "" {
				original, replacement, mode, err := prepareSandbox(item.Path, stateHome)
				if err == nil {
					item.Status = "ready"
					item.Fix = "add the state folder to sandbox.filesystem.allowWrite in " + item.Path
					item.Add = stateHome
					item.Diff = settingsDiff(item.Path, original, replacement)
					item.original, item.replacement, item.mode = original, replacement, mode
				} else {
					item.Fix = finding.Fix + "; setup cannot apply it: " + err.Error()
				}
			}
		}
		plan.Items = append(plan.Items, item)
	}
	return plan
}

// RunSetup is jst setup. Mutations require an interactive choice or --yes with a selected ID.
func RunSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	planOnly, asJSON, yes, chat, prefix, autonomy := false, false, false, false, "", ""
	selected := map[string]bool{}
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--plan":
			planOnly = true
		case "--json":
			asJSON, planOnly = true, true
		case "--yes":
			yes = true
		case "--chat":
			chat = true
		case "--select", "--prefix", "--autonomy":
			if index+1 >= len(args) {
				fmt.Fprintln(stderr, setupUsage)
				return 2
			}
			index++
			switch args[index-1] {
			case "--select":
				selected[args[index]] = true
			case "--prefix":
				prefix = args[index]
			default:
				autonomy = args[index]
			}
		default:
			fmt.Fprintln(stderr, setupUsage)
			return 2
		}
	}
	if (yes && len(selected) == 0) || (!yes && len(selected) != 0) || (planOnly && yes) || (chat && yes) {
		fmt.Fprintln(stderr, setupUsage)
		return 2
	}
	if autonomy != "" {
		if len(args) != 2 {
			fmt.Fprintln(stderr, setupUsage)
			return 2
		}
		if err := adopt.SaveMachineAutonomy(autonomy); err != nil {
			fmt.Fprintf(stderr, "jst setup: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "jst setup: jst adopt offers land.autonomy %s first on this machine\n", autonomy)
		return 0
	}
	for identifier := range selected {
		if identifier != "path" && identifier != "sandbox" {
			fmt.Fprintf(stderr, "jst setup: unknown repair %q\n", identifier)
			return 2
		}
	}
	if prefix != "" {
		var err error
		prefix, err = filepath.Abs(prefix)
		if err != nil {
			fmt.Fprintf(stderr, "jst setup: prefix: %v\n", err)
			return 2
		}
	}
	inputs := RealInputs(prefix)
	// Settings lives under ~/.claude; the profile belongs directly under account home.
	plan := planSetup(Diagnose(inputs), filepath.Dir(filepath.Dir(inputs.Settings)), os.Getenv("SHELL"))
	plan.Items = append(plan.Items, autonomyItem(adopt.MachineAutonomy()))
	if chat {
		plan.Items = append(plan.Items, chatEngineSetupItems(exec.LookPath)...)
	}
	if asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(plan)
		return 0
	}
	printSetupPlan(stdout, plan)
	if planOnly || (!yes && (!terminalInput(stdin) || !terminalOutput(stdout))) {
		return 0
	}
	reader := bufio.NewReader(stdin)
	if err := askMachineAutonomy(plan, reader, stdout, adopt.SaveMachineAutonomy); err != nil {
		fmt.Fprintf(stderr, "jst setup: autonomy: %v\n", err)
		return 1
	}
	if chat {
		promptChatEngineSetup(plan, reader, stdout)
	}
	return executeSetup(plan, yes, selected, reader, stdout, stderr)
}

// autonomyItem is this machine's answer to who merges (docs/design/land.md section 5): asked once,
// then offered first by `jst adopt`.
func autonomyItem(saved string, err error) SetupItem {
	item := SetupItem{ID: "autonomy", Status: "ask", Text: "who merges a project's changes, offered first by `jst adopt`: not answered on this machine",
		Fix: "answer the question at a terminal (`jst setup`), or `jst setup --autonomy merge-when-green|prepare`"}
	switch {
	case err != nil:
		item.Status, item.Text = "manual", "this machine's answer to who merges cannot be read: "+err.Error()
		item.Fix = "fix or remove the file named, then `jst setup --autonomy merge-when-green|prepare`"
	case saved != "":
		item.Status, item.Text = "done", "`jst adopt` offers land.autonomy "+saved+" first on this machine"
		item.Fix = "`jst setup --autonomy merge-when-green|prepare` changes it; a project's jackstay.json decides"
	}
	return item
}

// askMachineAutonomy asks adopt's question when the plan says it is not answered, and keeps the answer.
func askMachineAutonomy(plan SetupPlan, reader *bufio.Reader, stdout io.Writer, save func(string) error) error {
	for _, item := range plan.Items {
		if item.ID != "autonomy" || item.Status != "ask" {
			continue
		}
		answer, err := adopt.AskAutonomy(reader, stdout, "")
		if err != nil {
			return err
		}
		if err := save(answer); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "jst setup: jst adopt offers land.autonomy %s first on this machine\n", answer)
	}
	return nil
}

func chatEngineSetupItems(lookPath func(string) (string, error)) []SetupItem {
	engines := []struct {
		name, identifier, capability, steps string
	}{
		{"codex", "chat-codex", "poll-only", "Enable chat.codex_bridge in jackstay.json. Start jst chat codex-bridge with the owner-managed app-server socket, thread ID and session name. Use the same local session token as jst chat serve. Stop the bridge or restore the host configuration to roll back."},
		{"claude", "chat-claude", "next-boundary with hook", "Enable chat.claude_code in jackstay.json. Register jst chat serve --name NAME as a local MCP server. In Claude Code settings, add command hooks that run jst chat claude-hook --name NAME --event UserPromptSubmit and --event PostToolBatch. Export one JACKSTAY_CHAT_SESSION_TOKEN to both the MCP server and hooks. The hook announces pending messages; Claude reads them with chat.inbox and acknowledges with chat.ack. Remove the hooks to roll back."},
	}
	items := make([]SetupItem, 0, len(engines)+1)
	for _, engine := range engines {
		_, err := lookPath(engine.name)
		detection := "not found on PATH"
		if err == nil {
			detection = "detected on PATH"
		}
		items = append(items, SetupItem{
			ID: engine.identifier, Status: "manual",
			Text: fmt.Sprintf("%s session chat (%s; current capability: %s)", engine.name, detection, engine.capability),
			Fix:  engine.steps,
		})
	}
	items = append(items, SetupItem{
		ID: "chat-other", Status: "manual",
		Text: "other MCP host session chat (current capability: poll-only)",
		Fix:  "Register jst chat serve --name NAME as a local stdio server through an owner-controlled launcher. For active-turn delivery, connect a host adapter with addressed injection and an acknowledgment probe. Remove that local server registration and restore the saved host configuration to roll back.",
	})
	return items
}

func promptChatEngineSetup(plan SetupPlan, stdin io.Reader, stdout io.Writer) {
	reader := bufio.NewReader(stdin)
	for _, item := range plan.Items {
		if !strings.HasPrefix(item.ID, "chat-") {
			continue
		}
		fmt.Fprintf(stdout, "%s: [s]how integration steps, s[k]ip? ", item.ID)
		answer, _ := reader.ReadString('\n')
		if strings.EqualFold(strings.TrimSpace(answer), "s") || strings.EqualFold(strings.TrimSpace(answer), "show") {
			fmt.Fprintf(stdout, "%s: %s\n", item.ID, item.Fix)
		}
	}
}

func executeSetup(plan SetupPlan, yes bool, selected map[string]bool, stdin io.Reader, stdout, stderr io.Writer) int {
	reader := bufio.NewReader(stdin)
	for _, item := range plan.Items {
		if item.Status == "done" {
			continue
		}
		if item.Status != "ready" {
			if selected[item.ID] {
				fmt.Fprintf(stderr, "jst setup: %s needs manual attention: %s\n", item.ID, item.Fix)
				return 1
			}
			continue
		}
		apply := selected[item.ID]
		if !yes {
			apply = promptSetup(reader, stdout, item)
		}
		if apply {
			var backup string
			var err error
			if item.ID == "sandbox" {
				backup, err = replaceWithBackup(item.Path, item.original, item.replacement, item.mode)
			} else {
				backup, err = applyProfile(item.Path, item.Add)
			}
			if err != nil {
				fmt.Fprintf(stderr, "jst setup: %s: %v\n", item.ID, err)
				return 1
			}
			if backup != "" {
				fmt.Fprintf(stdout, "jst setup: saved previous profile at %s\n", backup)
			}
			fmt.Fprintf(stdout, "jst setup: updated %s; open a new shell or Claude Code session\n", item.Path)
		}
	}
	return 0
}

func terminalInput(input io.Reader) bool {
	file, ok := input.(*os.File)
	return ok && terminalFile(file)
}

func terminalOutput(output io.Writer) bool {
	file, ok := output.(*os.File)
	return ok && terminalFile(file)
}

func terminalFile(file *os.File) bool {
	return isTerminal(file.Fd())
}

func printSetupPlan(output io.Writer, plan SetupPlan) {
	if len(plan.Items) == 0 {
		fmt.Fprintln(output, "jst setup: nothing to change")
		return
	}
	for _, item := range plan.Items {
		fmt.Fprintf(output, "%s [%s]: %s\n  fix: %s\n", item.ID, item.Status, item.Text, item.Fix)
		if item.Status == "ready" {
			if item.Diff != "" {
				fmt.Fprint(output, item.Diff)
			} else {
				fmt.Fprintf(output, "  --- %s\n  +++ %s\n  +# jackstay setup: PATH\n  +%s\n", item.Path, item.Path, item.Add)
			}
		}
	}
}

func promptSetup(reader *bufio.Reader, output io.Writer, item SetupItem) bool {
	for {
		fmt.Fprintf(output, "%s: [a]pply, [s]how, s[k]ip? ", item.ID)
		answer, err := reader.ReadString('\n')
		if err != nil && answer == "" {
			return false
		}
		switch strings.TrimSpace(strings.ToLower(answer)) {
		case "a", "apply":
			return true
		case "s", "show":
			if item.Diff != "" {
				fmt.Fprint(output, item.Diff)
			} else {
				fmt.Fprintf(output, "--- %s\n+++ %s\n+# jackstay setup: PATH\n+%s\n", item.Path, item.Path, item.Add)
			}
		case "k", "skip":
			return false
		}
	}
}

func readProfile(path string) ([]byte, os.FileMode, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0o600, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0o600, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	const limit = 1 << 20
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, 0, fmt.Errorf("profile is not a regular file smaller than 1 MiB")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if len(content) > limit {
		return nil, 0, fmt.Errorf("profile exceeded 1 MiB while reading")
	}
	return content, info.Mode().Perm(), err
}

func applyProfile(path, line string) (string, error) {
	original, mode, err := readProfile(path)
	if err != nil {
		return "", err
	}
	if bytes.Contains(original, []byte("# jackstay setup: PATH")) {
		return "", fmt.Errorf("profile already contains a jackstay PATH entry; inspect it")
	}
	previous := append([]byte(nil), original...)
	if !bytes.Equal(previous, []byte{}) && previous[len(previous)-1] != '\n' {
		previous = append(previous, '\n')
	}
	updated := append(append([]byte(nil), previous...), []byte("# jackstay setup: PATH\n"+line+"\n")...)
	return replaceWithBackup(path, original, updated, mode)
}

// replaceWithBackup swaps one regular user file only if it still matches the planned bytes.
func replaceWithBackup(path string, original, updated []byte, mode os.FileMode) (string, error) {
	current, currentMode, err := readProfile(path)
	if err != nil || !bytes.Equal(current, original) || currentMode != mode {
		return "", fmt.Errorf("file changed while planning; inspect it and run setup again")
	}
	backup := ""
	_, existsErr := os.Lstat(path)
	if existsErr == nil {
		file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".jackstay-backup-")
		if err != nil {
			return "", err
		}
		backup = file.Name()
		if err = file.Chmod(0o600); err == nil {
			_, err = file.Write(original)
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return backup, err
		}
	}
	file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".jackstay-new-")
	if err != nil {
		return backup, err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(updated)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return backup, err
	}
	current, currentMode, err = readProfile(path)
	if err != nil || !bytes.Equal(current, original) || currentMode != mode {
		return backup, fmt.Errorf("profile changed while planning; inspect it and run setup again")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return backup, err
	}
	return backup, nil
}
