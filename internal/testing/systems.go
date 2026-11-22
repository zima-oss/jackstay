package scopes

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/receipts"
)

// Where tests run (docs/design/land.md section 7.4): `jst test changed` and `jst test uncovered`
// split the nodes they reach between here, CI and other systems; `jst test ci` runs what CI covers
// on this system.

const (
	// uncoveredCommand and ciCommand are `jst test uncovered` and `jst test ci`; neither can be a
	// scope's name (the six, or `x-…`).
	uncoveredCommand = "uncovered"
	ciCommand        = "ci"
)

// CIJobName is the CI job that runs `jst test ci` on a system, which the base's rules require.
func CIJobName(system string) string { return "jst test ci (" + system + ")" }

// partitionOf puts every node with `run` that the walk from the selected scopes reaches on this
// system in one group, the first that fits: here (eligible here and not covered, in the command's
// sense), ci (covered on some system), elsewhere (neither).
func (proj *project) partitionOf(command string, selected []string) partition {
	nodes := newGraph(proj.section, proj.operatingSystem)
	nodes.withoutBase = command == uncoveredCommand
	covered := map[string]map[string]bool{}
	for _, system := range operatingSystems {
		covered[system] = coveredOn(proj.section, system)
	}
	split := partition{command: command, where: map[string][]string{}}
	for _, name := range nodes.walk(selected) {
		if _, entry := find(proj.section, name); isComposite(entry) || nodes.withoutBase && needsBase(entry) {
			continue
		}
		anywhere := slices.ContainsFunc(operatingSystems, func(system string) bool { return covered[system][name] })
		uncovered := !anywhere
		if command == uncoveredCommand {
			uncovered = !covered[proj.operatingSystem][name]
		}
		switch {
		case nodes.eligible(name) && uncovered:
			split.here = append(split.here, name)
		case anywhere:
			split.ci = append(split.ci, name)
		default:
			split.elsewhere = append(split.elsewhere, name)
			for _, system := range operatingSystems {
				if newGraph(proj.section, system).eligible(name) {
					split.where[name] = append(split.where[name], system)
				}
			}
		}
	}
	return split
}

// runSplit runs the nodes a split puts here, with their deps from any group, and says the groups.
func (proj *project) runSplit(command string, selected []string) int {
	split := proj.partitionOf(command, selected)
	return proj.gate(command, split.here, &split)
}

// uncoveredSelection are the scopes `jst test uncovered` starts from: every scope that runs
// without test names, IDs or a --base.
func (proj *project) uncoveredSelection() []string {
	scopes := objectOf(proj.section["scopes"])
	var selected []string
	for _, name := range orderedScopes(scopes) {
		entry := objectOf(scopes[name])
		if name != "focused" && !needsTestArguments(entry) && !needsBase(entry) {
			selected = append(selected, name)
		}
	}
	return selected
}

// UncoveredCommand is `jst test uncovered [--commit REV] [--keep-going]`.
func UncoveredCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	base, err := newTester(stdout, stderr)
	if err != nil {
		receipts.Say(stderr, "the working folder cannot be read: %v", err)
		receipts.Say(stderr, "outcome=not-started (nothing was run)")
		return statusRefused
	}
	return base.uncovered(args)
}

// uncovered runs, on a clean copy of the exact commit, what CI does not cover on this system; its
// `passed` or `left-to-ci` line is the record `jst install` from a checkout needs.
func (base *tester) uncovered(args []string) int {
	_, rest, chosen, err := parse(append([]string{uncoveredCommand}, args...))
	if err == nil && (len(rest) > 0 || chosen.resume || chosen.base != "") {
		err = refused("usage: jst test uncovered [--commit REV] [--keep-going] [--project NAME]")
	}
	revision := chosen.commit
	if revision == "" {
		revision = "HEAD"
	}
	exact := ""
	if err == nil {
		var commit []byte
		commit, err = base.git(context.Background(), base.here, "rev-parse", "--verify", "--quiet", revision+"^{commit}")
		exact = strings.TrimSpace(string(commit))
		if err != nil || strings.HasPrefix(revision, "-") {
			err = refused("`" + revision + "` is not a commit")
		}
	}
	if err == nil && chosen.project == "" {
		// The commit names the project, as `jst install` reads it, so an uncommitted rename cannot
		// file the record under another name.
		if root, _ := config.Find(base.here); root != "" {
			var committed config.Config
			committed, err = committedConfig(func(args ...string) ([]byte, error) {
				return base.git(context.Background(), root, args...)
			}, exact)
			chosen.project = config.ProjectName(root, committed, root)
		}
	}
	if err != nil {
		base.say("%v", err)
		base.say("outcome=not-started (nothing was run)")
		return statusRefused
	}
	base.pin = &gatePin{commit: exact}
	words := []string{uncoveredCommand, "--commit", exact}
	if chosen.project != "" {
		words = append(words, "--project", chosen.project)
	}
	if chosen.keepGoing {
		words = append(words, "--keep-going")
	}
	if chosen.lock != nil && *chosen.lock {
		words = append(words, "--lock")
	} else if chosen.lock != nil {
		words = append(words, "--no-lock")
	}
	return base.scope(words)
}

// CICommand is `jst test ci [--keep-going]`: every scope covered on this system, once, in graph
// order; the one command of the CI job `jst test ci (SYSTEM)`.
func CICommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	base, err := newTester(stdout, stderr)
	if err != nil {
		receipts.Say(stderr, "the working folder cannot be read: %v", err)
		receipts.Say(stderr, "outcome=not-started (nothing was run)")
		return statusRefused
	}
	return base.scope(append([]string{ciCommand}, args...))
}

// UncoveredPassed is nil when `jst test uncovered` ended `passed` or `left-to-ci` on this
// machine's system for the revision of the checkout at root, else why not. The policy is the
// revision's own committed jackstay.json, never the working tree's: an install builds the commit,
// so an uncommitted edit cannot waive the record. A project none of whose committed scopes has
// `ci` declares no split, and needs none.
func UncoveredPassed(root, revision, system string) error {
	git := gitcli.Git{Dir: root}
	commit, err := git.Line(context.Background(), "rev-parse", "--verify", "--quiet", revision+"^{commit}")
	if err != nil {
		return fmt.Errorf("`%s` is not a commit of %s", revision, root)
	}
	loaded, err := committedConfig(func(args ...string) ([]byte, error) { return git.Run(context.Background(), args...) }, commit)
	if err != nil {
		return err
	}
	scopes := objectOf(loaded.Section("test")["scopes"])
	if !slices.ContainsFunc(sortedKeys(scopes), func(name string) bool { return has(objectOf(scopes[name]), "ci") }) {
		return nil
	}
	name := config.ProjectName(root, loaded, root)
	for _, record := range receipts.ReadRecords(receipts.RunLogPaths()) {
		outcome := record.Text("outcome")
		if record.Text("event") == receipts.EventGate && record.Text("scope") == uncoveredCommand &&
			record.Text("project") == name && (outcome == receipts.OutcomePassed || outcome == outcomeLeftToCI) &&
			record.Text("head") == commit && record.Text("pinned_candidate") == commit && record.Text("system") == system {
			return nil
		}
	}
	return fmt.Errorf("no passed or left-to-ci `jst test uncovered` on %s for %s: run it first (what CI does not cover there)",
		system, commit)
}

// committedConfig is the jackstay.json a commit holds beside the folder git runs in, checked; an
// empty one when the commit has none. Who a commit is, and what it asks for, come from the commit,
// never from the working tree.
func committedConfig(git func(args ...string) ([]byte, error), commit string) (config.Config, error) {
	listed, err := git("ls-tree", "--name-only", commit, "--", config.Name)
	if err != nil {
		return nil, fmt.Errorf("the tree of %s cannot be read: %w", commit, err)
	}
	if strings.TrimSpace(string(listed)) == "" {
		return config.Config{}, nil
	}
	data, err := git("show", commit+":./"+config.Name)
	if err != nil {
		return nil, fmt.Errorf("%s of %s cannot be read: %w", config.Name, commit, err)
	}
	loaded, err := config.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s of %s cannot be used: %w", config.Name, commit, err)
	}
	problems := config.SectionProblems(loaded)
	if found := append(problems.Of(""), problems.Of("test")...); len(found) > 0 {
		return nil, fmt.Errorf("%s of %s cannot be used: %s", config.Name, commit, strings.Join(found, "; "))
	}
	return loaded, nil
}
