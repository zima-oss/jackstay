//go:build integration

package installscript

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// The fake jst writes its arguments, one per line, to the file JST_RECORD names: what install.sh
// ran it with, or no file when it was never run. Run as `jst install --prefix P …`, it also puts
// itself at P/bin/jst, as an install does, so which jst a PATH runs can be asked.
// Asked `setup --help` (install.sh's probe), it records nothing and answers whether it has setup:
// not when JST_NO_SETUP is set, as a release before setup.
const fakeJst = "#!/bin/sh\n" +
	"if [ \"$1 $2\" = 'setup --help' ]; then [ -z \"$JST_NO_SETUP\" ]; exit; fi\n" +
	"printf '%s\\n' \"$@\" > \"$JST_RECORD\"\n" +
	"if [ \"$1 $2\" = 'install --prefix' ]; then mkdir -p \"$3/bin\" && cp \"$0\" \"$3/bin/jst\"; fi\n" +
	"if [ \"$1\" = setup ]; then echo 'fake machine setup started' >&2; fi\n"

// release is a fake GitHub releases page: /latest redirects to the newest tag, and
// /download/vX.Y.Z/NAME serves the files of that release. It also serves install.sh itself, at
// /install.sh, as the documented one-liner downloads it: whole, cut short, or not at all.
type release struct {
	server   *httptest.Server
	latest   string
	lock     sync.Mutex
	files    map[string][]byte // "v1.2.3/checksums.txt" -> contents, under lock
	requests atomic.Int64
	// How /install.sh is served: "whole"; "cut" (half, then the connection closes); "all-then-cut"
	// (every byte, then the connection closes before the length promised); or "" (404).
	script string
}

func newRelease(t *testing.T, latest string) *release {
	t.Helper()
	fake := &release{latest: latest, files: map[string][]byte{}}
	fake.server = httptest.NewServer(fake.handler(t))
	t.Cleanup(fake.server.Close)
	return fake
}

// newTLSRelease is newRelease served over https, with a certificate of its own; caBundle is the
// file that names it, for curl's CURL_CA_BUNDLE.
func newTLSRelease(t *testing.T, latest, script string) (fake *release, caBundle string) {
	t.Helper()
	fake = &release{latest: latest, files: map[string][]byte{}, script: script}
	fake.server = httptest.NewTLSServer(fake.handler(t))
	t.Cleanup(fake.server.Close)
	caBundle = filepath.Join(t.TempDir(), "ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fake.server.Certificate().Raw})
	if err := os.WriteFile(caBundle, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	return fake, caBundle
}

func (fake *release) handler(t *testing.T) http.Handler {
	t.Helper()
	script, err := os.ReadFile(installScript(t))
	if err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fake.requests.Add(1)
		switch {
		case request.URL.Path == "/install.sh" && fake.script == "whole":
			_, _ = writer.Write(script)
		case request.URL.Path == "/install.sh" && fake.script == "cut":
			// Promise the whole script, send half: the server closes the connection.
			writer.Header().Set("Content-Length", strconv.Itoa(len(script)))
			_, _ = writer.Write(script[:len(script)/2])
		case request.URL.Path == "/install.sh" && fake.script == "all-then-cut":
			writer.Header().Set("Content-Length", strconv.Itoa(len(script)+100))
			_, _ = writer.Write(script)
		case request.URL.Path == "/releases/latest":
			http.Redirect(writer, request, "/releases/tag/"+fake.latest, http.StatusFound)
		case strings.HasPrefix(request.URL.Path, "/releases/tag/"):
			fmt.Fprintln(writer, "a release page")
		case strings.HasPrefix(request.URL.Path, "/releases/download/"):
			contents, ok := fake.file(strings.TrimPrefix(request.URL.Path, "/releases/download/"))
			if !ok {
				http.NotFound(writer, request)
				return
			}
			_, _ = writer.Write(contents)
		default:
			http.NotFound(writer, request)
		}
	})
}

func (fake *release) file(name string) ([]byte, bool) {
	fake.lock.Lock()
	defer fake.lock.Unlock()
	contents, ok := fake.files[name]
	return contents, ok
}

func (fake *release) put(name string, contents []byte) {
	fake.lock.Lock()
	defer fake.lock.Unlock()
	fake.files[name] = contents
}

// publish adds a release with this machine's archive (holding the fake jst) and its checksums.txt;
// a non-empty wrongHash is written in checksums.txt in place of the archive's real one.
func (fake *release) publish(t *testing.T, tag, wrongHash string) string {
	t.Helper()
	return fake.publishArchive(t, tag, wrongHash, fakeJst)
}

// publishWith adds a release whose archive holds this jst.
func (fake *release) publishWith(t *testing.T, tag, jst string) {
	t.Helper()
	fake.publishArchive(t, tag, "", jst)
}

func (fake *release) publishArchive(t *testing.T, tag, wrongHash, jst string) string {
	t.Helper()
	name := fmt.Sprintf("jackstay_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
	archive := tarGz(t, map[string]string{"jst": jst, "LICENSE": "a licence\n"})
	sum := sha256.Sum256(archive)
	hash := hex.EncodeToString(sum[:])
	if wrongHash != "" {
		hash = wrongHash
	}
	other := strings.Repeat("0", 64) + "  jackstay_" + strings.TrimPrefix(tag, "v") + "_plan9_mips.tar.gz\n"
	fake.put(tag+"/"+name, archive)
	fake.put(tag+"/checksums.txt", []byte(other+hash+"  "+name+"\n"))
	return name
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(zipped)
	for name, contents := range files {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(contents)), Typeflag: tar.TypeReg}
		if name == "jst" {
			header.Mode = 0o755
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// run is one run of install.sh, by the shell named, against the fake release.
type run struct {
	exit   int
	stdout string // what a terminal shows, when the command is run under script
	stderr string
	record string // the fake jst's arguments; "" when it never ran
	prefix string
}

func installScript(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// shells are the POSIX shells install.sh is run by: sh, and dash where the machine has it (Debian's
// sh, stricter than bash's POSIX mode).
func shells(t *testing.T) []string {
	t.Helper()
	found := []string{"sh"}
	if _, err := exec.LookPath("dash"); err == nil {
		found = append(found, "dash")
	}
	return found
}

func runScript(t *testing.T, shell string, fake *release, path string, environment ...string) run {
	t.Helper()
	return runCommand(t, exec.Command(shell, installScript(t)), fake, path, environment...)
}

func runCommand(t *testing.T, command *exec.Cmd, fake *release, path string, environment ...string) run {
	t.Helper()
	home := t.TempDir()
	record := filepath.Join(t.TempDir(), "record")
	prefix := filepath.Join(home, "prefix")
	command.Env = append([]string{
		"PATH=" + path,
		"HOME=" + home,
		// jackstay's state under that home too (jst reads the account's home, not HOME), so a
		// test sees anything written there.
		"JACKSTAY_HOME=" + filepath.Join(home, ".jackstay"),
		"TMPDIR=" + t.TempDir(),
		"JACKSTAY_RELEASES=" + fake.server.URL + "/releases",
		"JACKSTAY_PREFIX=" + prefix,
		"JST_RECORD=" + record,
	}, environment...)
	var stdout, stderr bytes.Buffer
	if command.Stdout == nil {
		command.Stdout = &stdout
	}
	command.Stderr = &stderr
	err := command.Run()
	result := run{stdout: stdout.String(), stderr: stderr.String(), prefix: prefix}
	if exitError, ok := err.(*exec.ExitError); ok {
		result.exit = exitError.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if contents, readError := os.ReadFile(record); readError == nil {
		result.record = string(contents)
	}
	return result
}

func TestTheNewestReleaseIsCheckedAndInstalled(t *testing.T) {
	for _, shell := range shells(t) {
		t.Run(shell, func(t *testing.T) {
			fake := newRelease(t, "v1.2.4")
			fake.publish(t, "v1.2.3", "")
			fake.publish(t, "v1.2.4", "")
			result := runScript(t, shell, fake, os.Getenv("PATH"))
			if result.exit != 0 {
				t.Fatalf("exit %d: %s", result.exit, result.stderr)
			}
			arguments := strings.Split(strings.TrimSpace(result.record), "\n")
			if len(arguments) != 5 || arguments[0] != "install" || arguments[1] != "--prefix" ||
				arguments[2] != result.prefix || arguments[3] != "--binary" || filepath.Base(arguments[4]) != "jst" {
				t.Fatalf("jst ran as %q", arguments)
			}
			if _, err := os.Stat(arguments[4]); !os.IsNotExist(err) {
				t.Errorf("the download folder is left behind: %s", arguments[4])
			}
			for _, want := range []string{"jackstay 1.2.4 for", "jst doctor", "jst adopt --plan", "is not https"} {
				if !strings.Contains(result.stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, result.stderr)
				}
			}
		})
	}
}

// filesOutside are the files and folders under home that are not under prefix.
func filesOutside(t *testing.T, home, prefix string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == home {
			return nil
		}
		if path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator)) {
			return filepath.SkipDir
		}
		found = append(found, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// printedLine is the line install.sh printed for the user's profile.
func printedLine(t *testing.T, stderr string) string {
	t.Helper()
	_, after, found := strings.Cut(stderr, "is not on your PATH. Add this line to ")
	if !found {
		t.Fatalf("no PATH line:\n%s", stderr)
	}
	lines := strings.Split(after, "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[2], "  ") {
		t.Fatalf("no PATH line:\n%s", stderr)
	}
	return strings.TrimPrefix(lines[2], "  ")
}

func TestAMissingPathLineIsPrintedForTheUsersShell(t *testing.T) {
	for _, check := range []struct {
		shell   string
		profile string
	}{
		{"/bin/zsh", ".zshrc"},
		{"/bin/bash", ".bash_profile"},
		{"/usr/bin/fish", ""},
		{"/bin/ksh", ".profile"},
	} {
		t.Run(filepath.Base(check.shell), func(t *testing.T) {
			fake := newRelease(t, "v1.2.4")
			fake.publish(t, "v1.2.4", "")
			result := runScript(t, "sh", fake, pathWithout(t), "SHELL="+check.shell)
			if result.exit != 0 {
				t.Fatalf("exit %d: %s", result.exit, result.stderr)
			}
			home := filepath.Dir(result.prefix)
			bin, err := filepath.EvalSymlinks(filepath.Join(result.prefix, "bin"))
			if err != nil {
				t.Fatal(err)
			}
			wants := []string{"jst doctor", "jst adopt --plan"}
			if check.shell == "/usr/bin/fish" {
				// Not a POSIX shell: the folder is named, and no line is printed for it.
				wants = append(wants, bin+" is not on your PATH. Put this folder first on PATH in fish's configuration", "\n  "+bin+"\n")
				if strings.Contains(result.stderr, "export PATH") || strings.Contains(result.stderr, "fish_add_path") {
					t.Errorf("a line is printed for fish:\n%s", result.stderr)
				}
			} else {
				profile := check.profile
				if check.shell == "/bin/bash" && runtime.GOOS != "darwin" {
					profile = ".bashrc"
				}
				wants = append(wants, bin+" is not on your PATH. Add this line to "+filepath.Join(home, profile)+":")
				if line, want := printedLine(t, result.stderr), "export PATH='"+bin+"':\"$PATH\""; line != want {
					t.Errorf("the line is %q, want %q", line, want)
				}
			}
			for _, wanted := range wants {
				if !strings.Contains(result.stderr, wanted) {
					t.Errorf("stderr lacks %q:\n%s", wanted, result.stderr)
				}
			}
			if outside := filesOutside(t, home, result.prefix); len(outside) != 0 {
				t.Errorf("the script wrote outside the prefix: %v", outside)
			}
		})
	}
}

// TestThePathLineNamesThePrefixFromAnyFolder: a relative prefix is made absolute before the
// install, and a prefix with spaces, quotes or a dollar sign is one literal word in the line, which
// a shell then puts first on PATH as it is.
func TestThePathLineNamesThePrefixFromAnyFolder(t *testing.T) {
	for _, given := range []string{"tools", "with space", "it's $HOME `x`"} {
		t.Run(given, func(t *testing.T) {
			fake := newRelease(t, "v1.2.4")
			fake.publish(t, "v1.2.4", "")
			folder, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command("sh", installScript(t))
			command.Dir = folder
			result := runCommand(t, command, fake, pathWithout(t), "JACKSTAY_PREFIX="+given, "SHELL=/bin/zsh")
			if result.exit != 0 {
				t.Fatalf("exit %d: %s", result.exit, result.stderr)
			}
			prefix := filepath.Join(folder, given)
			if arguments := strings.Split(result.record, "\n"); len(arguments) < 3 || arguments[2] != prefix {
				t.Errorf("jst install ran with the prefix %q, want %q", arguments, prefix)
			}
			line := printedLine(t, result.stderr)
			out, err := exec.Command("sh", "-c", line+` && printf '%s' "$PATH"`).Output()
			if err != nil {
				t.Fatalf("%s: %v", line, err)
			}
			if first, _, _ := strings.Cut(string(out), ":"); first != filepath.Join(prefix, "bin") {
				t.Errorf("%s puts %q first on PATH, want %q", line, first, filepath.Join(prefix, "bin"))
			}
		})
	}
}

// TestARealInstallWritesNothingOutsideThePrefix: the release's own jst, built here, installs, and
// the home holds nothing else afterwards: no journal, no state.
func TestARealInstallWritesNothingOutsideThePrefix(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "jst")
	build := exec.Command("go", "build", "-o", binary,
		"-ldflags", "-X github.com/zima-oss/jackstay/internal/version.Commit=abc1234", "../../cmd/jst")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	contents, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	fake := newRelease(t, "v1.2.4")
	fake.publishWith(t, "v1.2.4", string(contents))
	result := runScript(t, "sh", fake, os.Getenv("PATH"))
	if result.exit != 0 {
		t.Fatalf("exit %d: %s", result.exit, result.stderr)
	}
	if _, err := os.Stat(filepath.Join(result.prefix, "bin", "jst")); err != nil {
		t.Fatalf("not installed: %v\n%s", err, result.stderr)
	}
	if outside := filesOutside(t, filepath.Dir(result.prefix), result.prefix); len(outside) != 0 {
		t.Errorf("the install wrote outside the prefix: %v", outside)
	}
}

// TestAnotherJstFirstOnPathIsNamed: PATH runs another install's jst before this one; the script
// names it and prints the line that puts this install first.
func TestAnotherJstFirstOnPathIsNamed(t *testing.T) {
	fake := newRelease(t, "v1.2.4")
	fake.publish(t, "v1.2.4", "")
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "jst"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(t.TempDir(), "prefix")
	path := other + string(os.PathListSeparator) + pathWithout(t) + string(os.PathListSeparator) + filepath.Join(prefix, "bin")
	result := runScript(t, "sh", fake, path, "JACKSTAY_PREFIX="+prefix, "SHELL=/bin/zsh")
	if result.exit != 0 {
		t.Fatalf("exit %d: %s", result.exit, result.stderr)
	}
	bin, err := filepath.EvalSymlinks(filepath.Join(prefix, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(other, "jst") + " comes first on your PATH, not this install: put " + bin + " before it."
	if !strings.Contains(result.stderr, want) {
		t.Errorf("stderr lacks %q:\n%s", want, result.stderr)
	}
}

func TestNoPathLineWhenTheInstallIsOnPath(t *testing.T) {
	for _, shell := range shells(t) {
		t.Run(shell, func(t *testing.T) {
			fake := newRelease(t, "v1.2.4")
			fake.publish(t, "v1.2.4", "")
			prefix := filepath.Join(t.TempDir(), "on path")
			for _, bin := range []string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "bin") + "/"} {
				path := pathWithout(t) + string(os.PathListSeparator) + bin
				result := runScript(t, shell, fake, path, "JACKSTAY_PREFIX="+prefix, "SHELL=/bin/zsh")
				if result.exit != 0 {
					t.Fatalf("exit %d: %s", result.exit, result.stderr)
				}
				if strings.Contains(result.stderr, "not on your PATH") || strings.Contains(result.stderr, "export PATH") {
					t.Errorf("PATH holds %s, yet a PATH line is printed:\n%s", bin, result.stderr)
				}
				if !strings.Contains(result.stderr, "jst doctor") || !strings.Contains(result.stderr, "jst adopt --plan") {
					t.Errorf("no next steps:\n%s", result.stderr)
				}
			}
		})
	}
}

func TestInstalledSetupRunsOnlyWithATerminal(t *testing.T) {
	terminal, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script is unavailable")
	}
	fake := newRelease(t, "v1.2.4")
	fake.publish(t, "v1.2.4", "")
	paths := pathWithout(t)
	direct := runScript(t, "sh", fake, paths)
	if direct.exit != 0 || !strings.HasPrefix(direct.record, "install\n") || strings.Contains(direct.record, "setup") {
		t.Fatalf("nonterminal install ran setup: exit %d, arguments %q", direct.exit, direct.record)
	}
	if !strings.Contains(direct.stderr, "no terminal here, so machine setup does not start: once jst is on your PATH") {
		t.Fatalf("a nonterminal install did not say setup was left:\n%s", direct.stderr)
	}
	installer := installScript(t)
	var command *exec.Cmd
	if runtime.GOOS == "darwin" {
		command = exec.Command(terminal, "-q", "/dev/null", "sh", installer)
	} else {
		quoted := "'" + strings.ReplaceAll(installer, "'", "'\\''") + "'"
		command = exec.Command(terminal, "-q", "-e", "-c", "sh "+quoted, "/dev/null")
	}
	interactive := runCommand(t, command, fake, paths)
	if want := "setup\n--prefix\n" + interactive.prefix + "\n"; interactive.exit != 0 || interactive.record != want {
		t.Fatalf("terminal install: exit %d, arguments %q, want %q\n%s",
			interactive.exit, interactive.record, want, interactive.stderr)
	}
	output := interactive.stdout + interactive.stderr
	notice, started := strings.Index(output, "a terminal is here, so machine setup starts now"), strings.Index(output, "fake machine setup started")
	if notice < 0 || started < 0 || notice > started || !strings.Contains(output, "asks who merges a project's changes and which reviewer runs") {
		t.Fatalf("a terminal install did not say setup starts before starting it:\n%s", output)
	}

	// A release without setup, with a terminal or without, is told apart and runs nothing more.
	older := runCommand(t, exec.Command(command.Args[0], command.Args[1:]...), fake, paths, "JST_NO_SETUP=1")
	plain := runScript(t, "sh", fake, paths, "JST_NO_SETUP=1")
	for name, result := range map[string]run{"terminal": older, "no terminal": plain} {
		said := result.stdout + result.stderr
		if result.exit != 0 || !strings.HasPrefix(result.record, "install\n") ||
			!strings.Contains(said, "this jst has no machine setup") || strings.Contains(said, "run `jst setup`") {
			t.Fatalf("%s, a release without setup: exit %d, arguments %q\n%s", name, result.exit, result.record, said)
		}
	}
}

func TestAVersionAskedForIsTheOneInstalled(t *testing.T) {
	for _, version := range []string{"1.2.3", "v1.2.3"} {
		fake := newRelease(t, "v1.2.4")
		fake.publish(t, "v1.2.3", "")
		fake.publish(t, "v1.2.4", "")
		result := runScript(t, "sh", fake, os.Getenv("PATH"), "JACKSTAY_VERSION="+version)
		if result.exit != 0 || !strings.Contains(result.stderr, "jackstay 1.2.3 for") {
			t.Fatalf("JACKSTAY_VERSION=%s: exit %d: %s", version, result.exit, result.stderr)
		}
	}
}

func TestAnArchiveThatDoesNotMatchItsChecksumIsNeverRun(t *testing.T) {
	for _, shell := range shells(t) {
		t.Run(shell, func(t *testing.T) {
			fake := newRelease(t, "v1.2.3")
			fake.publish(t, "v1.2.3", strings.Repeat("ab", 32))
			result := runScript(t, shell, fake, os.Getenv("PATH"))
			if result.exit != 1 || result.record != "" {
				t.Fatalf("exit %d, jst ran: %q\n%s", result.exit, result.record, result.stderr)
			}
			for _, want := range []string{"does not match its line in checksums.txt", "nothing was installed"} {
				if !strings.Contains(result.stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, result.stderr)
				}
			}
		})
	}
}

func TestAnIndependentDigestIsCheckedBeforeTheArchiveRuns(t *testing.T) {
	fake := newRelease(t, "v1.2.3")
	archive := fake.publish(t, "v1.2.3", "")
	contents, _ := fake.file("v1.2.3/" + archive)
	digest := sha256.Sum256(contents)
	for _, check := range []struct {
		name   string
		pin    string
		result int
		want   string
	}{
		{"valid", hex.EncodeToString(digest[:]), 0, ""},
		{"changed", strings.Repeat("0", 64), 1, "does not match the independently supplied"},
		{"malformed", "not-a-digest", 1, "must be a 64-digit lowercase"},
	} {
		t.Run(check.name, func(t *testing.T) {
			result := runScript(t, "sh", fake, os.Getenv("PATH"), "JACKSTAY_SHA256="+check.pin)
			if result.exit != check.result || (check.result != 0 && result.record != "") || !strings.Contains(result.stderr, check.want) {
				t.Fatalf("exit %d, jst ran: %q\n%s", result.exit, result.record, result.stderr)
			}
		})
	}
}

func TestStrictAttestationChecksIdentityAndNeverDowngrades(t *testing.T) {
	fake := newRelease(t, "v1.2.3")
	archive := fake.publish(t, "v1.2.3", "")
	contents, _ := fake.file("v1.2.3/" + archive)
	digest := sha256.Sum256(contents)
	folder := t.TempDir()
	gh := filepath.Join(folder, "gh")
	// A fake verifier checks the identity flags. It never contacts GitHub or accepts an
	// arbitrary workflow, repository or tag.
	script := `#!/bin/sh
test "$1 $2" = 'attestation verify' || exit 1
test "$4 $5" = '--repo zima-oss/jackstay' || exit 1
test "$6 $7" = '--signer-workflow zima-oss/jackstay/.github/workflows/release.yml' || exit 1
test "$8 $9" = '--source-ref refs/tags/v1.2.3' || exit 1
exit "${FAKE_GH_EXIT:-0}"
`
	if err := os.WriteFile(gh, []byte(script), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	realTar, err := exec.LookPath("tar")
	if err != nil {
		t.Fatal(err)
	}
	tar := "#!/bin/sh\nprintf x > \"$TAR_RECORD\"\nexec " + strconv.Quote(realTar) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(folder, "tar"), []byte(tar), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	path := folder + string(os.PathListSeparator) + pathWithout(t)
	for _, check := range []struct {
		name, verifierExit, pin string
		wantExit                int
	}{
		{"verified", "0", "", 0},
		{"refused", "1", "", 1},
		{"pin_does_not_bypass_verifier", "1", hex.EncodeToString(digest[:]), 1},
	} {
		t.Run(check.name, func(t *testing.T) {
			tarRecord := filepath.Join(t.TempDir(), "tar-record")
			result := runScript(t, "sh", fake, path, "JACKSTAY_PROVENANCE=attestation", "JACKSTAY_SHA256="+check.pin, "FAKE_GH_EXIT="+check.verifierExit, "TAR_RECORD="+tarRecord)
			if result.exit != check.wantExit || (check.wantExit != 0 && result.record != "") {
				t.Fatalf("exit %d, jst ran: %q\n%s", result.exit, result.record, result.stderr)
			}
			_, tarError := os.Stat(tarRecord)
			if check.wantExit == 0 && tarError != nil || check.wantExit != 0 && !os.IsNotExist(tarError) {
				t.Fatalf("tar ran unexpectedly or was skipped: %v\n%s", tarError, result.stderr)
			}
		})
	}
	result := runScript(t, "sh", fake, pathWithout(t), "JACKSTAY_PROVENANCE=attestation")
	if result.exit != 1 || result.record != "" || !strings.Contains(result.stderr, "gh is needed") {
		t.Fatalf("missing verifier: exit %d, jst ran: %q\n%s", result.exit, result.record, result.stderr)
	}
}

func TestAnArchiveWithoutALineInChecksumsIsNeverRun(t *testing.T) {
	fake := newRelease(t, "v1.2.3")
	name := fake.publish(t, "v1.2.3", "")
	// The archive's line names it only as part of a longer name.
	fake.put("v1.2.3/checksums.txt", []byte(strings.Repeat("0", 64)+"  x"+name+"\n"))
	result := runScript(t, "sh", fake, os.Getenv("PATH"))
	if result.exit != 1 || result.record != "" || !strings.Contains(result.stderr, "checksums.txt has no line for "+name) {
		t.Fatalf("exit %d, jst ran: %q\n%s", result.exit, result.record, result.stderr)
	}
}

func TestAReleaseWithoutThisPlatformsArchiveIsReported(t *testing.T) {
	fake := newRelease(t, "v1.2.3")
	result := runScript(t, "sh", fake, os.Getenv("PATH"))
	if result.exit != 1 || !strings.Contains(result.stderr, "could not download") {
		t.Fatalf("exit %d\n%s", result.exit, result.stderr)
	}
}

// withFakeUname is PATH with a uname first that answers -s and -m as given.
func withFakeUname(t *testing.T, system, machine string) string {
	t.Helper()
	folder := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\ncase $1 in -s) echo %s ;; -m) echo %s ;; esac\n", system, machine)
	if err := os.WriteFile(filepath.Join(folder, "uname"), []byte(script), 0o755); err != nil { //nolint:gosec // a test's fake command must be executable
		t.Fatal(err)
	}
	return folder + string(os.PathListSeparator) + os.Getenv("PATH")
}

func TestAnUnsupportedPlatformDownloadsNothing(t *testing.T) {
	for _, platform := range []struct{ system, machine, want string }{
		{"FreeBSD", "amd64", "this system is not supported: FreeBSD"},
		{"Linux", "ppc64le", "this processor is not supported: ppc64le"},
	} {
		for _, shell := range shells(t) {
			t.Run(platform.system+"-"+platform.machine+"-"+shell, func(t *testing.T) {
				fake := newRelease(t, "v1.2.3")
				fake.publish(t, "v1.2.3", "")
				result := runScript(t, shell, fake, withFakeUname(t, platform.system, platform.machine))
				if result.exit != 1 || !strings.Contains(result.stderr, platform.want) {
					t.Fatalf("exit %d\n%s", result.exit, result.stderr)
				}
				if count := fake.requests.Load(); count != 0 {
					t.Errorf("%d requests made", count)
				}
			})
		}
	}
}

// pathWithout is a PATH of one folder holding links to the commands install.sh uses, save those
// named.
func pathWithout(t *testing.T, missing ...string) string {
	t.Helper()
	folder := t.TempDir()
	for _, name := range []string{"uname", "curl", "tar", "gzip", "sha256sum", "shasum", "cut", "awk", "mktemp", "rm", "mkdir", "cat", "sysctl", "perl", "sed", "basename", "dirname", "cp", "sh"} {
		if strings.Contains(" "+strings.Join(missing, " ")+" ", " "+name+" ") {
			continue
		}
		if found, err := exec.LookPath(name); err == nil {
			if err := os.Symlink(found, filepath.Join(folder, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return folder
}

func TestAMissingToolIsNamedBeforeAnyDownload(t *testing.T) {
	for _, missing := range []struct {
		commands []string
		want     string
	}{
		{[]string{"curl"}, "curl is needed"},
		{[]string{"tar"}, "tar is needed"},
		{[]string{"sha256sum", "shasum"}, "sha256sum or shasum is needed"},
	} {
		t.Run(missing.commands[0], func(t *testing.T) {
			fake := newRelease(t, "v1.2.3")
			fake.publish(t, "v1.2.3", "")
			result := runScript(t, "sh", fake, pathWithout(t, missing.commands...))
			if result.exit != 1 || !strings.Contains(result.stderr, missing.want) {
				t.Fatalf("exit %d\n%s", result.exit, result.stderr)
			}
			if count := fake.requests.Load(); count != 0 {
				t.Errorf("%d requests made", count)
			}
		})
	}
}

// documentedAddress is where the documented one-liner downloads install.sh from.
const documentedAddress = "https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh"

// oneLiner is the README's install line.
func oneLiner(t *testing.T) string {
	t.Helper()
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(readme)) {
		if strings.HasPrefix(line, `script=$(curl `) {
			return strings.TrimSuffix(line, "\n")
		}
	}
	t.Fatal("README.md has no line starting with script=$(curl")
	return ""
}

func TestTheOneLinerIsTheSameWhereverItIsDocumented(t *testing.T) {
	line := oneLiner(t)
	if !strings.Contains(line, " "+documentedAddress+")") {
		t.Fatalf("the README's one-liner does not download %s: %s", documentedAddress, line)
	}
	for file, want := range map[string]string{
		"docs/guide/install.md":         "\n" + line + "\n",
		"docs/guide/getting-started.md": "\n" + line + "\n",
		"site/content/_index.md":        "\n" + line + "\n",
		"install.sh":                    "\n#   " + line + "\n",
		// jst doctor's fixes give the download half, with the prefix handed to sh.
		"internal/doctor/doctor.go": "\"" + strings.TrimSuffix(strings.TrimPrefix(line, "script=$("), `) && sh -c "$script"`) + "\"",
	} {
		contents, err := os.ReadFile(filepath.Join("../..", file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), want) {
			t.Errorf("%s does not carry the README's one-liner: %s", file, line)
		}
	}
}

func TestTheOneLinerInstallsOnlyAWholeScript(t *testing.T) {
	// A download that fails runs nothing, however much of the script arrived, and the line fails.
	for _, served := range []struct {
		script  string
		install bool
		want    string
	}{
		{"whole", true, "jst adopt --plan"},
		{"", false, "curl: (22)"},
		{"cut", false, "curl: (18)"},
		{"all-then-cut", false, "curl: (18)"},
	} {
		t.Run("served-"+served.script, func(t *testing.T) {
			fake, caBundle := newTLSRelease(t, "v1.2.3", served.script)
			fake.publish(t, "v1.2.3", "")
			line := strings.Replace(oneLiner(t), documentedAddress, fake.server.URL+"/install.sh", 1)
			result := runCommand(t, exec.Command("sh", "-c", line), fake, os.Getenv("PATH"), "CURL_CA_BUNDLE="+caBundle)
			if (result.exit == 0) != served.install || !strings.Contains(result.stderr, served.want) {
				t.Fatalf("exit %d\n%s", result.exit, result.stderr)
			}
			if ran := result.record != ""; ran != served.install {
				t.Errorf("jst ran: %q", result.record)
			}
			if strings.Contains(result.stderr, "is not https") {
				t.Errorf("the release was not fetched over https:\n%s", result.stderr)
			}
		})
	}
}
