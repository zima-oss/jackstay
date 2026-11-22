// Package check is `jst check [DIR]`: is the project's jackstay.json usable? For a project's own gate
// (docs/design/implementation.md section 3.2; docs/design/testing.md for the `test` warnings). Exit 0 when the whole file is usable, with warnings about the `test`
// section; 2 naming every problem of every section, or when there is no file.
package check

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/review"
	scopes "github.com/zima-oss/jackstay/internal/testing"
	"github.com/zima-oss/jackstay/internal/version"

	// Its init also installs the release rules into changes.Check.
	"github.com/zima-oss/jackstay/internal/release"
)

// A step measured above dearSeconds should not come before one below cheapSeconds.
const (
	cheapSeconds = 10.0
	dearSeconds  = 60.0
)

func init() {
	cli.Register(cli.Command{Name: "check", Summary: "is this project's jackstay.json usable?", Usage: help, Run: run})
}

func run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: jst check [DIR]")
		return 2
	}
	start := ""
	if len(args) == 1 {
		start = args[0]
	}
	if start == "" {
		start = "."
	}
	start, err := filepath.Abs(start)
	if err != nil {
		fmt.Fprintf(stderr, "jst check: %v\n", err)
		return 2
	}
	root, path := config.Find(start)
	if path == "" {
		fmt.Fprintf(stderr, "jst check: no %s at or above %s\n", config.Name, start)
		return 2
	}
	_, loaded, err := config.Load(start, nil)
	if err != nil {
		fmt.Fprintf(stderr, "jst check: %v\n", err)
		return 2
	}
	if problems := ConfigProblems(loaded); len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintf(stderr, "jst check: %s\n", problem)
		}
		return 2
	}
	if loaded.Section("changes") != nil {
		// The release rules installed by internal/release also validate the tree's entries.
		if code := releaseRules(start, stderr); code != 0 {
			return code
		}
	}
	fmt.Fprintf(stdout, "jst check: %s is usable by jackstay %s.\n", path, version.Version)
	if section := loaded.Section("test"); section != nil {
		for _, warning := range Warnings(section, config.ProjectName(root, loaded, start)) {
			fmt.Fprintf(stdout, "jst check: warning: %s\n", warning)
		}
	}
	return 0
}

// Warnings are what `jst check` says about a usable `test` section: a scope or step that runs with
// no timeout (none of its own, none for the section), and a scope that runs a step measured above
// a minute before one measured below ten seconds (medians of the project's run record).
func Warnings(section map[string]any, project string) []string {
	var found []string
	_, sectionTimeout := section["timeout"]
	for _, kind := range []string{"scopes", "steps"} {
		entries, _ := section[kind].(map[string]any)
		for _, name := range sortedNames(entries) {
			entry, _ := entries[name].(map[string]any)
			_, runs := entry["run"]
			_, timeout := entry["timeout"]
			if runs && !timeout && !sectionTimeout {
				found = append(found, fmt.Sprintf("`test.%s.%s` has no timeout (none for it, none for the section): it runs unbounded", kind, name))
			}
		}
	}
	if project == "" {
		return found
	}
	medians := stepMedians(project)
	declared, _ := section["scopes"].(map[string]any)
	for _, name := range sortedNames(declared) {
		entry, _ := declared[name].(map[string]any)
		_, listed := entry["steps"]
		if _, depends := entry["deps"]; !listed && !depends {
			continue
		}
		order := scopes.RunOrder(section, name, runtime.GOOS)
		for index, dear := range order {
			if medians[dear] <= dearSeconds {
				continue
			}
			cheap := ""
			for _, later := range order[index+1:] {
				if seconds, measured := medians[later]; measured && seconds < cheapSeconds {
					cheap = later
					break
				}
			}
			if cheap != "" {
				found = append(found, fmt.Sprintf("`test.scopes.%s`: the step %s (median %.0f s) comes before %s (median %.1f s): "+
					"cheap first, so that a failure is found early", name, dear, medians[dear], cheap, medians[cheap]))
				break
			}
		}
	}
	return found
}

// sortedNames are an object's member names, sorted (a Go map has no order of its own).
func sortedNames(members map[string]any) []string {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// stepMedians are the median seconds of each step or scope from the run record's finished runs
// of a project.
func stepMedians(project string) map[string]float64 {
	var files []string
	if shared, err := homedir.SharedPath("runs.jsonl"); err == nil {
		files = append(files, shared)
	}
	seen := map[string][]float64{}
	for _, file := range files {
		for _, record := range readLines(file) {
			outcome, _ := record["outcome"].(string)
			if record["event"] != "run" || record["project"] != project ||
				(outcome != "passed" && outcome != "exited-0" && outcome != "failed") {
				continue
			}
			name, _ := record["step"].(string)
			if name == "" {
				name, _ = record["scope"].(string)
			}
			seconds, isNumber := record["seconds"].(float64)
			if name != "" && isNumber {
				seen[name] = append(seen[name], seconds)
			}
		}
	}
	medians := map[string]float64{}
	for name, values := range seen {
		slices.Sort(values)
		middle := len(values) / 2
		if len(values)%2 == 1 {
			medians[name] = values[middle]
		} else {
			medians[name] = (values[middle-1] + values[middle]) / 2
		}
	}
	return medians
}

// readLines are a log's records; a line that is not a JSON object is skipped.
func readLines(file string) []map[string]any {
	handle, err := os.Open(file) // #nosec G304 -- jackstay's own log
	if err != nil {
		return nil
	}
	defer func() { _ = handle.Close() }()
	var found []map[string]any
	scanner := bufio.NewScanner(handle)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for scanner.Scan() {
		var record map[string]any
		if json.Unmarshal([]byte(strings.ToValidUTF8(scanner.Text(), "�")), &record) == nil && record != nil {
			found = append(found, record)
		}
	}
	return found
}

// releaseRules runs changes.Check for the project around start: 0 when nothing is wrong, 2 with
// each problem named (or the validator's own fatal error with its status).
func releaseRules(start string, stderr io.Writer) int {
	project, err := changes.LoadProjectAt(start)
	if err == nil {
		var problems []string
		problems, _, err = changes.Check(project)
		if err == nil {
			for _, problem := range problems {
				fmt.Fprintf(stderr, "jst check: %s\n", problem)
			}
			if len(problems) > 0 {
				return 2
			}
			return 0
		}
	}
	fmt.Fprintf(stderr, "jst check: %v\n", err)
	var failure *changes.Problem
	if errors.As(err, &failure) && failure.Code != 0 {
		return failure.Code
	}
	return 2
}

// ConfigProblems are what `jst check` finds wrong in a jackstay.json's content, every section judged: the
// table of keys, each section's meaning, `review`'s, `test`'s, `changes`'s and `release`'s rules. The release
// rules, which read the tree, are not among them. `jst adopt` judges the file it would write with
// it (docs/design/adopt.md section 4).
func ConfigProblems(parsed config.Config) []string {
	var found []string
	sectionProblems := config.SectionProblems(parsed)
	for _, section := range sectionProblems.Sections() {
		found = append(found, sectionProblems.Of(section)...)
	}
	if len(found) > 0 {
		return found
	}
	if parsed.Section("review") != nil {
		_, problems := review.SettingsOf(parsed)
		found = append(found, problems...)
	}
	if section := parsed.Section("test"); section != nil {
		found = append(found, scopes.Problems(section)...)
	}
	if parsed.Section("release") != nil {
		_, problems := release.SettingsOf(parsed)
		found = append(found, problems...)
	} else if parsed.Section("changes") != nil {
		if _, err := changes.SettingsOf(parsed); err != nil {
			found = append(found, err.Error())
		}
	}
	return found
}
