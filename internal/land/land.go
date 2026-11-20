// Package land is `jst land` (docs/design/land.md): push the branch, open the change request,
// wait for the checks the base's rules require, and merge at the head that was checked, when the
// project's policy lets the agent; otherwise stop with the change ready for a person. It asks the
// code host through internal/hosting (GitHub's adapter is internal/gh) and never switches, pulls
// or rebases the checkout it runs in (W-019).
package land

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gh"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/hosting"
	"github.com/zima-oss/jackstay/internal/remote"
	"github.com/zima-oss/jackstay/internal/telemetry"
)

func init() {
	cli.Register(cli.Command{Name: "land", Summary: "push and open a change request, wait for its required checks, merge when the policy allows",
		Usage: help, Run: Run})
}

// The exit codes of the contract (docs/design/land.md section 3.2).
const (
	exitLanded     = 0
	exitRefused    = 1
	exitInvalid    = 2
	exitPending    = 3
	exitMoved      = 4
	exitUnexpected = 5
	exitForPerson  = 6
	// exitPlanned is --plan's stop: exit 0, and the last line says planned.
	exitPlanned = -1
)

// stateOf is the last line's word for an exit code.
var stateOf = map[int]string{exitLanded: "landed", exitRefused: "refused", exitInvalid: "invalid", exitPending: "pending",
	exitMoved: "moved", exitUnexpected: "unexpected", exitForPerson: "ready for a person"}

// How long jst land lets the host compute mergeability, or show a branch update, after a push.
const (
	mergeableWait = 60 * time.Second
	mergeableStep = 5 * time.Second
	// checksAppearWait is how long a base with no gate waits for a first check to register.
	checksAppearWait = 120 * time.Second
	// commandBound bounds a whole call, for the host client's rate-limit waits.
	commandBound = 6 * time.Hour
)

// Git asks git a question in the checkout (internal/gitcli.Git is one).
type Git interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// Clock is the time waits are measured on; tests pass their own (W-013).
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, wait time.Duration) error
}

// Deps are what a call of jst land works with.
type Deps struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Dir is the working folder: its jackstay.json and its checkout.
	Dir string
	Git func(dir string) Git
	// Connect is the host for repository (owner/name), as the `github` section's account; the
	// note, when not empty, is said on standard error.
	Connect func(ctx context.Context, settings config.GitHub, repository string) (host hosting.Host, note string, err error)
	Clock   Clock
	// Terminal says whether standard input is a terminal a person types at.
	Terminal func() bool
}

// Run is `jst land` with the words after it.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "status":
			return gh.StatusMain(args[1:], stdout, stderr)
		case "find":
			return gh.FindMain(args[1:], stdout, stderr)
		}
	}
	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "jst land: %v\n", err)
		return exitInvalid
	}
	return Main(args, Deps{Stdin: stdin, Stdout: stdout, Stderr: stderr, Dir: workDir,
		Git:      func(dir string) Git { return gitcli.Git{Dir: dir} },
		Connect:  connectGitHub,
		Clock:    realClock{},
		Terminal: stdinIsTerminal})
}

func connectGitHub(ctx context.Context, settings config.GitHub, repository string) (hosting.Host, string, error) {
	host, token, err := gh.Connect(ctx, settings.Account, repository)
	return host, token.Note, err
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Main is `jst land` with its dependencies given. It ends with `jst land: STATE` on standard
// error, so that standard output stays the plan and the steps.
func Main(args []string, deps Deps) int {
	code, err := run(args, deps)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(deps.Stderr, "jst land: %v\n", err)
		code = codeOf(err)
	}
	if code == exitPlanned {
		fmt.Fprintln(deps.Stderr, "jst land: planned")
		return 0
	}
	fmt.Fprintf(deps.Stderr, "jst land: %s\n", stateOf[code])
	return code
}

// exitError is a stop with the exit code it means.
type exitError struct {
	code    int
	message string
}

func (failure *exitError) Error() string { return failure.message }

func failf(code int, format string, values ...any) error {
	return &exitError{code: code, message: fmt.Sprintf(format, values...)}
}

// codeOf is the exit code an error means: its own, refused for an operation the host lacks, else
// unexpected (a host answer jackstay does not expect).
func codeOf(err error) int {
	var failure *exitError
	if errors.As(err, &failure) {
		return failure.code
	}
	if errors.Is(err, hosting.ErrUnsupported) {
		return exitRefused
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return exitPending
	}
	return exitUnexpected
}

// options are jst land's flags.
type options struct {
	change, title, bodyFile, reason string
	cards                           []string
	prepare, confirm, plan          bool
	timeout, interval               time.Duration
	lines                           int
}

func parseOptions(args []string, settings config.GitHub, stderr io.Writer) (options, error) {
	timeoutDefault, intervalDefault, linesDefault := 1800.0, 20.0, 25
	if settings.Wait.TimeoutSeconds > 0 {
		timeoutDefault = settings.Wait.TimeoutSeconds
	}
	if settings.Wait.IntervalSeconds > 0 {
		intervalDefault = settings.Wait.IntervalSeconds
	}
	if settings.Wait.LogLines > 0 {
		linesDefault = settings.Wait.LogLines
	}
	flags := flag.NewFlagSet("jst land", flag.ContinueOnError)
	flags.SetOutput(stderr)
	change := flags.String("change", "", "an open change request from this branch, by number or URL")
	title := flags.String("title", "", "the new change request's title")
	bodyFile := flags.String("body-file", "", "the new change request's description, from a file")
	var cardIDs listFlag
	flags.Var(&cardIDs, "card", "the card this is work for (repeatable), or `none` with --reason")
	reason := flags.String("reason", "", "why a change is no card's work (with --card none)")
	prepare := flags.Bool("prepare", false, "stop before the merge: a person merges")
	confirm := flags.Bool("confirm", false, "a person at a terminal confirms the merge by typing the change's number")
	plan := flags.Bool("plan", false, "print the plan and stop")
	timeout := flags.Float64("timeout", timeoutDefault, "seconds to wait for the checks")
	interval := flags.Float64("interval", intervalDefault, "seconds between looks")
	lines := flags.Int("lines", linesDefault, "log lines to show per failed check")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{}, err
		}
		return options{}, failf(exitInvalid, "%v", err)
	}
	if flags.NArg() > 0 {
		return options{}, failf(exitInvalid, "jst land takes no words besides its flags: %s", strings.Join(flags.Args(), " "))
	}
	chosen := options{change: *change, title: *title, bodyFile: *bodyFile, reason: *reason, cards: cardIDs,
		prepare: *prepare, confirm: *confirm, plan: *plan, lines: *lines}
	var err error
	if chosen.timeout, err = seconds("timeout", *timeout, false); err != nil {
		return options{}, err
	}
	if chosen.interval, err = seconds("interval", *interval, true); err != nil {
		return options{}, err
	}
	if chosen.lines < 1 {
		return options{}, failf(exitInvalid, "--lines must be above 0")
	}
	if chosen.prepare && chosen.confirm {
		return options{}, failf(exitInvalid, "--confirm is a person's merge and --prepare stops before any merge: give one")
	}
	return chosen, nil
}

// seconds is a number of seconds as a duration; a negative, infinite or NaN value is invalid.
func seconds(name string, value float64, positive bool) (time.Duration, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || (positive && value == 0) {
		return 0, failf(exitInvalid, "--%s must be a number of seconds%s", name, map[bool]string{true: " above 0", false: ""}[positive])
	}
	return time.Duration(value * float64(time.Second)), nil
}

// session is one call: the project, the checkout, the host once connected.
type session struct {
	deps       Deps
	ctx        context.Context
	git        Git
	config     config.Config
	settings   config.GitHub
	policy     config.Land
	host       hosting.Host
	repository string
	options    options
	branch     string
	base       string
	// remoteName is the remote this branch's work goes to.
	remoteName string
}

func run(args []string, deps Deps) (int, error) {
	_, loaded, err := config.Load(deps.Dir, []string{"github", "land", "cards"})
	if err != nil {
		return 0, failf(exitInvalid, "%v", err)
	}
	settings, _ := loaded.GitHub()
	chosen, err := parseOptions(args, settings, deps.Stderr)
	if err != nil {
		return 0, err
	}
	land, found := loaded.Land()
	if !found {
		return 0, failf(exitInvalid, "this project has no landing policy: jackstay.json has no `land` section. "+
			"`jst adopt` writes one (a person merges the change that adds it)")
	}
	if land.Host != "" && land.Host != "github" {
		return 0, failf(exitInvalid, "`land.host` is %q: this jackstay lands on github only", land.Host)
	}
	if chosen.confirm && (deps.Terminal == nil || !deps.Terminal()) {
		return 0, failf(exitInvalid, "--confirm is a person's answer at a terminal, and standard input is not one")
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandBound)
	defer cancel()
	work := &session{deps: deps, ctx: ctx, git: deps.Git(deps.Dir), config: loaded, settings: settings, policy: land, options: chosen}
	return work.run()
}

func (work *session) say(format string, values ...any) {
	fmt.Fprintf(work.deps.Stdout, format+"\n", values...)
}

func (work *session) gitLine(args ...string) (string, error) {
	out, err := work.git.Run(work.ctx, args...)
	return strings.TrimSpace(string(out)), err
}

// connect finds the checkout's repository and connects to its host.
func (work *session) connect() error {
	repository, err := remote.RepositoryOf(work.ctx, work.git, work.config.Section("github"))
	switch {
	case errors.Is(err, remote.ErrNoRemote):
		return failf(exitInvalid, "%s has no git remote: there is no host to land on", work.deps.Dir)
	case err != nil:
		return failf(exitInvalid, "which repository this is cannot be told: %v", err)
	}
	work.repository = repository.String()
	host, note, err := work.deps.Connect(work.ctx, work.settings, work.repository)
	if err != nil {
		return failf(exitInvalid, "the host cannot be asked: %v", err)
	}
	if note != "" {
		fmt.Fprintf(work.deps.Stderr, "jst land: %s\n", note)
	}
	work.host = host
	return nil
}

// run is steps 1 to 7 of docs/design/land.md section 3.1. The plan is printed first; --plan
// stops there.
func (work *session) run() (int, error) {
	line, err := cardLine(work.options.cards, work.options.reason)
	if err != nil {
		return 0, err
	}
	if work.branch, err = work.currentBranch(); err != nil {
		return 0, err
	}
	if clean, err := work.clean(); err != nil {
		return 0, err
	} else if !clean {
		return 0, failf(exitInvalid, "the work tree is dirty: commit first, or the uncommitted changes are left behind")
	}
	if err := work.connect(); err != nil {
		return 0, err
	}
	if work.base, err = work.host.DefaultBranch(work.ctx); err != nil {
		return 0, fmt.Errorf("the default branch of %s: %w", work.repository, err)
	}
	if work.branch == work.base {
		return 0, failf(exitInvalid, "on %s: a landing needs a feature branch", work.branch)
	}
	change, open, err := work.selectChange()
	if err != nil {
		return 0, err
	}
	if open {
		work.base = change.Base
	}
	if !open && work.options.title == "" {
		return 0, failf(exitInvalid, "a new change request needs --title")
	}
	if work.remoteName, err = work.pushRemote(); err != nil {
		return 0, err
	}
	if err := work.verifyPushRemote(work.remoteName); err != nil {
		return 0, err
	}
	ahead := work.ahead()
	rules, err := work.host.Rules(work.ctx, work.base)
	if err != nil {
		return 0, fmt.Errorf("the rules of %s: %w", work.base, err)
	}
	basePolicy, baseHasPolicy, err := work.basePolicyAtTip()
	if err != nil {
		return 0, err
	}
	baseTip := "refs/remotes/" + work.remoteName + "/" + work.base
	baseFile := work.fileAt(baseTip)
	if err := cardsNeedAMergeCommit(methodOf(basePolicy), cardsSectionOf(baseFile) != nil); err != nil {
		return 0, err
	}
	body, err := work.newDescription(open, line, baseFile, baseTip)
	if err != nil {
		return 0, err
	}
	work.printPlan(open, ahead, rules, basePolicy, baseHasPolicy)
	if work.options.plan {
		work.say("\n--plan: stopping here")
		return exitPlanned, nil
	}
	work.say("")

	// Step 1: push and open.
	if ahead != 0 {
		if err := work.push(ahead < 0); err != nil {
			return 0, err
		}
	}
	if !open {
		if change, err = work.open(body); err != nil {
			return 0, err
		}
	} else {
		work.say("change #%d already open into %s", change.Number, work.base)
	}
	telemetry.Annotate(attribute.String(telemetry.AttrGitHubRepository, work.repository),
		attribute.Int(telemetry.AttrGitHubPull, change.Number))
	if change.Draft {
		return 0, failf(exitRefused, "#%d is a draft: a draft is not merged", change.Number)
	}
	deadline := work.deps.Clock.Now().Add(work.options.timeout)

	ready, code, err := work.prepareLanding(change.Number, deadline)
	if err != nil || code != exitLanded {
		return code, err
	}
	for _, reason := range ready.reasons {
		work.say("for a person: %s", reason)
	}
	if work.options.confirm {
		// --confirm always asks, whether or not the agent could merge it, and judges it again after.
		if err := work.askPerson(ready.change.Number); err != nil {
			return 0, err
		}
		if ready, code, err = work.prepareLanding(change.Number, deadline); err != nil || code != exitLanded {
			return code, err
		}
	}
	return work.finish(ready, deadline)
}

// askPerson asks the person at the terminal to type the change's number.
func (work *session) askPerson(number int) error {
	fmt.Fprintf(work.deps.Stderr, "type the change's number to merge it (#%d): ", number)
	reader := bufio.NewReader(work.deps.Stdin)
	answer, err := reader.ReadString('\n')
	if !strings.HasSuffix(answer, "\n") {
		fmt.Fprintln(work.deps.Stderr)
	}
	if err != nil && answer == "" {
		return failf(exitRefused, "no answer was typed: not merging")
	}
	typed, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(answer), "#"))
	if err != nil || typed != number {
		return failf(exitRefused, "%q is not #%d: not merging", strings.TrimSpace(answer), number)
	}
	return nil
}

// selectChange is the open change from this branch: the one --change names, or the one open
// from the branch (found false when none is).
func (work *session) selectChange() (hosting.Change, bool, error) {
	if work.options.change == "" {
		return work.host.Change(work.ctx, hosting.ChangeRef{Head: work.branch})
	}
	number, err := work.changeNumber(work.options.change)
	if err != nil {
		return hosting.Change{}, false, err
	}
	change, found, err := work.host.Change(work.ctx, hosting.ChangeRef{Number: number})
	if err != nil {
		return hosting.Change{}, false, fmt.Errorf("change #%d: %w", number, err)
	}
	if !found || change.Number != number || change.State != "open" || change.Base == "" || change.Head != work.branch ||
		!strings.EqualFold(change.HeadRepository, work.repository) || !strings.EqualFold(change.BaseRepository, work.repository) {
		return hosting.Change{}, false, failf(exitInvalid, "#%d is not an open change request from %s in %s", number, work.branch, work.repository)
	}
	return change, true, nil
}

// changeNumber reads --change: a number, or a change request's URL in this repository.
func (work *session) changeNumber(text string) (int, error) {
	text = strings.TrimSpace(text)
	if number, err := strconv.Atoi(strings.TrimPrefix(text, "#")); err == nil {
		if number <= 0 {
			return 0, failf(exitInvalid, "--change takes a positive number or the change request's URL")
		}
		return number, nil
	}
	repository, number, err := gh.ParsePullURL(text)
	if err != nil {
		return 0, failf(exitInvalid, "--change: %v", err)
	}
	if !strings.EqualFold(repository, work.repository) {
		return 0, failf(exitInvalid, "--change names %s, but this checkout is in %s", repository, work.repository)
	}
	return number, nil
}

func (work *session) printPlan(open bool, ahead int, rules hosting.Rules, basePolicy config.Land, baseHasPolicy bool) {
	var plan []string
	switch {
	case ahead > 0:
		plan = append(plan, fmt.Sprintf("push %s (%d commits)", work.branch, ahead))
	case ahead < 0:
		plan = append(plan, fmt.Sprintf("push %s (new upstream)", work.branch))
	}
	if !open {
		plan = append(plan, "open a change request into "+work.base)
	}
	plan = append(plan, "judge the paths it touches against the human paths of the base's and the change's jackstay.json")
	required, gate := requiredChecks(rules, basePolicy)
	switch {
	case gate != "":
		plan = append(plan, "wait for the checks to settle and report them ("+gate+"): CI is not a gate here, so a person merges")
	default:
		names := make([]string, 0, len(required))
		for _, check := range required {
			names = append(names, check.String())
		}
		plan = append(plan, "wait for the required checks ("+strings.Join(names, ", ")+"); other checks are reported, never waited for")
	}
	if rules.UpToDateRead && rules.UpToDate {
		plan = append(plan, "update the branch on the host when it is behind "+work.base+", then judge it again")
	}
	merge := fmt.Sprintf("%s-merge at the head that was checked", methodOf(basePolicy))
	queued := rules.QueueRead && rules.Queue
	if queued {
		merge = "put it in the host's merge queue at the head that was checked"
	}
	switch {
	case !baseHasPolicy:
		plan = append(plan, "stop ready for a person: "+work.base+" has no landing policy yet")
	case work.options.prepare:
		plan = append(plan, "stop ready for a person (--prepare)")
	case queued && !work.options.confirm:
		plan = append(plan, "stop ready for a person: "+work.base+" requires the host's merge queue, which a person uses")
	case basePolicy.Autonomy != config.AutonomyMergeWhenGreen && !work.options.confirm:
		plan = append(plan, "stop ready for a person (`land.autonomy` is "+basePolicy.Autonomy+" on "+work.base+")")
	case work.options.confirm:
		plan = append(plan, "ask the person at this terminal to type the change's number, judge it again, then "+merge)
	default:
		plan = append(plan, merge+", unless a human path is touched (then stop ready for a person)")
	}
	plan = append(plan, "delete the remote branch (this checkout, its HEAD and its branches are left as they are)")
	work.say("plan:")
	for index, step := range plan {
		work.say("  %d. %s", index+1, step)
	}
}

// methodOf is the merge method the base's policy asks for.
func methodOf(policy config.Land) hosting.Method {
	if policy.Method == "" {
		return hosting.Squash
	}
	return hosting.Method(policy.Method)
}

// listFlag is a repeatable flag.
type listFlag []string

func (list *listFlag) String() string { return strings.Join(*list, ",") }

func (list *listFlag) Set(value string) error {
	*list = append(*list, value)
	return nil
}

func (work *session) sleepUntil(deadline time.Time, step time.Duration) (bool, error) {
	now := work.deps.Clock.Now()
	if !now.Before(deadline) {
		return false, nil
	}
	return true, work.deps.Clock.Sleep(work.ctx, min(step, deadline.Sub(now)))
}

func humanSeconds(span time.Duration) string {
	whole := int(span / time.Second)
	if whole < 60 {
		return fmt.Sprintf("%ds", whole)
	}
	return fmt.Sprintf("%dm%02ds", whole/60, whole%60)
}

func short(sha string, length int) string {
	if len(sha) > length {
		return sha[:length]
	}
	return sha
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
