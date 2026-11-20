package supervise

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Detach starts argv in dir as the leader of a session of its own, with no terminal, standard
// input from /dev/null and both outputs appended to logPath (made 0600). It does not supervise the
// command: argv is expected to supervise itself (`jst bg`). The caller's process keeps no hold on
// it beyond reaping it, so the command outlives the caller. done is closed once it has ended and
// been reaped, which happens only while the caller still runs.
func Detach(argv []string, dir string, env []string, logPath string) (int, <-chan struct{}, error) {
	if len(argv) == 0 {
		return 0, nil, errors.New("no command to start")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return 0, nil, err
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) // #nosec G304 -- the caller's own log
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = output.Close() }()
	process, err := os.StartProcess(path, argv, &os.ProcAttr{Dir: dir, Env: env,
		Files: []*os.File{input, output, output}, Sys: &syscall.SysProcAttr{Setsid: true}})
	if err != nil {
		return 0, nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = process.Wait()
	}()
	return process.Pid, done, nil
}
