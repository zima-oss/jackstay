// Package codex runs one Codex review (docs/design/review.md sections 2 and 3.4): read only and
// never asking, the prompt on standard input and then closed, a built environment, a deadline
// that stops Codex's whole process group, the last message to a file, and the events read for
// the first answer and for Codex's own login failures. It knows nothing of rounds or locks.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/review/access"
	"github.com/zima-oss/jackstay/internal/review/runner"
	"github.com/zima-oss/jackstay/internal/strictjson"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// Outcomes of a run (docs/design/review.md section 3.5); review adds unparsable and inconsistent.
const (
	OutcomeDone     = runner.OutcomeDone
	OutcomeTimeout  = runner.OutcomeTimeout
	OutcomeFailed   = runner.OutcomeFailed
	OutcomeLogin    = runner.OutcomeLogin
	OutcomeNoAnswer = runner.OutcomeNoAnswer
	OutcomeRefused  = runner.OutcomeRefused
)

// OutputCap is the most of standard output or error kept per run.
const OutputCap = 64 << 20

// Grace is how long a stopped process group has between TERM and KILL (section 2), how long
// what a process left in its group may run on before it is killed, and how long its output may
// still be read after its end.
const Grace = 5 * time.Second

// GroupSpec is how review runs every child process, git and Codex alike: a process group of its
// own under internal/supervise, stopped as a group (TERM, KILL after Grace) and read under a bound.
func GroupSpec(argv []string) supervise.Spec {
	return supervise.Spec{Argv: argv, Mode: supervise.Group, Grace: Grace, KillWait: Grace, ReadBound: Grace,
		Who: "jst review"}
}

// startHold is the longest a run keeps the start lock waiting for its first answer.
const startHold = 120 * time.Second

// Named permission profiles have only been verified for the pinned reviewer with this CLI or newer.
const minimumPinnedVersion = "0.169.0"

// Request is the shared request used by the Codex runner.
type Request = runner.Request

// Result is the shared result returned by the Codex runner.
type Result = runner.Result

// Argv is the command line of a run (section 1): jackstay's own words, then the isolation flags.
func Argv(request Request, answerPath string) []string {
	argv := []string{request.Command, "-a", "never", "exec", "--json",
		"-C", request.Tree, "-o", answerPath, "--output-schema", request.SchemaPath}
	if !request.PinnedSource {
		argv = append(argv, "--sandbox", "read-only")
	}
	if request.Model != "" {
		argv = append(argv, "-m", request.Model)
	}
	if request.Effort != "" {
		argv = append(argv, "-c", fmt.Sprintf("model_reasoning_effort=%q", request.Effort))
	}
	argv = append(argv, request.Isolation...)
	for _, name := range sortedNames(request.MCPServers) {
		server := request.MCPServers[name]
		argv = append(argv, "-c", fmt.Sprintf("mcp_servers.%s={command=%s,args=%s}", name, strconv.Quote(server.Command), tomlList(server.Args)))
	}
	if request.PinnedSource {
		// Permission profiles and --sandbox do not compose. The reviewer's access profile (W-090):
		// it reads everything but the account's secrets, writes only its own temporary folder, and
		// has the network.
		if !slices.Contains(argv, "--skip-git-repo-check") {
			argv = append(argv, "--skip-git-repo-check")
		}
		argv = append(argv, "--strict-config", "-c", `default_permissions="jackstay_review"`,
			"-c", Filesystem(request, homedir.AccountHome()),
			"-c", "permissions.jackstay_review.network.enabled=true",
			"-c", "project_doc_max_bytes=0",
			"-c", `shell_environment_policy.inherit="all"`)
	}
	return append(argv, "-")
}

// Temporary is the reviewer's own folder for temporary files: the only place it may write.
func Temporary(request Request) string { return filepath.Join(request.Folder, "tmp") }

// Filesystem is the permission profile's filesystem table: read everything; never the account's
// secrets or a `.env` file of the reviewed tree or the live checkout; write only Temporary.
func Filesystem(request Request, home string) string {
	entries := []string{`":root"="read"`}
	denied := access.SecretPaths(home)
	denied = append(denied, access.SecretFiles([]string{request.Tree, request.RepositoryRoot}, 8, 512)...)
	for _, path := range denied {
		if resolved, err := canonicalPath(path); err == nil {
			path = resolved
		}
		entries = append(entries, strconv.Quote(path)+`="none"`)
	}
	if temporary, err := canonicalPath(Temporary(request)); err == nil {
		entries = append(entries, strconv.Quote(temporary)+`="write"`)
	}
	return "permissions.jackstay_review.filesystem={" + strings.Join(entries, ",") + "}"
}

func sortedNames(servers map[string]access.MCPServer) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func tomlList(words []string) string {
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		quoted = append(quoted, strconv.Quote(word))
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(absolute)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			return "", err
		}
		missing = append(missing, filepath.Base(absolute))
		absolute = parent
	}
}

// Home is Codex's folder: CODEX_HOME, else ~/.codex of the account.
func Home(environ []string) string {
	for _, pair := range environ {
		if value, ok := strings.CutPrefix(pair, "CODEX_HOME="); ok && value != "" {
			return value
		}
	}
	return filepath.Join(homedir.AccountHome(), ".codex")
}

// LoginMarker is the file that says Codex's login failed (section 2, item 3).
func LoginMarker() (string, error) { return homedir.SharedPath("reviews", "login-failed") }

// CheckEnvironment refuses a run that must not start (section 2): Codex's login cannot be saved
// here (a sandbox), there is no login, or the login failed and was not renewed since. "" when
// the run may start.
func CheckEnvironment(codexHome, marker string) string {
	auth := filepath.Join(codexHome, "auth.json")
	authInfo, err := os.Stat(auth)
	if errors.Is(err, os.ErrNotExist) {
		return "Codex has no login (" + auth + "): run `codex login`"
	}
	file, err := os.OpenFile(auth, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return "Codex cannot save its login here (" + auth + " is not writable): run `jst review` outside the sandbox"
		}
		return "Codex's login cannot be opened: " + err.Error()
	}
	_ = file.Close()
	if markerInfo, err := os.Stat(marker); err == nil && authInfo != nil && markerInfo.ModTime().After(authInfo.ModTime()) {
		words, _ := os.ReadFile(marker)
		return "Codex's login failed earlier and was not renewed since (" + strings.TrimSpace(string(words)) +
			"): run `codex login`"
	}
	return ""
}

// Probe asks the program its version (10 s); an error when it cannot be run.
func Probe(ctx context.Context, command string) (string, error) {
	path, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("codex is not installed (%s): %w", command, err)
	}
	probeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	probe := GroupSpec([]string{path, "--version"})
	probe.Env, probe.Context, probe.StdoutTo, probe.StderrTo = access.Environment(os.Environ(), access.Codex, ""), probeContext, &stdout, io.Discard
	outcome, err := supervise.Run(nil, probe)
	switch {
	case err != nil:
	case probeContext.Err() != nil:
		err = probeContext.Err()
	case outcome.Status() != 0:
		err = fmt.Errorf("exit status %d", outcome.Status())
	}
	if err != nil {
		return "", fmt.Errorf("codex did not answer `--version` (%s): %w", path, err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	version := strings.TrimSpace(lines[len(lines)-1])
	if !supportsPinnedSource(version) {
		return "", fmt.Errorf("pinned reviews require Codex %s or newer with permission profiles (found %q)", minimumPinnedVersion, version)
	}
	return version, nil
}

func supportsPinnedSource(version string) bool {
	parts := strings.Fields(version)
	if len(parts) != 2 || parts[0] != "codex-cli" {
		return false
	}
	numbers := strings.Split(parts[1], ".")
	if len(numbers) != 3 {
		return false
	}
	var parsed [3]int
	for index, part := range numbers {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return false
		}
		parsed[index] = number
	}
	return parsed[0] > 0 || parsed[1] > 169 || parsed[1] == 169 && parsed[2] >= 0
}

// Run runs one review. It always returns a result; a failure is an outcome, not an error.
func Run(ctx context.Context, request Request) Result {
	answerPath := filepath.Join(request.Folder, "answer.json")
	argv := Argv(request, answerPath)
	result := Result{Argv: argv, AnswerPath: answerPath, Started: time.Now(), Exit: -1}
	answered := onceFunc(request.Answered)
	defer answered()
	finish := func(outcome, reason string) Result {
		result.Ended, result.Outcome, result.Reason = time.Now(), outcome, reason
		return result
	}
	if err := os.MkdirAll(request.Folder, 0o700); err != nil {
		return finish(OutcomeFailed, err.Error())
	}
	path, err := exec.LookPath(request.Command)
	if err != nil {
		return finish(OutcomeRefused, "Codex is not installed: "+err.Error())
	}
	events, err := os.OpenFile(filepath.Join(request.Folder, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return finish(OutcomeFailed, err.Error())
	}
	defer func() { _ = events.Close() }()
	errorsFile, err := os.OpenFile(filepath.Join(request.Folder, "stderr.txt"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return finish(OutcomeFailed, err.Error())
	}
	defer func() { _ = errorsFile.Close() }()

	environ := request.Environ
	if environ == nil {
		environ = os.Environ()
	}
	temporary := Temporary(request)
	if err := os.MkdirAll(temporary, 0o700); err != nil {
		return finish(OutcomeFailed, err.Error())
	}
	spec := GroupSpec(append([]string{path}, argv[1:]...))
	spec.Dir, spec.Env, spec.Input = request.Tree, access.Environment(environ, access.Codex, temporary), request.Prompt
	spec.Trace = ctx
	stderrTail := &tailBuffer{limit: 64 << 10}
	spec.StderrTo = io.MultiWriter(&cappedWriter{target: errorsFile, room: OutputCap}, stderrTail)
	eventsRead, eventsWrite := io.Pipe()
	spec.StdoutTo = eventsWrite
	var watcher eventWatcher
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		watcher.read(eventsRead, &cappedWriter{target: events, room: OutputCap}, answered)
	}()
	var timing runTiming
	spec.Control = func(control *supervise.Control) { timing.watch(ctx, control, request.Timeout, answered) }
	outcome, err := supervise.Run(nil, spec)
	stoppedBy := timing.over(outcome.Ended)
	// The events were read to their end (or the bound after the stop is over): the watcher reaches
	// the end of its pipe.
	_ = eventsWrite.Close()
	<-readDone
	answered()
	if err != nil {
		return finish(OutcomeFailed, "Codex did not start: "+err.Error())
	}
	if outcome.Signal == 0 {
		result.Exit = outcome.Code
	}
	switch {
	case stoppedBy == OutcomeTimeout:
		return finish(OutcomeTimeout, fmt.Sprintf("past the deadline of %s; Codex's process group was stopped", request.Timeout))
	case stoppedBy != "":
		return finish(OutcomeFailed, "jackstay was asked to stop; Codex's process group was stopped")
	}
	answer, statErr := os.Stat(answerPath)
	hasAnswer := statErr == nil && answer.Size() > 0
	failure := watcher.failure()
	if hasAnswer && result.Exit == 0 {
		// An error event Codex recovered from (a reconnect) does not undo an answer.
		return finish(OutcomeDone, "")
	}
	ownWords := failure + "\n" + codexLogLines(stderrTail.String())
	if IsLoginFailure(ownWords) {
		return finish(OutcomeLogin, "Codex's login failed: "+firstLine(strings.TrimSpace(ownWords)))
	}
	if result.Exit != 0 || failure != "" {
		reason := fmt.Sprintf("Codex exited %d", result.Exit)
		if failure != "" {
			reason += ": " + failure
		}
		return finish(OutcomeFailed, reason)
	}
	return finish(OutcomeNoAnswer, "Codex exited 0 and wrote no last message")
}

// runTiming stops a run at its deadline or the context's end, and lets the start lock go after
// startHold, from a goroutine of its own while the run lasts.
type runTiming struct {
	mutex     sync.Mutex
	stoppedBy string
	stoppedAt time.Time
	runOver   chan struct{}
	watched   chan struct{}
}

// watch starts the goroutine; called once, when Codex has started.
func (timing *runTiming) watch(ctx context.Context, control *supervise.Control, timeout time.Duration, answered func()) {
	timing.runOver, timing.watched = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(timing.watched)
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		hold := time.NewTimer(startHold)
		defer hold.Stop()
		for {
			select {
			case <-hold.C:
				answered()
				continue
			case <-deadline.C:
				timing.stop(control, OutcomeTimeout)
			case <-ctx.Done():
				timing.stop(control, OutcomeFailed)
			case <-timing.runOver:
			}
			return
		}
	}()
}

func (timing *runTiming) stop(control *supervise.Control, outcome string) {
	timing.mutex.Lock()
	timing.stoppedBy, timing.stoppedAt = outcome, time.Now()
	timing.mutex.Unlock()
	control.Stop()
}

// over ends the goroutine and says what stopped the run ("" when Codex ended by itself: a stop
// made after its end, which was learnt at ended, did not end it).
func (timing *runTiming) over(ended time.Time) string {
	if timing.runOver == nil {
		return ""
	}
	close(timing.runOver)
	<-timing.watched
	timing.mutex.Lock()
	defer timing.mutex.Unlock()
	if timing.stoppedBy != "" && !ended.IsZero() && ended.Before(timing.stoppedAt) {
		return ""
	}
	return timing.stoppedBy
}

// RecordLoginFailure writes the marker that stops later runs until `codex login` (section 2, item 3).
func RecordLoginFailure(marker, reason string) {
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)+" "+reason+"\n"), 0o600)
}

// answeringEvents are the events that need the model to have answered (section 2, item 2).
var answeringEvents = map[string]bool{"item.started": true, "item.completed": true, "turn.completed": true,
	"turn.failed": true, "error": true}

// event is the part of a Codex event jackstay reads; items are never read for failures.
type event struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type eventWatcher struct {
	mutex    sync.Mutex
	failures []string
}

func (watcher *eventWatcher) read(stdout io.Reader, copyTo io.Writer, answered func()) {
	reader := bufio.NewReaderSize(stdout, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = copyTo.Write(line)
			watcher.look(line, answered)
		}
		if err != nil {
			return
		}
	}
}

func (watcher *eventWatcher) look(line []byte, answered func()) {
	var seen event
	if strictjson.UnmarshalOpen(bytes.TrimSpace(line), &seen) != nil {
		return
	}
	if answeringEvents[seen.Type] {
		answered()
	}
	if seen.Type != "error" && seen.Type != "turn.failed" {
		return
	}
	message := seen.Message
	if seen.Error != nil && seen.Error.Message != "" {
		message = seen.Error.Message
	}
	if message == "" {
		message = seen.Type
	}
	watcher.mutex.Lock()
	watcher.failures = append(watcher.failures, message)
	watcher.mutex.Unlock()
}

func (watcher *eventWatcher) failure() string {
	watcher.mutex.Lock()
	defer watcher.mutex.Unlock()
	return strings.Join(watcher.failures, "; ")
}

// loginWords are Codex's own words for a failed login (section 2, item 3).
var loginWords = regexp.MustCompile(`(?i)\b401\b|unauthori[sz]ed|refresh[ _-]token|not logged in|log ?in again|sign in again|token (has )?expired`)

// IsLoginFailure says whether Codex's own words (its error events and log lines) name a failed login.
func IsLoginFailure(ownWords string) bool { return loginWords.MatchString(ownWords) }

// codexLogLines are the ERROR and WARN lines of Codex's own log on standard error.
func codexLogLines(stderr string) string {
	var kept []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, " ERROR ") || strings.Contains(line, " WARN ") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func onceFunc(call func()) func() {
	if call == nil {
		return func() {}
	}
	return sync.OnceFunc(call)
}

// cappedWriter writes until its room is used, then drops the rest (a run never fails for it).
type cappedWriter struct {
	target io.Writer
	room   int64
}

func (writer *cappedWriter) Write(data []byte) (int, error) {
	if writer.room > 0 {
		part := data
		if int64(len(part)) > writer.room {
			part = part[:writer.room]
		}
		written, _ := writer.target.Write(part)
		writer.room -= int64(written)
	}
	return len(data), nil
}

// tailBuffer keeps the last bytes written to it.
type tailBuffer struct {
	mutex sync.Mutex
	data  []byte
	limit int
}

func (buffer *tailBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	buffer.data = append(buffer.data, data...)
	if len(buffer.data) > buffer.limit {
		buffer.data = buffer.data[len(buffer.data)-buffer.limit:]
	}
	return len(data), nil
}

func (buffer *tailBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return string(buffer.data)
}
