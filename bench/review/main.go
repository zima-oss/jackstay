// Command review measures what jackstay's review wrapper is worth (W-090): the same seeded defects
// are reviewed by each reviewer through `jst review` and directly, run after run, and every answer
// is scored against the defects, with the time of each stage, the model each run resolved to and
// its cost. Run it with `just review-bench`; it costs real model calls.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// Defects is testdata/defects.json: the fixture's ranges and the defects seeded in each.
type Defects struct {
	Ranges []struct {
		Name    string `json:"name"`
		Step    string `json:"step"`
		Subject string `json:"subject"`
	} `json:"ranges"`
	Defects []Defect `json:"defects"`
	// Extras are real issues of the fixture that were not seeded: a finding of one is confirmed (it
	// counts for precision) but not a seeded defect found (it does not count for recall).
	Extras []Defect `json:"extras"`
}

// Defect is one seeded defect: where it is, the words a finding about it uses, and what it is.
type Defect struct {
	ID     string   `json:"id"`
	Range  string   `json:"range"`
	File   string   `json:"file"`
	Symbol string   `json:"symbol"`
	Words  []string `json:"words"`
	What   string   `json:"what"`
}

// Run is one reviewer's review of one range by one arm.
type Run struct {
	Arm       string    `json:"arm"`
	Reviewer  string    `json:"reviewer"`
	Range     string    `json:"range"`
	Iteration int       `json:"iteration"`
	Started   time.Time `json:"started"`
	Wall      float64   `json:"wall_seconds"`
	Prepare   float64   `json:"prepare_seconds"` // source view (wrapped only)
	Wait      float64   `json:"wait_seconds"`    // slot and start lock (wrapped only)
	Model     float64   `json:"model_seconds"`   // the reviewer's own run: model and tools
	Models    []string  `json:"models"`
	CostUSD   float64   `json:"cost_usd"`
	Tokens    int64     `json:"tokens"`
	Outcome   string    `json:"outcome"`
	Findings  []Finding `json:"findings"`
	Found     []string  `json:"found"`     // seeded defects found
	Extras    int       `json:"extras"`    // findings of a confirmed issue that was not seeded
	Unmatched int       `json:"unmatched"` // findings that name no seeded defect
	False     int       `json:"false"`     // findings a person judged not to be a real issue
	// The settings asked for: the model and effort of this run's reviewer.
	RequestedModel  string `json:"requested_model"`
	RequestedEffort string `json:"requested_effort"`
	Duplicates      int    `json:"duplicates"` // findings of a defect already found in this run
}

// Finding is the part of a reviewer's finding that is scored.
type Finding struct {
	Place  string `json:"place"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Match  string `json:"match,omitempty"`
}

type bench struct {
	labels                         map[string]string // a person's judgement of findings, by labelKey
	root, fixture, jst, work, home string
	codexModel, claudeModel        string
	codexEffort, claudeEffort      string
	timeout                        time.Duration
	defects                        Defects
	commits                        []string // base, then the head of each range
	checkouts                      []string // a checkout at each range's head
	log                            io.Writer
}

func main() {
	var arms, out, rescore string
	var runs int
	measurement := bench{log: os.Stderr}
	flag.StringVar(&measurement.jst, "jst", "", "the jst binary to measure (absolute)")
	flag.StringVar(&arms, "arms", "codex-jst,codex-direct,claude-jst,claude-direct", "the arms, comma-separated")
	flag.IntVar(&runs, "runs", 5, "sequential runs of every arm on every range")
	flag.StringVar(&measurement.codexModel, "codex-model", "", "the Codex model both Codex arms use (default: the one in ~/.codex/config.toml)")
	flag.StringVar(&measurement.claudeModel, "claude-model", "", "the Claude model both Claude arms use (default: the one in ~/.claude/settings.json)")
	flag.StringVar(&measurement.codexEffort, "codex-effort", "medium", "Codex's reasoning effort, for both Codex arms")
	flag.StringVar(&measurement.claudeEffort, "claude-effort", "", "Claude Code's effort, for both Claude arms")
	flag.StringVar(&out, "out", "bench/review/results", "where the raw runs and the report are written")
	flag.DurationVar(&measurement.timeout, "timeout", 40*time.Minute, "one run's deadline (past jst review's own 37-minute budget)")
	flag.StringVar(&rescore, "rescore", "", "score the runs of this .jsonl again with testdata/defects.json and rewrite its report; no model call")
	flag.Parse()
	var err error
	if rescore != "" {
		err = measurement.rescore(rescore, strings.Split(arms, ","))
	} else {
		err = measurement.main(strings.Split(arms, ","), runs, out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "review bench:", err)
		os.Exit(1)
	}
}

func (measurement *bench) main(arms []string, runs int, out string) error {
	if !filepath.IsAbs(measurement.jst) {
		return errors.New("-jst must be an absolute path to a jst binary")
	}
	var err error
	if measurement.root, err = os.Getwd(); err != nil {
		return err
	}
	measurement.fixture = filepath.Join(measurement.root, "bench", "review", "testdata")
	data, err := os.ReadFile(filepath.Join(measurement.fixture, "defects.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &measurement.defects); err != nil {
		return err
	}
	if measurement.codexModel == "" {
		measurement.codexModel = configuredCodexModel()
	}
	if measurement.claudeModel == "" {
		measurement.claudeModel = configuredClaudeModel()
	}
	// jackstay's reviewers ignore the user's configuration: without one model named for both arms of
	// a reviewer, the two arms could compare different models.
	for _, arm := range arms {
		if strings.HasPrefix(arm, "codex") && measurement.codexModel == "" || strings.HasPrefix(arm, "claude") && measurement.claudeModel == "" {
			return fmt.Errorf("no model is configured for %s: name one with -%s-model", arm, strings.SplitN(arm, "-", 2)[0])
		}
	}
	if measurement.work, err = os.MkdirTemp("", "review-bench-"); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(measurement.work) }()
	measurement.home = filepath.Join(measurement.work, "jackstay-home")
	if err := measurement.makeFixture(); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o750); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("2006-01-02T150405Z")
	raw, err := os.Create(filepath.Join(out, stamp+".jsonl")) // #nosec G304 -- the operator's output folder
	if err != nil {
		return err
	}
	var all []Run
	for iteration := 1; iteration <= runs; iteration++ {
		// The order of the arms turns each iteration, so drift over the hours hits every arm alike.
		order := append(slices.Clone(arms[(iteration-1)%len(arms):]), arms[:(iteration-1)%len(arms)]...)
		for position := range measurement.defects.Ranges {
			for _, arm := range order {
				run := measurement.one(arm, position, iteration)
				all = append(all, run)
				line, err := json.Marshal(run)
				if err == nil {
					_, err = raw.Write(append(line, '\n'))
				}
				if err != nil {
					_ = raw.Close()
					return fmt.Errorf("keeping the raw runs: %w", err) // no report without its evidence
				}
				fmt.Fprintf(measurement.log, "%s %-14s %-7s run %d: %-10s found %s, %d unmatched, %.0f s\n", time.Now().Format("15:04:05"),
					arm, run.Range, iteration, run.Outcome, strings.Join(run.Found, ","), run.Unmatched, run.Wall)
			}
		}
	}
	if err := raw.Close(); err != nil {
		return fmt.Errorf("keeping the raw runs: %w", err)
	}
	report := measurement.report(all, arms, runs, stamp)
	path := filepath.Join(out, stamp+".md")
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil { // #nosec G306 -- a report meant to be read
		return err
	}
	fmt.Println(path)
	return nil
}

// rescore scores the runs saved in path again and rewrites the report beside it.
func (measurement *bench) rescore(path string, arms []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(root, "bench", "review", "testdata", "defects.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &measurement.defects); err != nil {
		return err
	}
	saved, err := os.ReadFile(path) // #nosec G304 -- the operator's own results
	if err != nil {
		return err
	}
	labelsPath := strings.TrimSuffix(path, ".jsonl") + ".labels.json"
	if data, err := os.ReadFile(labelsPath); err == nil { // #nosec G304 -- beside the operator's results
		if err := json.Unmarshal(data, &measurement.labels); err != nil {
			return fmt.Errorf("%s: %w", labelsPath, err)
		}
	}
	var all []Run
	runs := 0
	for _, line := range strings.Split(strings.TrimSpace(string(saved)), "\n") {
		var run Run
		if err := json.Unmarshal([]byte(line), &run); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if run.Outcome == "answered" {
			measurement.score(&run)
		}
		runs = max(runs, run.Iteration)
		all = append(all, run)
		// The settings asked for: kept with each run; flags stand in for runs saved without them.
		if run.RequestedModel != "" || run.RequestedEffort != "" {
			if run.Reviewer == "codex" {
				measurement.codexModel, measurement.codexEffort = run.RequestedModel, run.RequestedEffort
			} else {
				measurement.claudeModel, measurement.claudeEffort = run.RequestedModel, run.RequestedEffort
			}
		}
	}
	stamp := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	report := measurement.report(all, arms, runs, stamp)
	return os.WriteFile(strings.TrimSuffix(path, ".jsonl")+".md", []byte(report), 0o600)
}

// labelKey names one finding of one run for the labels file.
func labelKey(run Run, finding Finding) string {
	return fmt.Sprintf("%s/%s/%d: %s — %s", run.Arm, run.Range, run.Iteration, finding.Place, finding.Title)
}

// configuredCodexModel is the model of the user's Codex configuration ($CODEX_HOME, else ~/.codex),
// which a direct run uses; "" when it names none.
func configuredCodexModel() string {
	folder := os.Getenv("CODEX_HOME")
	if folder == "" {
		home, _ := os.UserHomeDir()
		folder = filepath.Join(home, ".codex")
	}
	data, err := os.ReadFile(filepath.Join(folder, "config.toml")) // #nosec G304 G703 -- the user's own configuration, named by their environment
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found && strings.TrimSpace(key) == "model" {
			return strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	return ""
}

// configuredClaudeModel is the model of the user's Claude Code settings, which a direct run uses.
func configuredClaudeModel() string {
	folder := os.Getenv("CLAUDE_CONFIG_DIR")
	if folder == "" {
		home, _ := os.UserHomeDir()
		folder = filepath.Join(home, ".claude")
	}
	data, err := os.ReadFile(filepath.Join(folder, "settings.json")) // #nosec G304 G703 -- the user's own settings, named by their environment
	if err != nil {
		return ""
	}
	var settings struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(data, &settings)
	return settings.Model
}

func (measurement *bench) git(folder string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", folder}, args...)...) // #nosec G204 -- fixed git subcommands
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=bench", "GIT_AUTHOR_EMAIL=bench@example.invalid",
		"GIT_COMMITTER_NAME=bench", "GIT_COMMITTER_EMAIL=bench@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output)), nil
}

// makeFixture builds the fixture's repository: the base, then one commit per range, and a checkout
// at each range's head for the direct arms.
func (measurement *bench) makeFixture() error {
	repository := filepath.Join(measurement.work, "fixture")
	if err := copyTree(filepath.Join(measurement.fixture, "fixture", "base"), repository); err != nil {
		return err
	}
	config := `{"jackstay_schema": 3, "project": "review-bench", "review": {"use": ["codex"], "cite": []}}` + "\n"
	if err := os.WriteFile(filepath.Join(repository, "jackstay.json"), []byte(config), 0o600); err != nil {
		return err
	}
	for _, step := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "The base: stock of items"}} {
		if _, err := measurement.git(repository, step...); err != nil {
			return err
		}
	}
	head, err := measurement.git(repository, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	measurement.commits = []string{head}
	for _, part := range measurement.defects.Ranges {
		if err := copyTree(filepath.Join(measurement.fixture, "fixture", part.Step), repository); err != nil {
			return err
		}
		if _, err := measurement.git(repository, "add", "-A"); err != nil {
			return err
		}
		if _, err := measurement.git(repository, "commit", "-q", "-m", part.Subject); err != nil {
			return err
		}
		if head, err = measurement.git(repository, "rev-parse", "HEAD"); err != nil {
			return err
		}
		measurement.commits = append(measurement.commits, head)
		checkout := filepath.Join(measurement.work, "checkout-"+part.Name)
		if _, err := measurement.git(repository, "worktree", "add", "-q", "--detach", checkout, head); err != nil {
			return err
		}
		measurement.checkouts = append(measurement.checkouts, checkout)
	}
	return nil
}

func copyTree(source, target string) error {
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(target, relative), 0o700)
		}
		data, err := root.ReadFile(relative)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(target, relative), data, 0o600) // #nosec G703 -- a path under the fixture, into the bench's own folder
	})
}

func reviewerOf(arm string) string {
	if strings.HasPrefix(arm, "claude") {
		return "claude-code"
	}
	return "codex"
}

// one runs one arm on one range, and scores it.
func (measurement *bench) one(arm string, position, iteration int) Run {
	part := measurement.defects.Ranges[position]
	run := Run{Arm: arm, Reviewer: reviewerOf(arm), Range: part.Name, Iteration: iteration, Started: time.Now(),
		RequestedModel: measurement.codexModel, RequestedEffort: measurement.codexEffort}
	if run.Reviewer == "claude-code" {
		run.RequestedModel, run.RequestedEffort = measurement.claudeModel, measurement.claudeEffort
	}
	base, head := measurement.commits[position], measurement.commits[position+1]
	var answer review.Answer
	var err error
	switch arm {
	case "codex-jst", "claude-jst":
		answer, err = measurement.wrapped(&run, base, head, fmt.Sprintf("bench-%s-%s-%d-%d", arm, part.Name, iteration, run.Started.Unix()))
	case "codex-direct":
		answer, err = measurement.directCodex(&run, measurement.checkouts[position], base, head)
	case "claude-direct":
		answer, err = measurement.directClaude(&run, measurement.checkouts[position], base, head)
	default:
		err = fmt.Errorf("unknown arm %q", arm)
	}
	run.Wall = time.Since(run.Started).Seconds()
	if err != nil {
		run.Outcome = "failed: " + firstLine(err.Error())
		return run
	}
	run.Outcome = "answered"
	for _, finding := range answer.Findings {
		run.Findings = append(run.Findings, Finding{Place: finding.Place, Title: finding.Title, Detail: finding.Detail})
	}
	measurement.score(&run)
	return run
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	if len(line) > 200 {
		line = line[:200]
	}
	return line
}

// wrapped is `jst review BASE..HEAD` with one reviewer, a fresh chain, both arms' model.
func (measurement *bench) wrapped(run *Run, base, head, task string) (review.Answer, error) {
	args := []string{"review", base + ".." + head, "--task", task, "--reviewer", run.Reviewer, "--fresh", "--json"}
	model, effort := measurement.codexModel, measurement.codexEffort
	if run.Reviewer == "claude-code" {
		model, effort = measurement.claudeModel, measurement.claudeEffort
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	ctx, cancel := context.WithTimeout(context.Background(), measurement.timeout)
	defer cancel()
	captured, runErr := supervise.Capture(ctx, append([]string{measurement.jst}, args...), filepath.Join(measurement.work, "fixture"),
		append(os.Environ(), "JACKSTAY_HOME="+measurement.home), nil)
	stdout, stderr := bytes.NewBuffer(captured.Stdout), bytes.NewBuffer(captured.Stderr)
	var result struct {
		Status int                 `json:"status"`
		Folder string              `json:"folder"`
		Next   string              `json:"next"`
		Round  *review.RoundRecord `json:"round"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return review.Answer{}, fmt.Errorf("jst review gave no result (%v): %s", runErr, firstLine(stderr.String()))
	}
	if result.Round == nil {
		return review.Answer{}, fmt.Errorf("no round (exit %d): %s", result.Status, result.Next)
	}
	run.Prepare = result.Round.PrepareSeconds
	for _, reviewer := range result.Round.Runs {
		run.Wait, run.Model = reviewer.WaitSeconds, reviewer.Seconds
	}
	folder := filepath.Join(result.Folder, run.Reviewer)
	if run.Reviewer == "codex" {
		run.Models = []string{model}
		run.Tokens = codexTokens(filepath.Join(folder, "events.jsonl"))
	} else {
		var usage struct {
			Input, CachedInput, CacheWrite, Output int64
			Models                                 []string `json:"models"`
			CostUSD                                float64  `json:"cost_usd"`
		}
		if data, err := os.ReadFile(filepath.Join(folder, "usage.json")); err == nil { // #nosec G304 -- the round's own file
			_ = json.Unmarshal(data, &usage)
			var tokens struct {
				Input       int64 `json:"input_tokens"`
				CachedInput int64 `json:"cache_read_input_tokens"`
				CacheWrite  int64 `json:"cache_creation_input_tokens"`
				Output      int64 `json:"output_tokens"`
			}
			_ = json.Unmarshal(data, &tokens)
			run.Tokens = tokens.Input + tokens.CachedInput + tokens.CacheWrite + tokens.Output
		}
		run.Models, run.CostUSD = usage.Models, usage.CostUSD
	}
	data, err := os.ReadFile(filepath.Join(folder, "answer.json")) // #nosec G304 -- the round's own file
	if err != nil {
		return review.Answer{}, err
	}
	var answer review.Answer
	return answer, json.Unmarshal(data, &answer)
}

// codexTokens are the tokens of a Codex run's turn.completed events.
func codexTokens(path string) int64 {
	data, err := os.ReadFile(path) // #nosec G304 -- a run's own events
	if err != nil {
		return 0
	}
	var total int64
	for _, line := range strings.Split(string(data), "\n") {
		var event struct {
			Type  string `json:"type"`
			Usage struct {
				Input  int64 `json:"input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == "turn.completed" {
			total += event.Usage.Input + event.Usage.Output
		}
	}
	return total
}

// directPrompt is what a person asks a reviewer by hand: the range, what to look for, the answer's form.
func directPrompt(base, head string) string {
	return fmt.Sprintf(`Review the change from commit %s to commit %s in this Git repository: read it with `+
		"`git diff %s %s`"+` and read the files as you need. Report every defect the change introduces: a wrong result, a
crash, a race, a security problem, a resource leak. For each finding give its place (file and function), a
severity (blocker, major or minor), its likelihood (ordinary or unusual) and consequence (severe or mild), a one-line
title and a detail. Leave "prior" empty and "repeats" empty. Answer only with the JSON object of the given schema.
`, base, head, base, head)
}

func (measurement *bench) schemaFile() (string, error) {
	path := filepath.Join(measurement.work, "schema.json")
	return path, os.WriteFile(path, review.Schema([]string{"blocker", "major", "minor"}), 0o600)
}

func (measurement *bench) directCodex(run *Run, checkout, base, head string) (review.Answer, error) {
	schema, err := measurement.schemaFile()
	if err != nil {
		return review.Answer{}, err
	}
	folder, err := os.MkdirTemp(measurement.work, "codex-direct-")
	if err != nil {
		return review.Answer{}, err
	}
	answerPath := filepath.Join(folder, "answer.json")
	args := []string{"-a", "never", "exec", "--json", "--ephemeral", "-s", "read-only", "-C", checkout,
		"-o", answerPath, "--output-schema", schema}
	if measurement.codexModel != "" {
		args = append(args, "-m", measurement.codexModel)
	}
	if measurement.codexEffort != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", measurement.codexEffort))
	}
	args = append(args, "-")
	ctx, cancel := context.WithTimeout(context.Background(), measurement.timeout)
	defer cancel()
	started := time.Now()
	captured, runErr := supervise.Capture(ctx, append([]string{"codex"}, args...), checkout, os.Environ(), []byte(directPrompt(base, head)))
	if runErr == nil && captured.Status != 0 {
		runErr = fmt.Errorf("exit status %d", captured.Status)
	}
	if err := os.WriteFile(filepath.Join(folder, "events.jsonl"), captured.Stdout, 0o600); err != nil {
		return review.Answer{}, err
	}
	run.Model = time.Since(started).Seconds()
	run.Models = []string{measurement.codexModel}
	run.Tokens = codexTokens(filepath.Join(folder, "events.jsonl"))
	if runErr != nil {
		return review.Answer{}, fmt.Errorf("codex failed: %w", runErr)
	}
	data, err := os.ReadFile(answerPath) // #nosec G304 -- the bench's own folder
	if err != nil {
		return review.Answer{}, fmt.Errorf("codex gave no answer: %w", err)
	}
	return complete(data)
}

// complete is a direct arm's answer held to the wrapped arm's rules: one object of the schema, not
// an incomplete verdict.
func complete(data []byte) (review.Answer, error) {
	var answer review.Answer
	if err := json.Unmarshal(data, &answer); err != nil {
		return review.Answer{}, fmt.Errorf("the answer does not read: %w", err)
	}
	if answer.Verdict != "approve" && answer.Verdict != "changes" {
		return review.Answer{}, fmt.Errorf("the verdict is %q, not a completed review", answer.Verdict)
	}
	settings := review.Defaults()
	if problem := review.CheckAnswer(answer, settings, nil); problem != "" {
		return review.Answer{}, fmt.Errorf("the answer does not check as the wrapped arm's would: %s", problem)
	}
	return answer, nil
}

func (measurement *bench) directClaude(run *Run, checkout, base, head string) (review.Answer, error) {
	args := []string{"--print", "--output-format", "json", "--no-session-persistence",
		"--json-schema", string(review.Schema([]string{"blocker", "major", "minor"})),
		"--permission-mode", "dontAsk", "--allowedTools", "Read,Glob,Grep,Bash(git diff:*),Bash(git show:*),Bash(git log:*)"}
	if measurement.claudeModel != "" {
		args = append(args, "--model", measurement.claudeModel)
	}
	if measurement.claudeEffort != "" {
		args = append(args, "--effort", measurement.claudeEffort)
	}
	ctx, cancel := context.WithTimeout(context.Background(), measurement.timeout)
	defer cancel()
	started := time.Now()
	captured, runErr := supervise.Capture(ctx, append([]string{"claude"}, args...), checkout, os.Environ(), []byte(directPrompt(base, head)))
	if runErr == nil && captured.Status != 0 {
		runErr = fmt.Errorf("exit status %d", captured.Status)
	}
	stdout := bytes.NewBuffer(captured.Stdout)
	run.Model = time.Since(started).Seconds()
	var envelope struct {
		Subtype    string          `json:"subtype"`
		Structured json.RawMessage `json:"structured_output"`
		CostUSD    float64         `json:"total_cost_usd"`
		ModelUsage map[string]any  `json:"modelUsage"`
		Usage      struct {
			Input       int64 `json:"input_tokens"`
			CachedInput int64 `json:"cache_read_input_tokens"`
			CacheWrite  int64 `json:"cache_creation_input_tokens"`
			Output      int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if runErr != nil {
		return review.Answer{}, fmt.Errorf("claude failed: %w", runErr)
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || envelope.Subtype != "success" ||
		len(envelope.Structured) == 0 || string(envelope.Structured) == "null" {
		return review.Answer{}, fmt.Errorf("claude gave no structured answer (%s)", envelope.Subtype)
	}
	for model := range envelope.ModelUsage {
		run.Models = append(run.Models, model)
	}
	slices.Sort(run.Models)
	run.CostUSD = envelope.CostUSD
	run.Tokens = envelope.Usage.Input + envelope.Usage.CachedInput + envelope.Usage.CacheWrite + envelope.Usage.Output
	return complete(envelope.Structured)
}

// match is the defect of list in run's range that finding names, with its score. Only the
// finding's place and title count, the detail being context that names neighbours in passing: the
// place must name the defect's file, or its function as a whole word, and each of the defect's
// words — specific to its mechanism — found in the place or title scores one. A finding no word
// matches is left to a person (the labels file).
func match(list []Defect, rangeName string, finding Finding) (string, int) {
	place := strings.ToLower(finding.Place)
	where := place + " " + strings.ToLower(finding.Title)
	best, bestScore := "", 0
	for _, defect := range list {
		if defect.Range != rangeName {
			continue
		}
		named := regexp.MustCompile(`\b` + regexp.QuoteMeta(strings.ToLower(defect.Symbol)) + `\b`)
		if !strings.Contains(place, strings.ToLower(defect.File)) && !named.MatchString(place) {
			continue
		}
		score := 0
		for _, word := range defect.Words {
			if strings.Contains(where, strings.ToLower(word)) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = defect.ID, score
		}
	}
	return best, bestScore
}

// score matches each finding to the seeded defect or confirmed extra it names best (an extra wins
// a tie: a finding of a known unseeded issue never earns a seeded defect), else counts it
// unmatched. A person's label for the finding (labels) overrides the match: a defect or extra id,
// or "false" for a finding that is not a real issue.
func (measurement *bench) score(run *Run) {
	found := map[string]bool{}
	run.Found, run.Extras, run.Unmatched, run.Duplicates, run.False = nil, 0, 0, 0, 0
	for index, finding := range run.Findings {
		best, bestScore := match(measurement.defects.Defects, run.Range, finding)
		if extra, extraScore := match(measurement.defects.Extras, run.Range, finding); extraScore >= bestScore && extraScore > 0 {
			best = extra
		}
		if label, labelled := measurement.labels[labelKey(*run, finding)]; labelled {
			best = label
		}
		if best == "false" {
			run.Findings[index].Match = best
			run.False++
			continue
		}
		run.Findings[index].Match = best
		switch {
		case best == "":
			run.Unmatched++
		case strings.HasPrefix(best, "E"):
			run.Extras++
		case found[best]:
			run.Duplicates++
		default:
			found[best] = true
		}
	}
	for id := range found {
		run.Found = append(run.Found, id)
	}
	sort.Strings(run.Found)
}

type stat struct{ values []float64 }

func (series *stat) add(value float64) { series.values = append(series.values, value) }

func (series stat) mean() float64 {
	if len(series.values) == 0 {
		return math.NaN()
	}
	total := 0.0
	for _, value := range series.values {
		total += value
	}
	return total / float64(len(series.values))
}

func (series stat) deviation() float64 {
	if len(series.values) < 2 {
		return 0
	}
	mean, total := series.mean(), 0.0
	for _, value := range series.values {
		total += (value - mean) * (value - mean)
	}
	return math.Sqrt(total / float64(len(series.values)-1))
}

func (series stat) text(format string) string {
	if len(series.values) == 0 {
		return "–"
	}
	return fmt.Sprintf(format+" ± "+format, series.mean(), series.deviation())
}

// report is the Markdown summary: one row per arm, then per range, then every unmatched finding
// for a person to confirm or reject.
func (measurement *bench) report(all []Run, arms []string, runs int, stamp string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Review wrapper measurement, %s\n\n", stamp)
	fmt.Fprintf(&out, "%d seeded defects over %d ranges (`bench/review/testdata`), %d sequential runs per arm and range, "+
		"arms in turning order. Codex model %q (requested; Codex does not report the model it ran), effort %q; Claude model %q "+
		"(requested; the models column shows what Claude Code reported), effort %q; both arms of a reviewer alike.\n\n",
		len(measurement.defects.Defects), len(measurement.defects.Ranges), runs, measurement.codexModel, measurement.codexEffort, measurement.claudeModel, measurement.claudeEffort)
	out.WriteString("Recall: seeded defects found per run, of those in the range. Precision: confirmed findings — a seeded " +
		"defect, or one of the fixture's known real issues that were not seeded (`extras`) — of all findings; the findings " +
		"that match neither are listed below for a person to judge. Mean ± standard deviation over runs.\n\n")
	out.WriteString("| arm | runs answered | recall | precision | extras per run | wall s | prepare s | wait s | model and tools s | tokens | cost USD | models |\n")
	out.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, arm := range arms {
		var recall, precision, extras, wall, prepare, wait, model, tokens, cost stat
		answered, total := 0, 0
		models := map[string]bool{}
		for _, run := range all {
			if run.Arm != arm {
				continue
			}
			total++
			if run.Outcome != "answered" {
				continue
			}
			answered++
			seeded := 0
			for _, defect := range measurement.defects.Defects {
				if defect.Range == run.Range {
					seeded++
				}
			}
			recall.add(float64(len(run.Found)) / float64(seeded))
			if len(run.Findings) > 0 {
				precision.add(float64(len(run.Findings)-run.Unmatched-run.False) / float64(len(run.Findings)))
			}
			extras.add(float64(run.Extras))
			wall.add(run.Wall)
			model.add(run.Model)
			if strings.HasSuffix(arm, "-jst") {
				prepare.add(run.Prepare)
				wait.add(run.Wait)
			}
			tokens.add(float64(run.Tokens))
			if run.CostUSD > 0 {
				cost.add(run.CostUSD)
			}
			for _, name := range run.Models {
				models[name] = true
			}
		}
		names := make([]string, 0, len(models))
		for name := range models {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintf(&out, "| %s | %d/%d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", arm, answered, total,
			recall.text("%.2f"), precision.text("%.2f"), extras.text("%.1f"), wall.text("%.0f"), prepare.text("%.1f"), wait.text("%.1f"),
			model.text("%.0f"), tokens.text("%.0f"), cost.text("%.3f"), strings.Join(names, ", "))
	}
	out.WriteString("\n## Defects found, by arm (runs that found each)\n\n| defect | what | ")
	out.WriteString(strings.Join(arms, " | ") + " |\n|---|---|" + strings.Repeat("---|", len(arms)) + "\n")
	for _, defect := range measurement.defects.Defects {
		fmt.Fprintf(&out, "| %s | %s |", defect.ID, defect.What)
		for _, arm := range arms {
			hits, runsOfRange := 0, 0
			for _, run := range all {
				if run.Arm == arm && run.Range == defect.Range {
					runsOfRange++
					if slices.Contains(run.Found, defect.ID) {
						hits++
					}
				}
			}
			fmt.Fprintf(&out, " %d/%d |", hits, runsOfRange)
		}
		out.WriteString("\n")
	}
	out.WriteString("\n## Findings that name no seeded defect\n\n")
	for _, run := range all {
		for _, finding := range run.Findings {
			if finding.Match == "" {
				fmt.Fprintf(&out, "- %s, %s, run %d: `%s` — %s\n", run.Arm, run.Range, run.Iteration, finding.Place, finding.Title)
			}
		}
	}
	failures := 0
	for _, run := range all {
		if run.Outcome != "answered" {
			if failures == 0 {
				out.WriteString("\n## Runs without an answer\n\n")
			}
			failures++
			fmt.Fprintf(&out, "- %s, %s, run %d: %s\n", run.Arm, run.Range, run.Iteration, run.Outcome)
		}
	}
	out.WriteString("\nRaw runs: `" + stamp + ".jsonl` beside this file. " + strconv.Itoa(len(all)) + " runs in all.\n")
	return out.String()
}
