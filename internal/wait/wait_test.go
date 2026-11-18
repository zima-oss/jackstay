package wait

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
)

// fakeWorld moves only when the wait pauses: each pause may change files or end processes.
type fakeWorld struct {
	t       *testing.T
	now     time.Time
	pauses  []time.Duration
	events  map[int]func() // run during the n-th pause, from 1
	running map[int]bool
	// onRunning runs each time the wait asks whether a process runs.
	onRunning func()
	readied   bool
}

func newFake(t *testing.T) *fakeWorld {
	return &fakeWorld{t: t, now: time.Unix(10_000, 0), events: map[int]func(){}, running: map[int]bool{}}
}

func (w *fakeWorld) Now() time.Time { return w.now }

func (w *fakeWorld) Sleep(pause time.Duration) {
	if !w.readied {
		w.t.Fatal("paused before it was ready")
	}
	w.now = w.now.Add(pause)
	w.pauses = append(w.pauses, pause)
	if event, ok := w.events[len(w.pauses)]; ok {
		event()
	}
	if len(w.pauses) > 10_000 {
		w.t.Fatal("the wait never ends")
	}
}

func (w *fakeWorld) Running(pid int) bool {
	if w.onRunning != nil {
		w.onRunning()
	}
	return w.running[pid]
}

func (w *fakeWorld) Ready() { w.readied = true }

func (w *fakeWorld) waited() time.Duration {
	var total time.Duration
	for _, pause := range w.pauses {
		total += pause
	}
	return total
}

// wait runs `jst wait` with the arguments in the fake world.
func (w *fakeWorld) wait(args ...string) (int, string) {
	w.t.Helper()
	var stderr bytes.Buffer
	asked, err := parse(args, &stderr)
	if err != nil {
		w.t.Fatalf("%v: %v %s", args, err, stderr.String())
	}
	var stdout bytes.Buffer
	code := run(asked, w, &stdout)
	return code, stdout.String()
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func logIn(t *testing.T) string {
	return filepath.Join(t.TempDir(), "out.log")
}

func TestALineThatComesLaterIsFoundAndPrinted(t *testing.T) {
	log := logIn(t)
	write(t, log, "building\n")
	world := newFake(t)
	world.events[3] = func() { appendTo(t, log, "Executed 12 tests, with 0 failures\n") }
	code, out := world.wait("--line", `Executed \d+ tests`, "--in", log, "--timeout", "120")
	if code != 0 || !strings.HasPrefix(out, "Executed 12 tests") || !strings.Contains(out, "  | building\n") {
		t.Fatalf("%d %q", code, out)
	}
	if !strings.Contains(out, "jst wait: the line came after 1 s") {
		t.Fatalf("elapsed: %q", out)
	}
}

func TestALineAlreadyThereCountsUnlessNew(t *testing.T) {
	log := logIn(t)
	write(t, log, "ready\n")
	if code, out := newFake(t).wait("--line", "ready", "--in", log, "--timeout", "1"); code != 0 {
		t.Fatalf("%d %q", code, out)
	}
	world := newFake(t)
	code, out := world.wait("--line", "ready", "--in", log, "--timeout", "0.5", "--new")
	if code != 3 || world.waited() != 500*time.Millisecond {
		t.Fatalf("%d %q after %v", code, out, world.waited())
	}
}

func TestFailLines(t *testing.T) {
	cases := []struct {
		name    string
		content string
		args    []string
		code    int
		says    string
	}{
		{"a fail line ends the wait with 1", "x.swift:3: error: no such thing\n",
			[]string{"--line", "BUILD SUCCEEDED", "--fail", "error:"}, 1, "a line matched --fail after 0 s: x.swift:3: error: no such thing"},
		{"a fail line beside the wanted one wins", "DONE\nERROR: it broke\n",
			[]string{"--line", "DONE", "--fail", "ERROR"}, 1, "ERROR: it broke"},
		{"a fail line not yet ended still beats the wanted one", "DONE\nERROR",
			[]string{"--line", "DONE", "--fail", "ERROR"}, 1, "--fail after 0 s: ERROR"},
		{"a wanted line not yet ended waits for its end", "DONE",
			[]string{"--line", "DONE", "--timeout", "1"}, 3, "no line matched 'DONE' after 1 s"},
		{"an empty --fail is none", "DONE\nERROR\n", []string{"--line", "DONE", "--fail", ""}, 0, "DONE\n"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			log := logIn(t)
			write(t, log, test.content)
			code, out := newFake(t).wait(append(test.args, "--in", log)...)
			if code != test.code || !strings.Contains(out, test.says) {
				t.Fatalf("%d %q", code, out)
			}
		})
	}
}

func TestTheDeadlineIs3WithWhatWasSeen(t *testing.T) {
	log := logIn(t)
	write(t, log, "still going\n")
	world := newFake(t)
	code, out := world.wait("--line", "done", "--in", log, "--timeout", "7")
	if code != 3 || !strings.Contains(out, "no line matched 'done' after 7 s") || !strings.Contains(out, "  | still going") {
		t.Fatalf("%d %q", code, out)
	}
	if world.waited() != 7*time.Second {
		t.Fatalf("waited %v", world.waited())
	}
	want := []time.Duration{200 * time.Millisecond, 300 * time.Millisecond, 450 * time.Millisecond, 675 * time.Millisecond}
	for index, pause := range want {
		if world.pauses[index] != pause {
			t.Fatalf("pauses %v", world.pauses)
		}
	}
}

func TestPausesGrowToFiveSeconds(t *testing.T) {
	world := newFake(t)
	world.running[42] = true
	code, out := world.wait("--pid", "42")
	if code != 3 || !strings.Contains(out, "process 42 still runs after 540 s") || world.waited() != DefaultTimeout {
		t.Fatalf("%d %q %v", code, out, world.waited())
	}
	longest := time.Duration(0)
	for _, pause := range world.pauses {
		longest = max(longest, pause)
	}
	if longest != 5*time.Second {
		t.Fatalf("longest pause %v", longest)
	}
}

func TestAProcessThatEnds(t *testing.T) {
	world := newFake(t)
	world.running[42] = true
	world.events[2] = func() { world.running[42] = false }
	code, out := world.wait("--pid", "42", "--timeout", "10")
	if code != 0 || !strings.Contains(out, "process 42 has ended (0 s)") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestAProcessWhoseLastWordsAreAFailure(t *testing.T) {
	log := logIn(t)
	write(t, log, "")
	world := newFake(t)
	// Its last words come between the look at the file and the look at the process.
	world.onRunning = func() { write(t, log, "ERROR at the end") }
	code, out := world.wait("--pid", "42", "--in", log, "--fail", "ERROR", "--timeout", "5")
	if code != 1 || !strings.Contains(out, "jst wait: a line matched --fail: ERROR at the end") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestWhilePID(t *testing.T) {
	t.Run("ending first is 1", func(t *testing.T) {
		log := logIn(t)
		write(t, log, "half\n")
		world := newFake(t)
		world.running[7] = true
		world.events[1] = func() { world.running[7] = false }
		code, out := world.wait("--line", "all done", "--in", log, "--while-pid", "7", "--timeout", "10")
		if code != 1 || !strings.Contains(out, "process 7 ended before the line it was waited for") {
			t.Fatalf("%d %q", code, out)
		}
	})
	t.Run("its last line, not ended, still counts", func(t *testing.T) {
		log := logIn(t)
		world := newFake(t)
		world.running[7] = true
		world.events[1] = func() { write(t, log, "all done"); world.running[7] = false }
		code, out := world.wait("--line", "all done", "--in", log, "--while-pid", "7", "--timeout", "10")
		if code != 0 || !strings.HasPrefix(out, "all done\n") {
			t.Fatalf("%d %q", code, out)
		}
	})
	t.Run("a file made as the writer ended still counts", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "result.json")
		world := newFake(t)
		world.running[7] = true
		world.events[1] = func() { world.running[7] = false }
		// The file comes between the look for it and the look at the writer.
		world.onRunning = func() { write(t, target, "{}") }
		code, out := world.wait("--file", target, "--while-pid", "7", "--timeout", "10")
		if code != 0 || !strings.Contains(out, "is there") {
			t.Fatalf("%d %q", code, out)
		}
	})
	t.Run("a file never made is 1", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "result.json")
		world := newFake(t)
		world.running[7] = true
		world.events[1] = func() { world.running[7] = false }
		code, out := world.wait("--file", target, "--while-pid", "7", "--timeout", "10")
		if code != 1 || !strings.Contains(out, "process 7 ended before the file it was waited for") || len(world.pauses) != 1 {
			t.Fatalf("%d %q", code, out)
		}
	})
	t.Run("quiet is not cut short", func(t *testing.T) {
		log := logIn(t)
		write(t, log, "1\n")
		world := newFake(t)
		code, out := world.wait("--quiet", "2", "--in", log, "--while-pid", "7", "--timeout", "10")
		if code != 0 || world.waited() < 2*time.Second {
			t.Fatalf("%d %q after %v", code, out, world.waited())
		}
	})
}

func TestFileNonemptyAndGone(t *testing.T) {
	folder := t.TempDir()
	target := filepath.Join(folder, "result.json")
	world := newFake(t)
	world.events[1] = func() { write(t, target, "") }
	world.events[3] = func() { write(t, target, "{}\n") }
	code, out := world.wait("--file", target, "--nonempty", "--timeout", "10", "--tail", "0")
	if code != 0 || len(world.pauses) != 3 || out != "jst wait: "+target+" is there (1 s)\n" {
		t.Fatalf("%d %q after %d pauses", code, out, len(world.pauses))
	}
	if code, out := newFake(t).wait("--file", target, "--timeout", "0"); code != 0 {
		t.Fatalf("%d %q", code, out)
	}
	if code, out := newFake(t).wait("--gone", target, "--timeout", "0.3"); code != 3 || !strings.Contains(out, "is still there after 0 s") {
		t.Fatalf("%d %q", code, out)
	}
	link := filepath.Join(folder, "link")
	if err := os.Symlink(filepath.Join(folder, "nowhere"), link); err != nil {
		t.Fatal(err)
	}
	if code, _ := newFake(t).wait("--gone", link, "--timeout", "0"); code != 3 {
		t.Fatal("a dangling link is not gone")
	}
	gone := newFake(t)
	gone.events[2] = func() { _ = os.Remove(target) }
	if code, out := gone.wait("--gone", target, "--timeout", "1"); code != 0 || !strings.Contains(out, "is gone") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestQuiet(t *testing.T) {
	log := logIn(t)
	write(t, log, "1\n2\n3\n")
	world := newFake(t)
	world.events[2] = func() { appendTo(t, log, "4\n") }
	code, out := world.wait("--quiet", "1", "--in", log, "--timeout", "120")
	if code != 0 || !strings.Contains(out, "has not grown for 1 s") || !strings.Contains(out, "  | 4") {
		t.Fatalf("%d %q", code, out)
	}
	// It grew during the second pause (at 0.5 s): quiet from then on for 1 s.
	if world.waited() < 1500*time.Millisecond {
		t.Fatalf("ended after %v", world.waited())
	}
	missing := newFake(t)
	code, out = missing.wait("--quiet", "1", "--in", log+".none", "--timeout", "3")
	if code != 3 || !strings.Contains(out, "still grows after 3 s") {
		t.Fatalf("a file never seen is not quiet: %d %q", code, out)
	}
}

func TestFilesReadAgainFromTheirStart(t *testing.T) {
	cases := []struct {
		name   string
		before string
		args   []string
		change func(t *testing.T, log string)
		code   int
	}{
		{"cut short", strings.Repeat("a long first run\n", 50), []string{"--line", "second run", "--new"},
			func(t *testing.T, log string) { write(t, log, "second run: ok\n") }, 0},
		{"written over in place without growing", "old\n", []string{"--line", "never", "--fail", "BAD"},
			func(t *testing.T, log string) {
				write(t, log, "BAD\n")
				later := time.Unix(2_000_000_000, 0)
				if err := os.Chtimes(log, later, later); err != nil {
					t.Fatal(err)
				}
			}, 1},
		{"replaced by another of the same size", "old run\n", []string{"--line", "new run", "--new"},
			func(t *testing.T, log string) {
				next := log + ".next"
				write(t, next, "new run\n")
				if err := os.Rename(next, log); err != nil {
					t.Fatal(err)
				}
			}, 0},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			log := logIn(t)
			write(t, log, test.before)
			world := newFake(t)
			world.events[1] = func() { test.change(t, log) }
			code, out := world.wait(append(test.args, "--in", log, "--timeout", "5")...)
			if code != test.code {
				t.Fatalf("%d %q", code, out)
			}
		})
	}
}

func TestTail(t *testing.T) {
	log := logIn(t)
	write(t, log, "1\n2\n3\n4\n")
	code, out := newFake(t).wait("--line", "4", "--in", log, "--tail", "2")
	if code != 0 || out != "4\njst wait: the line came after 0 s\n  | 3\n  | 4\n" {
		t.Fatalf("%d %q", code, out)
	}
	write(t, log, "")
	if lines := lastLines(log, 5); lines != nil {
		t.Fatalf("an empty file's lines: %q", lines)
	}
	write(t, log, strings.Repeat("x", tailWindow)+"\nend\n")
	if lines := lastLines(log, 5); len(lines) != 2 || lines[1] != "end" {
		t.Fatalf("lines of a long file: %d", len(lines))
	}
}

func TestBadRequestsAre2(t *testing.T) {
	cases := [][]string{
		{}, {"--pid", "1", "--file", "x"}, {"--line", "x"}, {"--quiet", "1"}, {"--file", "x", "--fail", "e"},
		{"--line", "(", "--in", "x"}, {"--file", "x", "--timeout", "-1"}, {"--nonsense"},
		{"--file", "x", "--timeout", "nan"}, {"--file", "x", "--timeout", "inf"}, {"--file", "x", "--timeout", "soon"},
		{"--quiet", "-1", "--in", "x"}, {"--quiet", "0", "--in", "x"}, {"--quiet", "NaN", "--in", "x"},
		{"--pid", "0"}, {"--pid", "-3"}, {"--file", "x", "--while-pid", "0"}, {"--file", "x", "extra"},
		{"--line", "x", "--in", "y", "--fail", "("},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := command(args, nil, &stdout, &stderr); code != 2 {
				t.Fatalf("%d %q %q", code, stdout.String(), stderr.String())
			}
			if stderr.Len() == 0 {
				t.Fatal("nothing said")
			}
		})
	}
}

func TestHelpIs0(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := command([]string{"--help"}, nil, &stdout, &stderr); code != 0 || !strings.Contains(stderr.String(), "usage: jst wait") {
		t.Fatalf("%d %q", code, stderr.String())
	}
}

func TestRegistered(t *testing.T) {
	if _, _, found := cli.Find([]string{"wait"}); !found {
		t.Fatal("jst wait is not in the table")
	}
}

func TestTheReadyFileIsCreatedAndTheCommandRuns(t *testing.T) {
	folder := t.TempDir()
	ready := filepath.Join(folder, "ready")
	t.Setenv(readyFileVariable, ready)
	target := filepath.Join(folder, "there")
	write(t, target, "x")
	var stdout, stderr bytes.Buffer
	if code := command([]string{"--file", target, "--timeout", "0"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("%d %q %q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatal("no ready file")
	}
}
