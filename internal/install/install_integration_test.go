//go:build integration

package install

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/layout"
	"github.com/zima-oss/jackstay/internal/prove"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/testrepo"
	"github.com/zima-oss/jackstay/internal/version"
)

// binaries are this checkout's jackstay built twice, as two commits of one version.
var binaries = map[string]string{}

func TestMain(m *testing.M) {
	folder, err := os.MkdirTemp("", "jackstay-install-test.")
	if err != nil {
		panic(err)
	}
	// An empty jackstay home of the suite's own: no test reads this machine's signing identity unless
	// it sets one (signingHome), so the gate never depends on the keychain.
	home := filepath.Join(folder, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		panic(err)
	}
	if err := os.Setenv("JACKSTAY_HOME", home); err != nil {
		panic(err)
	}
	here, _ := os.Getwd()
	checkout := FindCheckout(here)
	for _, commit := range []string{"1111111", "2222222", "v" + version.Version} {
		out := filepath.Join(folder, commit, "jst")
		build := exec.Command("go", "build", "-trimpath", "-ldflags", "-X "+Module+"/internal/version.Commit="+commit, "-o", out, "./cmd/jst")
		build.Dir, build.Env = checkout, append(os.Environ(), "CGO_ENABLED=0")
		if output, err := build.CombinedOutput(); err != nil {
			panic(string(output))
		}
		binaries[commit] = out
	}
	code := m.Run()
	os.RemoveAll(folder)
	os.Exit(code)
}

func identityOf(commit string) string { return version.Version + " (" + commit + ")" }

func options(prefix, binary string) (Options, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	return Options{Prefix: prefix, Binary: binary, Stdout: &stdout, Stderr: &stderr}, &stdout, &stderr
}

// launchersRun runs every launcher for real: each names the live version, and the guard refuses.
func launchersRun(t *testing.T, prefix string) string {
	t.Helper()
	state, _ := layout.Read(prefix)
	if !state.AllFixed() || state.Live == nil {
		t.Fatalf("not installed whole: %+v", state)
	}
	for _, name := range []string{layout.Program, layout.LinkName} {
		said, err := exec.Command(filepath.Join(layout.Bin(prefix), name), "version").Output()
		if err != nil {
			t.Fatalf("%s version: %v", name, err)
		}
		if text := strings.TrimPrefix(strings.TrimSpace(string(said)), "jackstay "); text != state.Live.Version {
			t.Fatalf("%s says %q, lib/jackstay names %q", name, text, state.Live.Version)
		}
	}
	home := t.TempDir()
	if err := prove.Guard(context.Background(), filepath.Join(layout.Bin(prefix), layout.Guard), append(os.Environ(), "JACKSTAY_HOME="+home)); err != nil {
		t.Fatal(err)
	}
	return state.Live.Version
}

// An update stopped after its one rename, or a fresh install stopped after any of its steps: every
// launcher runs one whole version, and running install again finishes it.
func TestInstallStoppedAfterEachStep(t *testing.T) {
	fresh := []string{"activate", "launcher jst", "launcher jackstay", "launcher jst-guard"}
	for stopAt := 1; stopAt <= len(fresh); stopAt++ {
		t.Run(fresh[stopAt-1], func(t *testing.T) {
			prefix := filepath.Join(t.TempDir(), "prefix")
			run, _, stderr := options(prefix, binaries["1111111"])
			var seen []string
			run.AfterStep = func(step string) error {
				if seen = append(seen, step); len(seen) == stopAt {
					return layout.ErrStopped
				}
				return nil
			}
			if code := Install(run); code != 1 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if strings.Join(seen, ",") != strings.Join(fresh[:stopAt], ",") {
				t.Fatalf("steps %v", seen)
			}
			again, stdout, stderr := options(prefix, binaries["1111111"])
			if code := Install(again); code != 0 {
				t.Fatalf("rerun: exit %d: %s", code, stderr)
			}
			if got := launchersRun(t, prefix); got != identityOf("1111111") {
				t.Fatalf("after the rerun: %s (%s)", got, stdout)
			}
		})
	}
	prefix := filepath.Join(t.TempDir(), "prefix")
	first, _, _ := options(prefix, binaries["1111111"])
	if code := Install(first); code != 0 {
		t.Fatal(code)
	}
	update, _, stderr := options(prefix, binaries["2222222"])
	update.AfterStep = func(step string) error {
		if step != "activate" {
			t.Fatalf("an update took the step %s", step)
		}
		return layout.ErrStopped
	}
	if code := Install(update); code != 1 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := launchersRun(t, prefix); got != identityOf("2222222") {
		t.Fatalf("after the update's rename: %s", got)
	}
}

// Each install keeps the live folder and the one live before it; older ones are removed.
func TestUpdatesKeepTheVersionBefore(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "prefix")
	first, _, stderr := options(prefix, binaries["1111111"])
	if code := Install(first); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	second, stdout, stderr := options(prefix, binaries["2222222"])
	if code := Install(second); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := launchersRun(t, prefix); got != identityOf("2222222") {
		t.Fatalf("live %s", got)
	}
	if !strings.Contains(stdout.String(), "the version before: jackstay-"+version.Version+"-1111111") {
		t.Fatalf("output %s", stdout)
	}
	again, stdout, _ := options(prefix, binaries["2222222"])
	if code := Install(again); code != 0 || !strings.Contains(stdout.String(), "already installed") {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	state, _ := layout.Read(prefix)
	if len(state.Folders) != 2 {
		t.Fatalf("%d folders", len(state.Folders))
	}
	third, stdout, stderr := options(prefix, binaries["1111111"])
	if code := Install(third); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	state, _ = layout.Read(prefix)
	if len(state.Folders) != 2 || state.Before() == nil || !strings.Contains(state.Before().Version, "2222222") ||
		!strings.Contains(stdout.String(), "removed ") {
		t.Fatalf("%+v\n%s", state.Folders, stdout)
	}
}

func TestFailedProofChangesNothing(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "prefix")
	good, _, _ := options(prefix, binaries["1111111"])
	if code := Install(good); code != 0 {
		t.Fatal(code)
	}
	fake := filepath.Join(t.TempDir(), "jst")
	script := "#!/bin/sh\ncase \"$1\" in version) echo 'jackstay " + identityOf("3333333") + "';; esac\necho '{}'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := layout.Read(prefix)
	bad, _, stderr := options(prefix, fake)
	if code := Install(bad); code != 2 || !strings.Contains(stderr.String(), "did not refuse") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	after, _ := layout.Read(prefix)
	if len(after.Folders) != len(before.Folders) || after.Live.Name != before.Live.Name {
		t.Fatalf("changed: %+v", after)
	}
}

func TestLockHeldRefuses(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "prefix")
	os.MkdirAll(layout.Lib(prefix), 0o755)
	held, err := layout.Lock(prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	run, _, stderr := options(prefix, binaries["1111111"])
	// The lock is the process's own here; flock locks per open file, so a second open is refused.
	if code := Install(run); code != 2 || !strings.Contains(stderr.String(), layout.ErrBusy.Error()) {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestUninstall(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "prefix")
	run, _, stderr := options(prefix, binaries["1111111"])
	if code := Install(run); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	foreign := filepath.Join(layout.Lib(prefix), "jackstay-someone-else")
	os.MkdirAll(foreign, 0o755)
	var stdout bytes.Buffer
	if code := RunUninstall([]string{"--prefix", prefix}, nil, &stdout, &stdout); code != 0 {
		t.Fatalf("exit %d: %s", code, stdout.String())
	}
	entries, _ := os.ReadDir(layout.Lib(prefix))
	if len(entries) != 2 || entries[0].Name() != layout.LockName || entries[1].Name() != "jackstay-someone-else" {
		t.Fatalf("left in lib: %v", entries)
	}
	for _, name := range layout.Launchers {
		if _, err := os.Lstat(filepath.Join(layout.Bin(prefix), name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left", name)
		}
	}
}

// BuildChanged reads only what goes into the binary: docs, the justfile and tests moving on need no
// install; a code change does, committed or not (W-020; found when a project adopted 0.5.1).
func TestBuildChangedReadsOnlyTheBuildInputs(t *testing.T) {
	repo := testrepo.New(t, nil)
	installed := repo.Commit("installed", map[string]string{"go.mod": "module example.com/m\n", "internal/a/a.go": "package a\n",
		"justfile": "gate:\n", "docs/x.md": "x\n"})
	changed := func(want bool, what string) {
		t.Helper()
		got, err := BuildChanged(context.Background(), repo.Root, installed[:7])
		if err != nil || got != want {
			t.Fatalf("%s: changed %v (%v), want %v", what, got, err, want)
		}
	}
	changed(false, "nothing")
	repo.Commit("docs", map[string]string{"docs/x.md": "y\n", "justfile": "gate: check\n", "bin/tool": "#!/bin/sh\n"})
	changed(false, "docs, the justfile and bin/")
	repo.Commit("a test", map[string]string{"internal/a/a_test.go": "package a\n"})
	changed(false, "a test file")
	repo.Write(map[string]string{"internal/a/a.go": "package a\n\nconst X = 1\n"})
	changed(true, "uncommitted code")
	repo.Git("commit", "-q", "-am", "code")
	changed(true, "committed code")
	if _, err := BuildChanged(context.Background(), repo.Root, "0000000"); err == nil {
		t.Fatal("an unknown installed commit was compared")
	}
}

// signingHome is a jackstay home whose setting names identity; off macOS the test is skipped (nothing
// is ever signed there).
func signingHome(t *testing.T, identity string) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("signing is macOS's (W-070)")
	}
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	place := filepath.Join(home, ".all", IdentityFile)
	if err := os.MkdirAll(filepath.Dir(place), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(place, []byte(identity+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return place
}

// adHocUntilSigned is fakeCodesign whose display says ad-hoc until a --sign call: a local build.
func adHocUntilSigned(fail string, calls *[][]string) Runner {
	signing := fakeCodesign(fail, signedDisplay, calls)
	signed := false
	return func(argv []string) (supervise.Captured, error) {
		if argv[1] == "--sign" {
			signed = true
		}
		if argv[1] == "--display" && !signed {
			*calls = append(*calls, argv)
			return supervise.Captured{Stderr: []byte(adHocDisplay)}, nil
		}
		return signing(argv)
	}
}

// A signature that fails refuses the install: exit 2, the new folder removed, nothing live, and the
// message names the fix (#8).
func TestAFailedSignatureRefusesTheInstall(t *testing.T) {
	setting := signingHome(t, owner)
	prefix := filepath.Join(t.TempDir(), "prefix")
	run, _, stderr := options(prefix, binaries["1111111"])
	var calls [][]string
	run.Codesign = adHocUntilSigned("--sign", &calls)
	if code := Install(run); code != 2 || !strings.Contains(stderr.String(), "could not be signed") ||
		!strings.Contains(stderr.String(), "to fix it, put") || !strings.Contains(stderr.String(), setting) {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	state, _ := layout.Read(prefix)
	if state.Live != nil || len(state.Folders) != 0 {
		t.Fatalf("left %+v", state)
	}
}

// The binary of the new folder is signed before the folder is live; the links reach it (prove).
func TestTheNewBinaryIsSignedBeforeItIsLive(t *testing.T) {
	signingHome(t, owner)
	prefix := filepath.Join(t.TempDir(), "prefix")
	run, stdout, stderr := options(prefix, binaries["1111111"])
	var calls [][]string
	signing := adHocUntilSigned("", &calls)
	run.Codesign = func(argv []string) (supervise.Captured, error) {
		if state, _ := layout.Read(prefix); state.Live != nil {
			t.Errorf("%v ran with a live folder", argv)
		}
		return signing(argv)
	}
	if code := Install(run); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	state, _ := layout.Read(prefix)
	live := filepath.Join(state.Live.Path, layout.Program)
	if want := [][]string{DisplayArgs(live), SignArgs(owner, live), VerifyArgs(live), DisplayArgs(live)}; !slices.EqualFunc(calls, want, slices.Equal[[]string]) ||
		!strings.Contains(stdout.String(), "signed with") {
		t.Fatalf("calls %q\n%s", calls, stdout)
	}
	launchersRun(t, prefix)
}

// #12: a binary signed by a certificate with the identifier jst whose signature verifies (a release's,
// notarised) is installed as it is, whatever identity the setting names: signing it again would lose
// its notarisation. Installed again as the live version, it is still not signed again.
func TestACertifiedBinaryKeepsItsSignature(t *testing.T) {
	signingHome(t, "Apple Development: Someone Else (ZZZZZ99999)")
	prefix := filepath.Join(t.TempDir(), "prefix")
	run, stdout, stderr := options(prefix, binaries["1111111"])
	var calls [][]string
	run.Codesign = fakeCodesign("", signedDisplay, &calls)
	if code := Install(run); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	binary, before := liveBinary(t, prefix)
	if want := [][]string{DisplayArgs(binary), VerifyArgs(binary)}; !slices.EqualFunc(calls, want, slices.Equal[[]string]) ||
		!strings.Contains(stdout.String(), "kept the binary's own signature by \""+owner+"\"") {
		t.Fatalf("calls %q\n%s", calls, stdout)
	}
	again, stdout, stderr := options(prefix, binaries["1111111"])
	calls = nil
	again.Codesign = fakeCodesign("", signedDisplay, &calls)
	if code := Install(again); code != 0 || slices.ContainsFunc(calls, func(argv []string) bool { return argv[1] == "--sign" }) ||
		strings.Contains(stdout.String(), "signed again") {
		t.Fatalf("exit %d, calls %q: %s %s", code, calls, stdout, stderr)
	}
	if _, after := liveBinary(t, prefix); !os.SameFile(before, after) {
		t.Fatal("a certified live binary was replaced")
	}
}

// A certified binary whose signature does not verify strictly is signed, as an unsigned one is.
func TestACertifiedBinaryThatDoesNotVerifyIsSigned(t *testing.T) {
	signingHome(t, owner)
	prefix := filepath.Join(t.TempDir(), "prefix")
	run, stdout, stderr := options(prefix, binaries["1111111"])
	var calls [][]string
	signing := fakeCodesign("", signedDisplay, &calls)
	verified := 0
	run.Codesign = func(argv []string) (supervise.Captured, error) {
		if argv[1] == "--verify" {
			verified++
			if verified == 1 {
				calls = append(calls, argv)
				return supervise.Captured{Status: 1, Stderr: []byte("invalid signature\n")}, nil
			}
		}
		return signing(argv)
	}
	if code := Install(run); code != 0 || !strings.Contains(stdout.String(), "signed with") {
		t.Fatalf("exit %d: %s %s", code, stdout, stderr)
	}
	if !slices.ContainsFunc(calls, func(argv []string) bool { return argv[1] == "--sign" }) {
		t.Fatalf("calls %q", calls)
	}
}

// Real codesign, ad-hoc (any Mac): the identifier is set, the signature verifies, and the binary and
// a hook's link to it still run.
func TestAnAdHocSignatureWithTheIdentifierRuns(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("codesign is macOS's")
	}
	folder := t.TempDir()
	binary := filepath.Join(folder, layout.Program)
	data, err := os.ReadFile(binaries["1111111"])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, data, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{SignArgs("-", binary), VerifyArgs(binary)} {
		if said, err := realRunner(argv); err != nil || said.Status != 0 {
			t.Fatalf("%q: %v %s", argv, err, said.Stderr)
		}
	}
	said, err := realRunner(DisplayArgs(binary))
	if signature := ReadSignature(string(said.Stderr)); err != nil || !signature.AdHoc || signature.Identifier != SigningIdentifier {
		t.Fatalf("%+v %v", signature, err)
	}
	guard := filepath.Join(folder, layout.Guard)
	if err := os.Symlink(layout.Program, guard); err != nil {
		t.Fatal(err)
	}
	if err := prove.Guard(context.Background(), guard, nil); err != nil {
		t.Fatal(err)
	}
}

// resignCodesign answers display with adHoc for the live binary and with signedDisplay for the copy
// being signed again: the live version was installed before the setting named owner.
func resignCodesign(fail string, calls *[][]string) Runner {
	signing := fakeCodesign(fail, signedDisplay, calls)
	return func(argv []string) (supervise.Captured, error) {
		if argv[1] == "--display" && !strings.Contains(argv[len(argv)-1], resignPartial) {
			*calls = append(*calls, argv)
			return supervise.Captured{Stderr: []byte(adHocDisplay)}, nil
		}
		return signing(argv)
	}
}

func liveBinary(t *testing.T, prefix string) (string, os.FileInfo) {
	t.Helper()
	state, _ := layout.Read(prefix)
	binary := filepath.Join(state.Live.Path, layout.Program)
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	return binary, info
}

// The version already live, installed before the identity was set (or by another identity): the
// install signs a copy in the live folder, proves it and renames it over the live binary; an
// install of a binary already signed by the identity signs nothing.
func TestTheLiveVersionIsSignedAgainWhenItsSignatureIsNotTheIdentitys(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "prefix")
	first, _, stderr := options(prefix, binaries["1111111"])
	if code := Install(first); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	signingHome(t, owner)
	binary, before := liveBinary(t, prefix)
	again, stdout, stderr := options(prefix, binaries["1111111"])
	var calls [][]string
	again.Codesign = resignCodesign("", &calls)
	if code := Install(again); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	copied := filepath.Join(filepath.Dir(binary), resignPartial, layout.Program)
	want := [][]string{DisplayArgs(binary), SignArgs(owner, copied), VerifyArgs(copied), DisplayArgs(copied)}
	if !slices.EqualFunc(calls, want, slices.Equal[[]string]) || !strings.Contains(stdout.String(), "signed again with") {
		t.Fatalf("calls %q\n%s", calls, stdout)
	}
	_, after := liveBinary(t, prefix)
	if os.SameFile(before, after) || after.Mode().Perm() != 0o755 {
		t.Fatalf("the live binary was not replaced by the signed copy: %v", after.Mode())
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(binary), resignPartial)); !os.IsNotExist(err) {
		t.Fatalf("the signing folder is left: %v", err)
	}
	launchersRun(t, prefix)

	signed, stdout, stderr := options(prefix, binaries["1111111"])
	calls = nil
	signed.Codesign = fakeCodesign("", signedDisplay, &calls)
	if code := Install(signed); code != 0 || len(calls) != 2 || strings.Contains(stdout.String(), "signed again") {
		t.Fatalf("exit %d, calls %q: %s %s", code, calls, stdout, stderr)
	}
	if _, now := liveBinary(t, prefix); !os.SameFile(after, now) {
		t.Fatal("a binary signed by the identity was replaced")
	}
}

// A step of signing again that fails refuses the install and leaves the live binary as it was.
func TestAFailedSigningAgainLeavesTheLiveBinary(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "prefix")
	first, _, stderr := options(prefix, binaries["1111111"])
	if code := Install(first); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	signingHome(t, owner)
	for _, step := range []string{"--sign", "--verify"} {
		binary, before := liveBinary(t, prefix)
		again, _, stderr := options(prefix, binaries["1111111"])
		var calls [][]string
		again.Codesign = resignCodesign(step, &calls)
		if code := Install(again); code != 2 || !strings.Contains(stderr.String(), "could not be signed again") {
			t.Fatalf("%s: exit %d: %s", step, code, stderr)
		}
		if _, after := liveBinary(t, prefix); !os.SameFile(before, after) {
			t.Fatalf("%s: the live binary was replaced", step)
		}
		if _, err := os.Lstat(filepath.Join(filepath.Dir(binary), resignPartial)); !os.IsNotExist(err) {
			t.Fatalf("%s: the signing folder is left: %v", step, err)
		}
		launchersRun(t, prefix)
	}
}

// A live binary whose signature names the identity but does not verify (a damaged file) is signed
// again from the binary being installed.
func TestALiveSignatureThatDoesNotVerifyIsSignedAgain(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "prefix")
	first, _, stderr := options(prefix, binaries["1111111"])
	if code := Install(first); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	signingHome(t, owner)
	binary, before := liveBinary(t, prefix)
	if err := os.WriteFile(binary, []byte("damaged"), 0o755); err != nil {
		t.Fatal(err)
	}
	again, stdout, stderr := options(prefix, binaries["1111111"])
	var calls [][]string
	signing := fakeCodesign("", signedDisplay, &calls)
	again.Codesign = func(argv []string) (supervise.Captured, error) {
		if argv[1] == "--verify" && argv[len(argv)-1] == binary {
			calls = append(calls, argv)
			return supervise.Captured{Status: 1, Stderr: []byte("invalid signature\n")}, nil
		}
		return signing(argv)
	}
	if code := Install(again); code != 0 || !strings.Contains(stdout.String(), "signed again") || len(calls) != 5 {
		t.Fatalf("exit %d, calls %q: %s %s", code, calls, stdout, stderr)
	}
	if _, after := liveBinary(t, prefix); os.SameFile(before, after) {
		t.Fatal("the damaged binary was kept")
	}
	launchersRun(t, prefix)
}

// A `go install github.com/zima-oss/jackstay/cmd/jst@vX.Y.Z` build names its commit by the tag
// (version.CommitFromBuild); `jst install --binary` takes it.
func TestABinaryNamedByItsTagIsInstalled(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "prefix")
	run, _, stderr := options(prefix, binaries["v"+version.Version])
	if code := Install(run); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := launchersRun(t, prefix); got != identityOf("v"+version.Version) {
		t.Fatalf("installed %q", got)
	}
}

// W-075: a live binary signed by the identity with the earlier identifier (dev.jackstay.jst, 0.5.4 to
// 0.5.8) is not signed by it; an install of the live version signs it again, with `jst`.
func TestALiveBinaryWithTheEarlierIdentifierIsSignedAgain(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "prefix")
	first, _, stderr := options(prefix, binaries["1111111"])
	if code := Install(first); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	signingHome(t, owner)
	binary, before := liveBinary(t, prefix)
	earlier := strings.ReplaceAll(signedDisplay, "Identifier="+SigningIdentifier, "Identifier=dev.jackstay.jst")
	again, stdout, stderr := options(prefix, binaries["1111111"])
	var calls [][]string
	signing := fakeCodesign("", signedDisplay, &calls)
	again.Codesign = func(argv []string) (supervise.Captured, error) {
		if argv[1] == "--display" && argv[len(argv)-1] == binary {
			calls = append(calls, argv)
			return supervise.Captured{Stderr: []byte(earlier)}, nil
		}
		return signing(argv)
	}
	if code := Install(again); code != 0 || !strings.Contains(stdout.String(), "signed again") {
		t.Fatalf("exit %d: %s %s", code, stdout, stderr)
	}
	copied := filepath.Join(filepath.Dir(binary), resignPartial, layout.Program)
	if !slices.ContainsFunc(calls, func(argv []string) bool { return slices.Equal(argv, SignArgs(owner, copied)) }) ||
		!slices.Contains(SignArgs(owner, copied), "jst") {
		t.Fatalf("calls %q", calls)
	}
	if _, after := liveBinary(t, prefix); os.SameFile(before, after) {
		t.Fatal("the live binary was not replaced")
	}
	launchersRun(t, prefix)
}
