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
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/review"
)

// Defects is testdata/defects.json: the fixture's ranges and the defects seeded in each.
type Defects struct {
	Ranges []struct {
		Name    string `json:"name"`
		Step    string `json:"step"`
		Subject string `json:"subject"`
	} `json:"ranges"`
	Defects []Defect `json:"defects"`
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
	Arm        string    `json:"arm"`
	Reviewer   string    `json:"reviewer"`
	Range      string    `json:"range"`
	Iteration  int       `json:"iteration"`
	Started    time.Time `json:"started"`
	Wall       float64   `json:"wall_seconds"`
	Prepare    float64   `json:"prepare_seconds"` // source view (wrapped only)
	Wait       float64   `json:"wait_seconds"`    // slot and start lock (wrapped only)
	Model      float64   `json:"model_seconds"`   // the reviewer's own run: model and tools
	Models     []string  `json:"models"`
	CostUSD    float64   `json:"cost_usd"`
	Tokens     int64     `json:"tokens"`
	Outcome    string    `json:"outcome"`
	Findings   []Finding `json:"findings"`
	Found      []string  `json:"found"`      // seeded defects found
	Unmatched  int       `json:"unmatched"`  // findings that name no seeded defect
	Duplicates int       `json:"duplicates"` // findings of a defect already found in this run
}

// Finding is the part of a reviewer's finding that is scored.
type Finding struct {
	Place  string `json:"place"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Match  string `json:"match,omitempty"`
}

type bench struct {
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
	var arms, out string
	var runs int
	b := bench{log: os.Stderr}
	flag.StringVar(&b.jst, "jst", "", "the jst binary to measure (absolute)")
	flag.StringVar(&arms, "arms", "codex-jst,codex-direct,claude-jst,claude-direct", "the arms, comma-separated")
	flag.IntVar(&runs, "runs", 5, "sequential runs of every arm on every range")
	flag.StringVar(&b.codexModel, "codex-model", "", "the Codex model both Codex arms use (default: the one in ~/.codex/config.toml)")
	flag.StringVar(&b.claudeModel, "claude-model", "", "the Claude model both Claude arms use (default: the one in ~/.claude/settings.json)")
	flag.StringVar(&b.codexEffort, "codex-effort", "medium", "Codex's reasoning effort, for both Codex arms")
	flag.StringVar(&b.claudeEffort, "claude-effort", "", "Claude Code's effort, for both Claude arms")
	flag.StringVar(&out, "out", "bench/review/results", "where the raw runs and the report are written")
	flag.DurationVar(&b.timeout, "timeout", 15*time.Minute, "one run's deadline")
	flag.Parse()
	if err := b.main(strings.Split(arms, ","), runs, out); err != nil {
		fmt.Fprintln(os.Stderr, "review bench:", err)
		os.Exit(1)
	}
}

func (b *bench) main(arms []string, runs int, out string) error {
	if !filepath.IsAbs(b.jst) {
		return errors.New("-jst must be an absolute path to a jst binary")
	}
	var err error
	if b.root, err = os.Getwd(); err != nil {
		return err
	}
	b.fixture = filepath.Join(b.root, "bench", "review", "testdata")
	data, err := os.ReadFile(filepath.Join(b.fixture, "defects.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &b.defects); err != nil {
		return err
	}
	if b.codexModel == "" {
		b.codexModel = configuredCodexModel()
	}
	if b.claudeModel == "" {
		b.claudeModel = configuredClaudeModel()
	}
	if b.work, err = os.MkdirTemp("", "review-bench-"); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(b.work) }()
	b.home = filepath.Join(b.work, "jackstay-home")
	if err := b.makeFixture(); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("2006-01-02T150405Z")
	raw, err := os.Create(filepath.Join(out, stamp+".jsonl")) // #nosec G304 -- the operator's output folder
	if err != nil {
		return err
	}
	defer func() { _ = raw.Close() }()
	var all []Run
	for iteration := 1; iteration <= runs; iteration++ {
		// The order of the arms turns each iteration, so drift over the hours hits every arm alike.
		order := append(slices.Clone(arms[(iteration-1)%len(arms):]), arms[:(iteration-1)%len(arms)]...)
		for position := range b.defects.Ranges {
			for _, arm := range order {
				run := b.one(arm, position, iteration)
				all = append(all, run)
				line, _ := json.Marshal(run)
				_, _ = raw.Write(append(line, '\n'))
				fmt.Fprintf(b.log, "%s %-14s %-7s run %d: %-10s found %s, %d unmatched, %.0f s\n", time.Now().Format("15:04:05"),
					arm, run.Range, iteration, run.Outcome, strings.Join(run.Found, ","), run.Unmatched, run.Wall)
			}
		}
	}
	report := b.report(all, arms, runs, stamp)
	path := filepath.Join(out, stamp+".md")
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil { // #nosec G306 -- a report meant to be read
		return err
	}
	fmt.Println(path)
	return nil
}

// configuredCodexModel is the model of the user's Codex configuration, which a direct run uses.
func configuredCodexModel() string {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml")) // #nosec G304 -- the user's own configuration
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
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json")) // #nosec G304 -- the user's own settings
	if err != nil {
		return ""
	}
	var settings struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(data, &settings)
	return settings.Model
}

func (b *bench) git(folder string, args ...string) (string, error) {
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
func (b *bench) makeFixture() error {
	repository := filepath.Join(b.work, "fixture")
	if err := copyTree(filepath.Join(b.fixture, "fixture", "base"), repository); err != nil {
		return err
	}
	config := `{"jackstay_schema": 3, "project": "review-bench", "review": {"use": ["codex"], "cite": []}}` + "\n"
	if err := os.WriteFile(filepath.Join(repository, "jackstay.json"), []byte(config), 0o600); err != nil {
		return err
	}
	for _, step := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "The base: stock of items"}} {
		if _, err := b.git(repository, step...); err != nil {
			return err
		}
	}
	head, err := b.git(repository, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	b.commits = []string{head}
	for _, part := range b.defects.Ranges {
		if err := copyTree(filepath.Join(b.fixture, "fixture", part.Step), repository); err != nil {
			return err
		}
		if _, err := b.git(repository, "add", "-A"); err != nil {
			return err
		}
		if _, err := b.git(repository, "commit", "-q", "-m", part.Subject); err != nil {
			return err
		}
		if head, err = b.git(repository, "rev-parse", "HEAD"); err != nil {
			return err
		}
		b.commits = append(b.commits, head)
		checkout := filepath.Join(b.work, "checkout-"+part.Name)
		if _, err := b.git(repository, "worktree", "add", "-q", "--detach", checkout, head); err != nil {
			return err
		}
		b.checkouts = append(b.checkouts, checkout)
	}
	return nil
}

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(source, path)
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(target, relative), 0o700)
		}
		data, err := os.ReadFile(path) // #nosec G304 -- the fixture's own files
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(target, relative), data, 0o600)
	})
}

func reviewerOf(arm string) string {
	if strings.HasPrefix(arm, "claude") {
		return "claude-code"
	}
	return "codex"
}

// one runs one arm on one range, and scores it.
func (b *bench) one(arm string, position, iteration int) Run {
	part := b.defects.Ranges[position]
	run := Run{Arm: arm, Reviewer: reviewerOf(arm), Range: part.Name, Iteration: iteration, Started: time.Now()}
	base, head := b.commits[position], b.commits[position+1]
	var answer review.Answer
	var err error
	switch arm {
	case "codex-jst", "claude-jst":
		answer, err = b.wrapped(&run, base, head, fmt.Sprintf("bench-%s-%s-%d-%d", arm, part.Name, iteration, run.Started.Unix()))
	case "codex-direct":
		answer, err = b.directCodex(&run, b.checkouts[position], base, head)
	case "claude-direct":
		answer, err = b.directClaude(&run, b.checkouts[position], base, head)
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
	b.score(&run)
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
func (b *bench) wrapped(run *Run, base, head, task string) (review.Answer, error) {
	args := []string{"review", base + ".." + head, "--task", task, "--reviewer", run.Reviewer, "--fresh", "--json"}
	model, effort := b.codexModel, b.codexEffort
	if run.Reviewer == "claude-code" {
		model, effort = b.claudeModel, b.claudeEffort
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	command := exec.CommandContext(ctx, b.jst, args...) // #nosec G204 -- the binary being measured
	command.Dir = filepath.Join(b.work, "fixture")
	command.Env = append(os.Environ(), "JACKSTAY_HOME="+b.home)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
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

func (b *bench) schemaFile() (string, error) {
	path := filepath.Join(b.work, "schema.json")
	return path, os.WriteFile(path, review.Schema([]string{"blocker", "major", "minor"}), 0o600)
}

func (b *bench) directCodex(run *Run, checkout, base, head string) (review.Answer, error) {
	schema, err := b.schemaFile()
	if err != nil {
		return review.Answer{}, err
	}
	folder, err := os.MkdirTemp(b.work, "codex-direct-")
	if err != nil {
		return review.Answer{}, err
	}
	answerPath := filepath.Join(folder, "answer.json")
	args := []string{"-a", "never", "exec", "--json", "--ephemeral", "-s", "read-only", "-C", checkout,
		"-o", answerPath, "--output-schema", schema}
	if b.codexModel != "" {
		args = append(args, "-m", b.codexModel)
	}
	if b.codexEffort != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", b.codexEffort))
	}
	args = append(args, "-")
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	command := exec.CommandContext(ctx, "codex", args...) // #nosec G204 -- the reviewer measured
	command.Dir = checkout
	command.Stdin = strings.NewReader(directPrompt(base, head))
	events, err := os.Create(filepath.Join(folder, "events.jsonl")) // #nosec G304 -- the bench's own folder
	if err != nil {
		return review.Answer{}, err
	}
	command.Stdout, command.Stderr = events, io.Discard
	started := time.Now()
	runErr := command.Run()
	_ = events.Close()
	run.Model = time.Since(started).Seconds()
	run.Models = []string{b.codexModel}
	run.Tokens = codexTokens(filepath.Join(folder, "events.jsonl"))
	data, err := os.ReadFile(answerPath) // #nosec G304 -- the bench's own folder
	if err != nil {
		return review.Answer{}, fmt.Errorf("codex gave no answer (%v)", runErr)
	}
	var answer review.Answer
	return answer, json.Unmarshal(data, &answer)
}

func (b *bench) directClaude(run *Run, checkout, base, head string) (review.Answer, error) {
	args := []string{"--print", "--output-format", "json", "--no-session-persistence",
		"--json-schema", string(review.Schema([]string{"blocker", "major", "minor"})),
		"--permission-mode", "dontAsk", "--allowedTools", "Read,Glob,Grep,Bash(git diff:*),Bash(git show:*),Bash(git log:*)"}
	if b.claudeModel != "" {
		args = append(args, "--model", b.claudeModel)
	}
	if b.claudeEffort != "" {
		args = append(args, "--effort", b.claudeEffort)
	}
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	command := exec.CommandContext(ctx, "claude", args...) // #nosec G204 -- the reviewer measured
	command.Dir = checkout
	command.Stdin = strings.NewReader(directPrompt(base, head))
	var stdout bytes.Buffer
	command.Stdout, command.Stderr = &stdout, io.Discard
	started := time.Now()
	runErr := command.Run()
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
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || len(envelope.Structured) == 0 {
		return review.Answer{}, fmt.Errorf("claude gave no structured answer (%v, %s)", runErr, envelope.Subtype)
	}
	for model := range envelope.ModelUsage {
		run.Models = append(run.Models, model)
	}
	slices.Sort(run.Models)
	run.CostUSD = envelope.CostUSD
	run.Tokens = envelope.Usage.Input + envelope.Usage.CachedInput + envelope.Usage.CacheWrite + envelope.Usage.Output
	var answer review.Answer
	return answer, json.Unmarshal(envelope.Structured, &answer)
}

// score matches each finding to at most one seeded defect of its range: the finding must name the
// defect's file or symbol and use one of its words; the defect with the most words wins.
func (b *bench) score(run *Run) {
	found := map[string]bool{}
	for index, finding := range run.Findings {
		text := strings.ToLower(finding.Place + " " + finding.Title + " " + finding.Detail)
		where := strings.ToLower(finding.Place + " " + finding.Title)
		best, bestScore := "", 0
		for _, defect := range b.defects.Defects {
			if defect.Range != run.Range {
				continue
			}
			if !strings.Contains(where, strings.ToLower(defect.File)) && !strings.Contains(where, strings.ToLower(defect.Symbol)) &&
				!strings.Contains(text, strings.ToLower(defect.Symbol)) {
				continue
			}
			score := 0
			for _, word := range defect.Words {
				if strings.Contains(text, strings.ToLower(word)) {
					score++
				}
			}
			if score > bestScore {
				best, bestScore = defect.ID, score
			}
		}
		run.Findings[index].Match = best
		switch {
		case best == "":
			run.Unmatched++
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

func (s *stat) add(value float64) { s.values = append(s.values, value) }

func (s stat) mean() float64 {
	if len(s.values) == 0 {
		return math.NaN()
	}
	total := 0.0
	for _, value := range s.values {
		total += value
	}
	return total / float64(len(s.values))
}

func (s stat) deviation() float64 {
	if len(s.values) < 2 {
		return 0
	}
	mean, total := s.mean(), 0.0
	for _, value := range s.values {
		total += (value - mean) * (value - mean)
	}
	return math.Sqrt(total / float64(len(s.values)-1))
}

func (s stat) text(format string) string {
	if len(s.values) == 0 {
		return "–"
	}
	return fmt.Sprintf(format+" ± "+format, s.mean(), s.deviation())
}

// report is the Markdown summary: one row per arm, then per range, then every unmatched finding
// for a person to confirm or reject.
func (b *bench) report(all []Run, arms []string, runs int, stamp string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Review wrapper measurement, %s\n\n", stamp)
	fmt.Fprintf(&out, "%d seeded defects over %d ranges (`bench/review/testdata`), %d sequential runs per arm and range, "+
		"arms in turning order. Codex model %q, effort %q; Claude model %q, effort %q (both arms of a reviewer alike).\n\n",
		len(b.defects.Defects), len(b.defects.Ranges), runs, b.codexModel, b.codexEffort, b.claudeModel, b.claudeEffort)
	out.WriteString("Recall: seeded defects found per run, of those in the range. Precision: findings that name a seeded defect, " +
		"of all findings (unmatched findings are listed below for a person to confirm). Mean ± standard deviation over runs.\n\n")
	out.WriteString("| arm | runs answered | recall | precision | wall s | prepare s | wait s | model and tools s | tokens | cost USD | models |\n")
	out.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, arm := range arms {
		var recall, precision, wall, prepare, wait, model, tokens, cost stat
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
			for _, defect := range b.defects.Defects {
				if defect.Range == run.Range {
					seeded++
				}
			}
			recall.add(float64(len(run.Found)) / float64(seeded))
			if len(run.Findings) > 0 {
				precision.add(float64(len(run.Findings)-run.Unmatched) / float64(len(run.Findings)))
			}
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
		fmt.Fprintf(&out, "| %s | %d/%d | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", arm, answered, total,
			recall.text("%.2f"), precision.text("%.2f"), wall.text("%.0f"), prepare.text("%.1f"), wait.text("%.1f"),
			model.text("%.0f"), tokens.text("%.0f"), cost.text("%.3f"), strings.Join(names, ", "))
	}
	out.WriteString("\n## Defects found, by arm (runs that found each)\n\n| defect | what | ")
	out.WriteString(strings.Join(arms, " | ") + " |\n|---|---|" + strings.Repeat("---|", len(arms)) + "\n")
	for _, defect := range b.defects.Defects {
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
