package scopes

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// ListCommand is `jst test list`: the project's scopes, what each runs, its timeout, and when the
// project runs it (its `when`).
func ListCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	base, err := newTester(stdout, stderr)
	if err != nil {
		unreadableFolder(stderr, err)
		return statusRefused
	}
	return base.list(args)
}

func unreadableFolder(stderr io.Writer, err error) {
	fmt.Fprintf(stderr, "jst test: the working folder cannot be read: %v\n", err)
}

func (base *tester) list(args []string) int {
	_, _, chosen, err := parse(args)
	var proj *project
	if err == nil {
		proj, err = base.open(chosen)
	}
	if err != nil {
		base.say("%v", err)
		return statusRefused
	}
	scopes := objectOf(proj.section["scopes"])
	var order, missing []string
	for _, name := range six {
		if has(scopes, name) {
			order = append(order, name)
		} else {
			missing = append(missing, name)
		}
	}
	for _, name := range sortedKeys(scopes) {
		if !slices.Contains(six, name) {
			order = append(order, name)
		}
	}
	fmt.Fprintf(base.stdout, "%s: scopes (`jst test <scope>`; when each is required is the project's rule, below)\n", proj.name)
	for _, name := range order {
		entry := objectOf(scopes[name])
		what, limit := "deps: "+joinComma(depsOf(entry)), ""
		if !isComposite(entry) {
			chosen := resolve(proj.section, name, kindScope, entry)
			what = shellWords(chosen.run)
			limit = "  [no timeout: runs unbounded]"
			if chosen.timeout > 0 {
				limit = fmt.Sprintf("  [timeout %s s]", formatSeconds(chosen.timeout))
			}
		}
		fmt.Fprintf(base.stdout, "  %-10s %s%s\n", name, what, limit)
		if when := textOf(entry["when"]); when != "" {
			fmt.Fprintf(base.stdout, "  %-10s when: %s\n", "", when)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(base.stdout, "  not declared: %s\n", joinComma(missing))
	}
	return 0
}

// PlanCommand is `jst test plan <scope> [ARGS…]`: the words that would run, step by step, with
// their judge, timeout, folder, environment, base, checks and `{slow}`'s classes; nothing is run.
func PlanCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	base, err := newTester(stdout, stderr)
	if err != nil {
		unreadableFolder(stderr, err)
		return statusRefused
	}
	return base.plan(args)
}

func (base *tester) plan(args []string) int {
	name, rest, chosen, err := parse(args)
	selectChanged := false
	selectedRange := false
	var rangeArgument string
	if err == nil && name == "changed" && len(rest) == 1 && strings.Contains(rest[0], "..") {
		if chosen.base != "" || chosen.commit != "" {
			err = refused("`jst test plan changed BASE..REV` takes one range, without --base or --commit")
		} else {
			rangeArgument = rest[0]
			chosen.base, chosen.commit, err = selectionRange(rest[0])
			rest = nil
			selectedRange = err == nil
		}
	}
	if err == nil && name == "" {
		err = refused("usage: jst test plan <scope> [ARGS…]")
	}
	if err == nil && chosen.commit != "" {
		made, inside, copyError := base.makeCopy(chosen)
		if copyError != nil {
			err = copyError
		} else {
			defer made.Remove()
			base.here = inside
		}
	}
	var proj *project
	if err == nil {
		proj, err = base.open(chosen)
	}
	if err == nil && name == "changed" {
		selectChanged = has(proj.section, "selection")
		if selectedRange && !selectChanged {
			rest = []string{rangeArgument}
		}
	}
	if err == nil && (name == "auto" || selectChanged) {
		if len(rest) > 0 || chosen.commit != "" && !selectedRange {
			err = refused("`jst test plan auto` takes no test names or --commit")
		} else {
			var choice selectionResult
			choice, err = proj.selectScope()
			if err == nil {
				name = choice.scope
				chosen.base = choice.base
				proj.options = chosen
				proj.printSelection(choice)
			}
		}
	}
	// A project that says where its scopes run plans what the run does: the nodes here with
	// their deps, and what it leaves to CI or to other systems (docs/design/land.md section 7.4).
	var split *partition
	if err == nil && selectChanged && declaresWhere(proj.section) {
		made := proj.partitionOf("changed", []string{name})
		split = &made
	}
	order := []string{name}
	if err == nil {
		scopes := objectOf(proj.section["scopes"])
		entry := objectOf(scopes[name])
		switch {
		case entry == nil:
			err = refused("`" + name + "` is not a scope of " + proj.name + "; its scopes: " + joinComma(sortedKeys(scopes)))
		case namesRefusal(name, entry, rest) != nil:
			err = namesRefusal(name, entry, rest)
		case split != nil:
			order = newGraph(proj.section, proj.operatingSystem).walk(split.here)
			rest = nil
		case len(depsOf(entry)) > 0:
			order = newGraph(proj.section, proj.operatingSystem).walk(order)
		}
	}
	if err != nil {
		base.say("%v", err)
		return statusRefused
	}
	out := base.stdout
	nodes := newGraph(proj.section, proj.operatingSystem)
	requested := name
	for _, name := range order {
		kind, entry := find(proj.section, name)
		switch {
		case !nodes.eligible(name):
			fmt.Fprintf(out, "%s: skipped (it cannot run on %s)\n", name, proj.operatingSystem)
			continue
		case isComposite(entry):
			fmt.Fprintf(out, "%s: passes when its deps that run here pass (%s)\n", name, joinComma(depsOf(entry)))
			continue
		}
		leafOf := resolve(proj.section, name, kind, entry)
		// The scope asked for takes the test arguments; its deps none.
		var args []string
		if name == requested {
			args = rest
		}
		found, err := proj.wordsOf(leafOf, args)
		if err != nil {
			fmt.Fprintf(out, "%s: cannot run as asked: %v\n", name, err)
			continue
		}
		fmt.Fprintf(out, "%s: %s\n", name, shellWords(found.argv))
		timeout := "none"
		if leafOf.timeout > 0 {
			timeout = formatSeconds(leafOf.timeout)
		}
		details := fmt.Sprintf("judge=%s timeout=%s cwd=%s", leafOf.judge, timeout, leafOf.cwd)
		if len(leafOf.env) > 0 {
			pairs := make([]string, 0, len(leafOf.env))
			for _, key := range sortedKeys(leafOf.env) {
				pairs = append(pairs, key+"="+leafOf.env[key])
			}
			details += " env=" + strings.Join(pairs, ",")
		}
		if found.base != "" {
			details += " base=" + found.base
		}
		fmt.Fprintf(out, "    %s\n", details)
		for _, each := range leafOf.requires {
			fmt.Fprintf(out, "    requires: %s  (%s)\n", joinSpace(each.run), each.why)
		}
		if !slices.Contains(placeholdersOf(leafOf.run), "slow") {
			continue
		}
		switch {
		case found.note != "":
			fmt.Fprintf(out, "    %s\n", found.note)
		case len(found.classes) == 0:
			fmt.Fprintln(out, "    quick skips nothing: no class is slow by the rule")
		}
		for _, class := range found.classes {
			fmt.Fprintf(out, "    skipped: %s  (median %.1f s, slowest case %.1f s)\n", class.name, class.total, class.slowest)
		}
	}
	if split != nil {
		var leftToCI []string
		for _, name := range split.ci {
			if !slices.Contains(order, name) {
				leftToCI = append(leftToCI, name)
			}
		}
		fmt.Fprintf(out, "here: %s\n", orNone(joinComma(split.here)))
		fmt.Fprintf(out, "left to CI: %s\n", orNone(joinComma(leftToCI)))
		fmt.Fprintf(out, "elsewhere: %s\n", orNone(split.elsewhereText()))
	}
	return 0
}
