// Package baseline is `jst baseline`: how a project's test runs and merges look (docs/design/implementation.md
// section 9.6), counted from Claude Code transcripts, git and jackstay's run log. It
// reads, and changes nothing. For one project and a span of UTC days it reports:
//
//   - test runs by kind: every Bash call of the span read as a shell reads it and given the first
//     kind one of its simple commands matches; attempts, refused (a hook), denied (another
//     permission layer), started, and the transcript wall time of the started ones (a background
//     call ends at its notification; a call answered in under five seconds is not timed);
//   - the same per UTC day, with merges and overlap per day;
//   - merges: subjects on the branch (first parent) ending in `(#N)`, or the branch's commits;
//   - jackstay's run log: runs that executed, their time alone, reuses, would-hit, overlap with other
//     recorded runs, and W-005's split and lock evidence (docs/design/implementation.md section 5.4);
//   - destructive commands: every Bash command of the span put through today's guard, split by
//     what became of the call (git's answers of that day are not known: guard.NoGit);
//   - overlap: per gate kind, how many timed runs shared five seconds of wall time with another
//     gate run of the project or of one named with --also, and the medians.
//
// What it cannot see: Codex sessions, runs started by the owner in a terminal, whether two runs
// tested the same tree, and (from transcripts alone) pass or fail behind `| tail`.
package baseline

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/receipts"
)

const usage = `usage: jst baseline --repo DIR --since YYYY-MM-DD --until YYYY-MM-DD [--project NAME] [--config FILE]
                   [--kind NAME=REGEX ...] [--gate NAME ...] [--merge-unit pr|commit] [--branch B]
                   [--transcripts GLOB ...] [--also NAME=DIR ...] [--projects-dir DIR] [--jackstay-log FILE]
                   [--examples FILE] [--json]

The days are UTC. The project's kinds of run come from the ` + "`baseline`" + ` section of its jackstay.json (or --config);
the flags replace what they name. --examples writes each command the guard would refuse to a file
(it may hold private text).`

const exitBadRequest = 2

func init() {
	cli.Register(cli.Command{Name: "baseline", Summary: "test runs by kind, their time, merges and destructive commands, from transcripts, git and jackstay's log",
		Usage: help, Run: run})
}

// projectsDir is where Claude Code keeps its sessions: CLAUDE_CONFIG_DIR's, else the account's.
func projectsDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects")
	}
	return filepath.Join(homedir.AccountHome(), ".claude", "projects")
}

type request struct {
	project, repo, since, until, configFile, mergeUnit, branch, projects, jackstayLog, examples string
	kinds, gates, transcripts, also                                                             []string
	json                                                                                        bool
}

func run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	asked := request{projects: projectsDir()}
	flags := flag.NewFlagSet("jst baseline", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	for name, target := range map[string]*string{"project": &asked.project, "repo": &asked.repo, "since": &asked.since,
		"until": &asked.until, "config": &asked.configFile, "merge-unit": &asked.mergeUnit, "per": &asked.mergeUnit,
		"branch": &asked.branch, "projects-dir": &asked.projects, "jackstay-log": &asked.jackstayLog, "examples": &asked.examples} {
		flags.StringVar(target, name, *target, "")
	}
	for name, target := range map[string]*[]string{"kind": &asked.kinds, "gate": &asked.gates, "transcripts": &asked.transcripts,
		"also": &asked.also} {
		flags.Func(name, "", func(value string) error {
			*target = append(*target, value)
			return nil
		})
	}
	flags.BoolVar(&asked.json, "json", false, "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, usage)
			return 0
		}
		fmt.Fprintf(stderr, "jst baseline: %v\n%s\n", err, usage)
		return exitBadRequest
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "jst baseline: unexpected argument %q\n%s\n", flags.Arg(0), usage)
		return exitBadRequest
	}
	var missing []string
	for name, value := range map[string]string{"--repo": asked.repo, "--since": asked.since, "--until": asked.until} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "jst baseline: %s must be given\n%s\n", strings.Join(sortedNames(setOf(missing)), ", "), usage)
		return exitBadRequest
	}
	if asked.mergeUnit != "" && asked.mergeUnit != "pr" && asked.mergeUnit != "commit" {
		fmt.Fprintf(stderr, "jst baseline: --merge-unit is pr or commit, not %q\n", asked.mergeUnit)
		return exitBadRequest
	}
	span, err := SpanOf(asked.since, asked.until)
	if err != nil {
		fmt.Fprintf(stderr, "jst baseline: %v\n", err)
		return exitBadRequest
	}
	spec, err := Define(asked.project, asked.repo, asked.configFile, Flags{Kinds: asked.kinds, Gates: asked.gates,
		MergeUnit: asked.mergeUnit, Branch: asked.branch, Transcripts: asked.transcripts})
	inputs := Inputs{Repo: asked.repo, ProjectsDir: asked.projects, Examples: asked.examples, LogPaths: receipts.RunLogPaths()}
	if asked.jackstayLog != "" {
		inputs.LogPaths, inputs.LogGiven = []string{asked.jackstayLog}, true
	}
	for _, word := range asked.also {
		if err != nil {
			break
		}
		name, folder, found := strings.Cut(word, "=")
		if !found || name == "" || folder == "" {
			err = definitionError("--also takes NAME=DIR: %q", word)
			break
		}
		var other Definition
		other, err = Define(name, folder, "", Flags{})
		inputs.Also = append(inputs.Also, other)
	}
	if err != nil {
		fmt.Fprintf(stderr, "jst baseline: %v\n", err)
		return exitBadRequest
	}
	if len(spec.Kinds) == 0 {
		fmt.Fprintf(stderr, "jst baseline: no kinds of run are defined for %s (a `baseline` section in its jackstay.json, or --kind NAME=REGEX); "+
			"only merges and destructive commands are counted.\n", spec.Project)
	}
	report, err := Measure(spec, span, inputs)
	if err != nil {
		fmt.Fprintf(stderr, "jst baseline: %v\n", err)
		return 1
	}
	if asked.json {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(stderr, "jst baseline: %v\n", err)
			return 1
		}
		return 0
	}
	show(report, stdout)
	return 0
}

func setOf(names []string) map[string]bool {
	found := map[string]bool{}
	for _, name := range names {
		found[name] = true
	}
	return found
}
