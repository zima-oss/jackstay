package proctree

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/procs"
)

// cwdSources are what FoldersInside reads: the table (before and after lsof) and lsof's answer.
type cwdSources struct {
	read func() (map[int]procs.Process, error)
	// list is `lsof -a -d cwd -F pn`'s output; exited says it ended with status 0.
	list func(ctx context.Context) (output []byte, exited bool, err error)
	uid  int
}

// View is what FoldersInside saw.
type View struct {
	// Inside are the processes whose working folder is the folder or inside it, by id.
	Inside map[int]string
	// Complete says lsof could see every process of this account: it named the working folder of
	// each one that ran throughout its walk.
	Complete bool
	// Unseen are the processes of this account that ran throughout lsof's walk and whose folder it
	// did not name, by id; empty when lsof itself could not report.
	Unseen []int
}

// FoldersInside are the processes whose working folder is folder or inside it, by id, and whether
// that view is complete.
//
// What the view must prove is that lsof can see this account's processes: inside the agent sandbox
// it lists only the caller's own and still ends with 0. So it is complete only when lsof named the
// folder of every process of this account that ran throughout its walk: in the table read before
// it and in the one read after, as the same process (its start unchanged). A process that started
// while lsof walked the table is counted when lsof named it, but one it did not reach proves
// nothing about what lsof can see: it stands where a process started a moment after the look
// stands, and processes start all the time (a session's `sleep 1` in a wait loop, `git`; section 5.2
// of the implementation spec gives the measurement). A session that works in a folder has processes
// that live there longer than a look (its agent, its shell, `jst bg`), and those hold it; `jst tidy
// --fix` looks again just before it removes anything.
func FoldersInside(ctx context.Context, folder string) (View, error) {
	return foldersInside(ctx, folder, cwdSources{read: procs.ReadTable, list: runLsof, uid: os.Getuid()})
}

func runLsof(ctx context.Context) ([]byte, bool, error) {
	output, err := exec.CommandContext(ctx, "lsof", "-a", "-d", "cwd", "-F", "pn").Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		// lsof ends with 1 when it could not report on something: what it said is kept, the
		// view is not complete.
		return output, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return output, true, nil
}

func foldersInside(ctx context.Context, folder string, sources cwdSources) (View, error) {
	root, err := filepath.Abs(folder)
	if err != nil {
		return View{}, err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	before, err := sources.read()
	if err != nil {
		return View{}, err
	}
	output, exited, err := sources.list(ctx)
	if err != nil {
		return View{}, err
	}
	after, err := sources.read()
	if err != nil {
		return View{}, err
	}
	folders := parseLsof(output)
	view := View{Inside: map[int]string{}}
	for pid, place := range folders {
		row, running := after[pid]
		if earlier, ran := before[pid]; place == "" || !running || row.Zombie ||
			(ran && !earlier.Zombie && earlier.StartMicros != row.StartMicros) {
			// No folder; ended; or its id given again while lsof ran, so which of the two lsof
			// saw cannot be told.
			continue
		}
		if place == root || strings.HasPrefix(place, strings.TrimSuffix(root, "/")+"/") {
			view.Inside[pid] = place
		}
	}
	for pid, row := range after {
		earlier, ran := before[pid]
		if pid <= 0 || row.UID != sources.uid || row.Zombie || !ran || earlier.Zombie || earlier.StartMicros != row.StartMicros {
			// Another account's, a zombie, or one that started while lsof walked (its id new, or
			// given again).
			continue
		}
		if folders[pid] == "" {
			view.Unseen = append(view.Unseen, pid)
		}
	}
	sort.Ints(view.Unseen)
	view.Complete = exited && len(view.Unseen) == 0
	if !exited {
		view.Unseen = nil
	}
	return view, nil
}

// parseLsof reads lsof's field output: a `p` line starts a process, its `n` line is the folder;
// a process listed without a folder is kept with "".
func parseLsof(output []byte) map[int]string {
	folders := map[int]string{}
	pid := 0
	lines := bufio.NewScanner(bytes.NewReader(output))
	lines.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for lines.Scan() {
		line := lines.Text()
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			number, err := strconv.Atoi(line[1:])
			if err != nil {
				pid = 0
				continue
			}
			pid = number
			if _, seen := folders[pid]; !seen {
				folders[pid] = "" // listed, its folder not (yet) named
			}
		case 'n':
			if pid > 0 {
				folders[pid] = line[1:]
			}
		}
	}
	return folders
}
