package doctor

import (
	"regexp"
	"sort"
	"strings"
)

// lintJob is a workflow job whose steps run `jst lint pr`, and the check it reports.
type lintJob struct {
	File, ID string
	// Check is the check run's name: the job's `name:`, else its ID ("" when Unknown).
	Check string
	// Unknown says why the check's name cannot be told from the file (an expression, a matrix).
	Unknown string
}

func (job lintJob) String() string { return job.File + " job " + job.ID }

var (
	runsLint  = regexp.MustCompile(`\bjst\s+lint\s+pr\b`)
	runKey    = regexp.MustCompile(`^(\s*)(?:-\s+)?run:\s*(.*)$`)
	keyLine   = regexp.MustCompile(`^(\s*)([A-Za-z0-9_-]+):\s*(.*)$`)
	blockMark = regexp.MustCompile(`^[|>][-+0-9]*\s*(#.*)?$`)
)

// lintJobs are the jobs of the workflows (file name to text) whose `run:` steps run `jst lint pr`,
// sorted by file and job. It reads block-style YAML indented with spaces, as GitHub's workflow
// files are written: `jobs:` at the left margin, each job a key under it, its `name:` and
// `strategy:` keys, and its steps' `run:` values (on the line or as a `|`/`>` block). A job's
// check is its `name:` or else its ID; a name with `${{` or a job with a matrix reports checks
// whose names this does not compute, so those are Unknown. Flow-style mappings, anchors and
// aliases are not read.
func lintJobs(workflows map[string]string) []lintJob {
	var found []lintJob
	for file, text := range workflows {
		for _, job := range jobsOf(text) {
			if !job.lint {
				continue
			}
			entry := lintJob{File: file, ID: job.id, Check: job.id}
			switch {
			case job.matrix:
				entry.Check, entry.Unknown = "", "it runs a matrix, whose checks are named by its values"
			case strings.Contains(job.name, "${{"):
				entry.Check, entry.Unknown = "", "its name is an expression: "+job.name
			case job.name != "":
				entry.Check = job.name
			}
			found = append(found, entry)
		}
	}
	sort.Slice(found, func(left, right int) bool {
		if found[left].File != found[right].File {
			return found[left].File < found[right].File
		}
		return found[left].ID < found[right].ID
	})
	return found
}

type workflowJob struct {
	id, name     string
	matrix, lint bool
}

// jobsOf reads the jobs of one workflow file.
func jobsOf(text string) []workflowJob {
	lines := strings.Split(text, "\n")
	var jobs []workflowJob
	inJobs := false
	jobIndent, keyIndent := -1, -1
	strategyIndent := -1
	runIndent := -1
	var current *workflowJob
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if runIndent >= 0 {
			if indent > runIndent {
				if current != nil && runsLint.MatchString(line) {
					current.lint = true
				}
				continue
			}
			runIndent = -1
		}
		if indent == 0 {
			inJobs = strings.HasPrefix(line, "jobs:")
			jobIndent, current = -1, nil
			continue
		}
		if !inJobs {
			continue
		}
		if jobIndent < 0 {
			jobIndent = indent
		}
		if indent <= jobIndent {
			current = nil
			if match := keyLine.FindStringSubmatch(line); match != nil && indent == jobIndent {
				jobs = append(jobs, workflowJob{id: match[2]})
				current = &jobs[len(jobs)-1]
				keyIndent, strategyIndent = -1, -1
			}
			continue
		}
		if current == nil {
			continue
		}
		if keyIndent < 0 {
			keyIndent = indent
		}
		if strategyIndent >= 0 && indent <= strategyIndent {
			strategyIndent = -1
		}
		if indent == keyIndent {
			if match := keyLine.FindStringSubmatch(line); match != nil {
				switch match[2] {
				case "name":
					current.name = unquote(match[3])
				case "strategy":
					strategyIndent = indent
				}
			}
		} else if strategyIndent >= 0 && strings.HasPrefix(trimmed, "matrix:") {
			current.matrix = true
		}
		if match := runKey.FindStringSubmatch(line); match != nil {
			value := strings.TrimSpace(match[2])
			if value == "" || blockMark.MatchString(value) {
				runIndent = indent
			} else if runsLint.MatchString(value) {
				current.lint = true
			}
		}
	}
	return jobs
}

// unquote is a scalar's text without its quotes and a trailing comment.
func unquote(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
		if end := strings.IndexByte(value[1:], value[0]); end >= 0 {
			return value[1 : end+1]
		}
	}
	if comment := strings.Index(value, " #"); comment >= 0 {
		value = value[:comment]
	}
	return strings.TrimSpace(value)
}
