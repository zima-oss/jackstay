package adopt

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zima-oss/jackstay/internal/adopt/block"
	"github.com/zima-oss/jackstay/internal/choices"
	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/version"
)

const usage = `usage: jst adopt [--plan] [--settings-only] [--profile PATH] [--profile-sha256 DIGEST] [--answer ID=VALUE]... [--json] [DIR]
       jst adopt --apply [--accept ID]... [--pin] [--settings-only] [--profile PATH] [--profile-sha256 DIGEST] [--answer ID=VALUE]... [DIR]
       jst adopt --block [--profile PATH] [--profile-sha256 DIGEST] [DIR]

Works on the project around DIR (default: here) and changes nothing else; what it must not decide
is a conflict.
  --plan           what would change and the conflicts left; writes nothing (the default)
  --apply          write the changes; with a conflict left, nothing is written
  --accept ID      with --apply: overwrite what conflict ID protects, when its settlement says
                   --accept (repeatable)
  --pin            with --apply: set adopt.pin, the jst release the workflows download, to this one
  --settings-only  plan or write jackstay.json alone; land it in its own pull request, then
                   --apply again on the merged base
  --profile PATH   an organisation's profile; --apply records it in jackstay.json
  --profile-sha256 DIGEST  the exact bundle bytes to accept; recorded by --apply
  --answer ID=VALUE  answer a question below without being asked; written over the project's
                   value (repeatable)
  --json           the plan as one JSON object
  --block          print the AGENTS block this project gets; writes nothing

The questions, recommended answer first. With --apply on a terminal, adopt asks each one
jackstay.json does not answer, offering this machine's answer from jst setup; without a terminal
it writes none and the plan says what applies:
  reviewer   codex, claude-code, or codex,claude-code: who runs jst review (review.use)
  chat       off or on: the agent chat here (chat.enabled; not asked before a pin of 0.6.0)

exit: 0 adopted (--plan: nothing to change, no conflict; --apply: written); 1 conflicts left;
2 usage, or jackstay.json or the profile cannot be read; 3 (--plan) changes pending, no conflict.

example: jst adopt --plan --json`

func init() {
	cli.Register(cli.Command{Name: "adopt", Summary: "bring jackstay.json, the AGENTS block, the guard hook and workflows to jackstay's reference",
		Usage: usage, Run: Run})
}

// Run is `jst adopt`.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options, mode, asJSON, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "jst adopt: %v\n%s\n", err, strings.SplitN(usage, "\n\n", 2)[0])
		return 2
	}
	if store, err := choices.MachineStore(); err == nil {
		// Unreadable machine defaults only lose the offer; the recommended answer stands.
		options.Defaults, _ = store.Load()
	}
	if mode == "apply" && terminal(stdin) && terminal(stdout) {
		asker := choices.TerminalAsker{In: bufio.NewReader(stdin), Out: stdout}
		options.Ask = asker.Ask
	}
	if mode == "block" {
		body, err := BlockOf(options)
		if err != nil {
			fmt.Fprintf(stderr, "jst adopt: %v\n", err)
			return 2
		}
		_, _ = io.WriteString(stdout, body)
		return 0
	}
	plan, err := Make(options)
	if err != nil {
		fmt.Fprintf(stderr, "jst adopt: %v\n", err)
		return 2
	}
	if mode == "apply" {
		if err := plan.Apply(); err != nil {
			if errors.Is(err, ErrConflicts) {
				writeText(stdout, plan)
				fmt.Fprintf(stderr, "jst adopt: %d conflict(s) left: nothing written\n", len(plan.Conflicts))
				return 1
			}
			fmt.Fprintf(stderr, "jst adopt: %v\n", err)
			return 2
		}
		writeApplied(stdout, plan, options.SettingsOnly)
		return 0
	}
	if asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(plan)
	} else {
		writeText(stdout, plan)
	}
	switch {
	case len(plan.Conflicts) > 0:
		return 1
	case len(plan.Changes) > 0:
		return 3
	}
	return 0
}

func parseArgs(args []string) (Options, string, bool, error) {
	working, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	options := Options{WorkingDir: working, Home: home, Has: cli.Has, Version: version.Version}
	mode, asJSON := "plan", false
	modes := 0
	var positional []string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		value := func() (string, error) {
			if index+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", argument)
			}
			index++
			return args[index], nil
		}
		switch argument {
		case "--plan":
			mode, modes = "plan", modes+1
		case "--apply":
			mode, modes = "apply", modes+1
		case "--block":
			mode, modes = "block", modes+1
		case "--json":
			asJSON = true
		case "--pin":
			options.Pin = true
		case "--settings-only":
			options.SettingsOnly = true
		case "--answer":
			text, err := value()
			if err != nil {
				return options, "", false, err
			}
			id, answer, err := choices.ParseAnswer(text, keyed())
			if err != nil {
				return options, "", false, err
			}
			if options.Answers == nil {
				options.Answers = map[string]string{}
			}
			options.Answers[id] = answer
		case "--accept":
			id, err := value()
			if err != nil {
				return options, "", false, err
			}
			options.Accept = append(options.Accept, id)
		case "--profile-sha256":
			digest, err := value()
			if err != nil {
				return options, "", false, err
			}
			options.ProfileSHA256 = digest
		case "--profile":
			path, err := value()
			if err != nil {
				return options, "", false, err
			}
			if path == "" {
				return options, "", false, errors.New("--profile needs a path")
			}
			options.Profile = path
		default:
			if strings.HasPrefix(argument, "-") {
				return options, "", false, fmt.Errorf("unknown option %s", argument)
			}
			positional = append(positional, argument)
		}
	}
	switch {
	case modes > 1:
		return options, "", false, errors.New("--plan, --apply and --block are one at a time")
	case len(positional) > 1:
		return options, "", false, errors.New("one DIR at most")
	case mode != "apply" && (len(options.Accept) > 0 || options.Pin):
		return options, "", false, errors.New("--accept and --pin go with --apply")
	case mode == "block" && options.SettingsOnly:
		return options, "", false, errors.New("--settings-only goes with --plan or --apply")
	case mode != "plan" && asJSON:
		return options, "", false, errors.New("--json goes with --plan")
	}
	if len(positional) == 1 {
		options.Dir = positional[0]
	}
	return options, mode, asJSON, nil
}

// BlockOf is the block's text (between the markers) for the project.
func BlockOf(options Options) (string, error) {
	root, err := projectRoot(options.Dir)
	if err != nil {
		return "", err
	}
	planner := newPlanner(options, root)
	original, _, err := planner.readConfig()
	if err != nil {
		return "", err
	}
	chosen, err := planner.readProfile(original)
	if err != nil {
		return "", err
	}
	if err := planner.prospective(original, chosen); err != nil {
		return "", err
	}
	inputs := block.Inputs{Config: planner.plain, Has: options.Has}
	if chosen != nil {
		inputs.Rules, inputs.LeaveOut = chosen.Rules, chosen.LeaveOut
	}
	return block.Render(inputs), nil
}

// keyed are the questions adopt can write an answer for.
func keyed() []choices.Question {
	var found []choices.Question
	for _, question := range choices.All() {
		if question.Key != nil {
			found = append(found, question)
		}
	}
	return found
}

// terminal says the stream is a terminal, where a person can be asked.
func terminal(stream any) bool {
	file, isFile := stream.(*os.File)
	return isFile && choices.IsTerminal(file)
}

func writeText(out io.Writer, plan *Plan) {
	var text strings.Builder
	profileWords := "no profile"
	if plan.Profile != "" {
		profileWords = "profile " + plan.Profile
		if plan.ProfileName != "" {
			profileWords += " (" + plan.ProfileName + ")"
		}
	}
	fmt.Fprintf(&text, "jst adopt: project %s (%s), %s\n", plan.Project, plan.Root, profileWords)
	fmt.Fprintf(&text, "changes (%d):\n", len(plan.Changes))
	for _, change := range plan.Changes {
		fmt.Fprintf(&text, "  %-28s %s: %s\n", change.ID, change.Path, change.What)
	}
	if len(plan.Conflicts) > 0 {
		fmt.Fprintf(&text, "conflicts (%d) — nothing is written until each is settled:\n", len(plan.Conflicts))
	} else {
		text.WriteString("conflicts (0)\n")
	}
	for _, conflict := range plan.Conflicts {
		fmt.Fprintf(&text, "  %-28s %s\n  %-28s settle: %s\n", conflict.ID, conflict.What, "", conflict.Settle)
		if conflict.Lost != "" {
			fmt.Fprintf(&text, "  %-28s what --accept replaces:\n", "")
			for _, line := range strings.Split(strings.TrimRight(conflict.Lost, "\n"), "\n") {
				fmt.Fprintf(&text, "      | %s\n", line)
			}
		}
	}
	fmt.Fprintf(&text, "exceptions (%d):\n", len(plan.Exceptions))
	for _, exception := range plan.Exceptions {
		fmt.Fprintf(&text, "  %-28s %s\n", exception.ID, exception.Why)
	}
	fmt.Fprintf(&text, "choices (%d):\n", len(plan.Choices))
	for _, choice := range plan.Choices {
		fmt.Fprintf(&text, "  %-28s %s = %s: %s\n", choice.ID, choice.Key, choice.Value, choice.From)
	}
	fmt.Fprintf(&text, "notes (%d):\n", len(plan.Notes))
	for _, note := range plan.Notes {
		fmt.Fprintf(&text, "  %s\n", note)
	}
	_, _ = io.WriteString(out, text.String())
}

// Check is `jst doctor`'s adoption check (spec section 10) for the project around dir: false
// outside a project (no jackstay.json at or above dir); problems for conflicts, pending changes and a
// profile that cannot be read; notes for the exceptions.
func Check(dir string) (inProject bool, problems, notes []string) {
	if root, _ := config.Find(dir); root == "" {
		return false, nil, nil
	}
	home, _ := os.UserHomeDir()
	plan, err := Make(Options{Dir: dir, WorkingDir: dir, Home: home, Has: cli.Has, Version: version.Version})
	if err != nil {
		return true, []string{"jst adopt cannot plan this project: " + err.Error()}, nil
	}
	for _, conflict := range plan.Conflicts {
		problems = append(problems, fmt.Sprintf("%s: conflict %s: %s", plan.Project, conflict.ID, conflict.What))
	}
	if len(plan.Changes) > 0 {
		problems = append(problems, fmt.Sprintf("%s is not up to date with jackstay %s: %d change(s) pending — run `jst adopt --plan`",
			plan.Project, version.Version, len(plan.Changes)))
	}
	problems = append(problems, HookProblems(plan.Root, home)...)
	for _, exception := range plan.Exceptions {
		notes = append(notes, fmt.Sprintf("%s keeps %s: %s", plan.Project, exception.ID, exception.Why))
	}
	return true, problems, notes
}

// writeApplied says what an apply wrote and what is left: after --settings-only, the apply of the
// other files once jackstay.json has merged; after an apply that wrote jackstay.json and other
// files, that rule D0 lands them in two pull requests.
func writeApplied(out io.Writer, plan *Plan, settingsOnly bool) {
	switch {
	case len(plan.Changes) == 0 && settingsOnly:
		fmt.Fprintf(out, "jst adopt: %s: nothing to write in %s; `jst adopt --apply` writes the other files\n", plan.Project, config.Name)
		return
	case len(plan.Changes) == 0:
		fmt.Fprintf(out, "jst adopt: %s is up to date: nothing written\n", plan.Project)
		return
	}
	for _, change := range plan.Changes {
		fmt.Fprintf(out, "wrote  %-24s %s: %s\n", change.ID, change.Path, change.What)
	}
	switch {
	case settingsOnly:
		fmt.Fprintf(out, "jst adopt: %d change(s) written to %s alone. Land it in a pull request of its own (rule D0, merged "+
			"by the owner); then, on the merged base, run `jst adopt --apply` as before (the same folder and profile, "+
			"without --settings-only) for the other files\n", len(plan.Changes), config.Name)
	case plan.SplitsSettings():
		fmt.Fprintf(out, "jst adopt: %d change(s) written, to %s and other files: rule D0 refuses them in one pull request. "+
			"Commit %s alone first, in a pull request of its own merged by the owner, then the other files in another "+
			"(`--settings-only` writes the first part alone)\n", len(plan.Changes), config.Name, config.Name)
	default:
		fmt.Fprintf(out, "jst adopt: %d change(s) written; `jst adopt --plan` should now find nothing\n", len(plan.Changes))
	}
}
