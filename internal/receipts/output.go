package receipts

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// Kept outputs (docs/design/implementation.md section 9.1).
const (
	// KeepOutputs is how many outputs each project keeps, the newest by run id.
	KeepOutputs = 30
	// keepUnfinished is how long the output of a run with no end in the record is kept.
	keepUnfinished = 7 * 24 * time.Hour
	// NotableMax is how many lines of what went wrong are shown.
	NotableMax = 40
)

// notable are lines that say what went wrong.
var notable = regexp.MustCompile(`(?:^|\s)error:|\bFAIL(?:ED|URE)?\b|\bfailed\b|✘|\b[1-9][0-9]* failures?\b|\bfatal\b|Assertion failed|panic:`)

// RunID is a run's id: the time it started in nanoseconds (hex), and the process.
func RunID(now time.Time, pid int) string { return fmt.Sprintf("%x-%d", now.UnixNano(), pid) }

// OutputsFolder is `<jackstay home>/<project>/outputs`.
func OutputsFolder(project string) (string, error) { return homedir.ProjectPath(project, "outputs") }

// Kept is a run's output being kept.
type Kept struct {
	Path   string
	file   *os.File
	broken bool
}

// KeepOutput opens the file a run's output is kept in and removes the project's older outputs
// beyond KeepOutputs — by run id, and only those whose run has an end in the record (one still
// going is kept, unless it has had no end for a week). An error when it cannot be made: the run
// goes on without.
func KeepOutput(project, runID string, logPaths []string, now time.Time) (*Kept, error) {
	folder, err := OutputsFolder(project)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return nil, err
	}
	own := runID + ".log"
	place := filepath.Join(folder, own)
	file, err := os.OpenFile(place, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	pruneOutputs(folder, own, logPaths, now)
	return &Kept{Path: place, file: file}, nil
}

func pruneOutputs(folder, own string, logPaths []string, now time.Time) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return
	}
	var logs []string
	for _, entry := range entries {
		if name := entry.Name(); strings.HasSuffix(name, ".log") && name != own {
			logs = append(logs, name)
		}
	}
	sort.Strings(logs)
	surplus := logs[:max(0, len(logs)-KeepOutputs+1)]
	if len(surplus) == 0 {
		return
	}
	ended := map[string]bool{}
	for _, record := range ReadRecords(logPaths) {
		if record.Text("event") == EventRun {
			ended[record.Text("run_id")] = true
		}
	}
	for _, name := range surplus {
		place := filepath.Join(folder, name)
		status, err := os.Stat(place)
		if err != nil {
			continue
		}
		if ended[strings.TrimSuffix(name, ".log")] || now.Sub(status.ModTime()) > keepUnfinished {
			_ = os.Remove(place)
		}
	}
}

// Line keeps one line; a failed write is remembered (the kept output is then not whole).
func (kept *Kept) Line(text string) {
	if kept == nil || kept.file == nil {
		return
	}
	if _, err := kept.file.WriteString(text + "\n"); err != nil {
		kept.broken = true
	}
}

// Close ends the kept output; it says whether the output is whole.
func (kept *Kept) Close() bool {
	if kept == nil {
		return false
	}
	if kept.file != nil {
		if err := kept.file.Close(); err != nil {
			kept.broken = true
		}
		kept.file = nil
	}
	return !kept.broken
}

// NotableLines are the lines of a kept output that say what went wrong (a veto's line too), each
// once, at most NotableMax, and how many more there were.
func NotableLines(place string, veto *regexp.Regexp) ([]string, int) {
	file, err := os.Open(place)
	if err != nil {
		return nil, 0
	}
	defer func() { _ = file.Close() }()
	var found []string
	seen := map[string]bool{}
	more := 0
	reader := bufio.NewReader(file)
	for {
		raw, readErr := reader.ReadString('\n')
		text := strings.TrimRight(supervise.Clean(raw), " \t\r\n")
		if text != "" && !seen[text] && (notable.MatchString(text) || (veto != nil && veto.MatchString(text))) {
			seen[text] = true
			if len(found) < NotableMax {
				found = append(found, cut(text, 300))
			} else {
				more++
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return nil, 0
			}
			return found, more
		}
	}
}

// WriteNotable says the notable lines as `jst test` and `jst test last` do.
func WriteNotable(out io.Writer, prefix string, lines []string, more int) {
	if len(lines) == 0 {
		return
	}
	first := ""
	if more > 0 {
		first = fmt.Sprintf(", the first %d", len(lines))
	}
	fmt.Fprintf(out, "%swhat the output says went wrong (%d lines%s):\n", prefix, len(lines)+more, first)
	for _, text := range lines {
		fmt.Fprintf(out, "%s| %s\n", prefix, text)
	}
}

// tee passes the command's output on to jackstay's own streams, keeps it and feeds the judge.
type tee struct {
	stdout, stderr io.Writer
	kept           *Kept
	judge          supervise.Sink
}

func (sink *tee) Line(stream supervise.Stream, text string) error {
	target := sink.stdout
	if stream == supervise.Stderr {
		target = sink.stderr
	}
	_, _ = io.WriteString(target, text+"\n") // a closed reader does not stop the run (A7)
	sink.kept.Line(text)
	return sink.judge.Line(stream, text)
}
