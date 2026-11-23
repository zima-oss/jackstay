// Package install is `jst install` and `jst uninstall` (docs/design/implementation.md sections 7.2 and 7.6):
// a version folder made whole and proved, then made live by layout.Activate, so every launcher
// moves in one rename; uninstall removes what install wrote and leaves ~/.jackstay.
package install

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/layout"
	"github.com/zima-oss/jackstay/internal/prove"
	"github.com/zima-oss/jackstay/internal/supervise"
	scopes "github.com/zima-oss/jackstay/internal/testing"
	"golang.org/x/mod/semver"
)

func init() {
	cli.Register(
		cli.Command{Name: "install", Summary: "put a whole version into PREFIX/lib/jackstay-<id> and make it live in one rename", Usage: installHelp, Run: Run},
		cli.Command{Name: "uninstall", Summary: "remove what install wrote (~/.jackstay is left)", Usage: uninstallHelp, Run: RunUninstall},
	)
}

// Module is jackstay's Go module path, which names a checkout of jackstay.
const Module = "github.com/zima-oss/jackstay"

// buildTimeout bounds `go build`.
const buildTimeout = 10 * time.Minute

const usage = "usage: jst install [--prefix DIR] [--checkout DIR | --binary FILE]"

// Options are one install.
type Options struct {
	// Prefix is P; empty is the account's ~/.local.
	Prefix string
	// Checkout builds from that checkout; Binary installs that file; neither: the checkout around
	// the working folder, else the running binary itself.
	Checkout, Binary string
	Stdout, Stderr   io.Writer
	// Now and AfterStep are the machine's unless a test gives its own (layout.Activation).
	Now       func() time.Time
	AfterStep func(step string) error
	// Codesign runs codesign (supervise.Capture under a deadline unless a test gives its own).
	Codesign Runner
	// Uncovered is nil when `jst test uncovered` passed for the commit of the checkout
	// (scopes.UncoveredPassed on this machine's system unless a test gives its own).
	Uncovered func(checkout, commit string) error
}

// Run is `jst install`.
func Run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("jst install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	prefix := flags.String("prefix", "", "")
	checkout := flags.String("checkout", "", "")
	binary := flags.String("binary", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 || *checkout != "" && *binary != "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return Install(Options{Prefix: absolute(*prefix), Checkout: absolute(*checkout), Binary: absolute(*binary), Stdout: stdout, Stderr: stderr})
}

func absolute(path string) string {
	if path == "" {
		return ""
	}
	if made, err := filepath.Abs(path); err == nil {
		return made
	}
	return path
}

// AccountPrefix is the account's own prefix, ~/.local of its home.
func AccountPrefix() string { return filepath.Join(homedir.AccountHome(), ".local") }

func (options Options) codesign() Runner {
	if options.Codesign != nil {
		return options.Codesign
	}
	return realRunner
}

func (options Options) now() time.Time {
	if options.Now != nil {
		return options.Now()
	}
	return time.Now()
}

// Install installs and returns the exit status (0 installed, 1 a step failed half-way, 2 refused).
func Install(options Options) int {
	if options.Prefix == "" {
		options.Prefix = AccountPrefix()
	}
	refuse := func(format string, values ...any) int {
		fmt.Fprintf(options.Stderr, "jst install: "+format+"; nothing was changed\n", values...)
		return 2
	}
	identity, setting, err := SigningIdentity()
	if err != nil {
		return refuse("the signing setting cannot be read: %v", err)
	}
	ctx := context.Background()
	staged, err := os.MkdirTemp("", "jackstay-install.")
	if err != nil {
		return refuse("%v", err)
	}
	defer func() { _ = os.RemoveAll(staged) }()
	binary, source, err := options.binary(ctx, staged)
	if err != nil {
		return refuse("%v", err)
	}
	versionText, err := Identity(ctx, binary)
	if err != nil {
		return refuse("%v", err)
	}
	_, commit := splitVersion(versionText)
	if commit == "unknown" || strings.HasSuffix(commit, "+changes") {
		if options.Binary != "" || options.Checkout == "" && !strings.HasPrefix(source, "/") {
			return refuse("%s says it is %s: a binary is installed only when it names one commit; build it from a clean checkout", binary, versionText)
		}
		if samePath(options.Prefix, AccountPrefix()) {
			return refuse("the checkout has uncommitted changes (%s): an installed version names one commit (W-020); commit first, or give another --prefix to try it", versionText)
		}
	}
	if err := options.uncoveredPassed(source, commit); err != nil {
		return refuse("%v; a checkout's commit is installed only after the tests CI does not run on this system passed on it", err)
	}
	if err := os.MkdirAll(layout.Lib(options.Prefix), 0o755); err != nil { // #nosec G301 -- read by every shell of the account
		return refuse("%v", err)
	}
	held, err := layout.Lock(options.Prefix)
	if err != nil {
		return refuse("%v", err)
	}
	defer held.Release()
	state, err := layout.Read(options.Prefix)
	if err != nil {
		return refuse("%s cannot be read: %v", options.Prefix, err)
	}
	if folder, ok := recoverableLive(state, versionText); ok {
		state.Link = layout.LinkToFolder
		state.Live = &folder
		state.Folders = append(state.Folders, folder)
	}
	if refusal := state.Refusal(); refusal != nil {
		return refuse("%v", refusal)
	}
	if problem := goingBack(state, versionText, options.Prefix); problem != "" {
		return refuse("%s", problem)
	}
	activation := layout.Activation{Prefix: options.Prefix, AfterStep: options.AfterStep}
	already := state.Live != nil && state.Live.Version == versionText
	target := ""
	if already {
		target = state.Live.Path
		replaced, err := refreshLive(ctx, options.codesign(), identity, target, binary, versionText)
		if err != nil {
			if identity == "" {
				return refuse("the live binary could not be refreshed: %v", err)
			}
			code := refuse("the live binary could not be signed again with %q (the identity in %s): %v", identity, setting, err)
			fmt.Fprintf(options.Stderr, "jst install: to fix it, %s\n", SigningFix(identity, setting))
			return code
		}
		switch replaced {
		case Repaired:
			fmt.Fprintf(options.Stdout, "jst install: repaired the live %s\n", filepath.Join(target, layout.Program))
		case SignedAgain:
			fmt.Fprintf(options.Stdout, "jst install: the live %s was signed again with %q, identifier %s\n",
				filepath.Join(target, layout.Program), identity, SigningIdentifier)
		case ReplacedAsSigned:
			fmt.Fprintf(options.Stdout, "jst install: the live %s was replaced by the binary being installed, which keeps its own signature\n",
				filepath.Join(target, layout.Program))
		}
	} else {
		target, err = layout.MakeFolder(options.Prefix, versionText, source, options.now(), layout.CopyBinary(binary))
		if err != nil {
			return refuse("the version folder could not be made: %v", err)
		}
		if identity != "" {
			newBinary := filepath.Join(target, layout.Program)
			kept, signature, err := KeepsItsSignature(options.codesign(), newBinary)
			switch {
			case err != nil:
				layout.RemoveFolder(target)
				return refuse("the binary's signature could not be read: %v", err)
			case kept:
				fmt.Fprintf(options.Stdout, "jst install: kept the binary's own signature by %q, identifier %s (signing it again would lose its notarisation)\n",
					signature.Authority, signature.Identifier)
			default:
				if err := Sign(options.codesign(), identity, newBinary); err != nil {
					layout.RemoveFolder(target)
					code := refuse("the binary could not be signed with %q (the identity in %s): %v", identity, setting, err)
					fmt.Fprintf(options.Stderr, "jst install: to fix it, %s\n", SigningFix(identity, setting))
					return code
				}
				fmt.Fprintf(options.Stdout, "jst install: signed with %q, identifier %s\n", identity, SigningIdentifier)
			}
		}
		if err := prove.Folder(ctx, target, versionText); err != nil {
			layout.RemoveFolder(target)
			return refuse("the new folder does not work: %v", err)
		}
	}
	if !already || !state.AllFixed() {
		if err := activation.Activate(target); err != nil {
			var stepError *layout.StepError
			if errors.As(err, &stepError) || errors.Is(err, layout.ErrStopped) {
				fmt.Fprintf(options.Stderr, "jst install: %v\n", err)
				return 1
			}
			if !already {
				layout.RemoveFolder(target)
			}
			return refuse("%v", err)
		}
	}
	return options.finish(state, versionText, target, already)
}

// uncoveredPassed is nil unless a checkout's commit is installed, at any prefix, without a passed
// `jst test uncovered` for it (docs/design/land.md section 7.4). A binary and a trial build with
// changes (only ever at another prefix) need none.
func (options Options) uncoveredPassed(source, commit string) error {
	if !strings.HasPrefix(source, "/") || commit == "unknown" || strings.HasSuffix(commit, "+changes") {
		return nil
	}
	if options.Uncovered != nil {
		return options.Uncovered(source, commit)
	}
	return scopes.UncoveredPassed(source, commit, runtime.GOOS)
}

// recoverableLive recognizes only the active folder that jackstay created for
// this exact version. A missing or non-executable program may be repaired, but
// foreign links, metadata and non-regular program paths remain a refusal.
func recoverableLive(state layout.State, versionText string) (layout.Folder, bool) {
	if state.Link != layout.LinkElsewhere || strings.Contains(state.LinkTarget, "/") ||
		!strings.HasPrefix(state.LinkTarget, layout.FolderPrefix) {
		return layout.Folder{}, false
	}
	path := filepath.Join(layout.Lib(state.Prefix), state.LinkTarget)
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return layout.Folder{}, false
	}
	for _, marker := range []string{layout.VersionFile, layout.SourceFile} {
		markerPath := filepath.Join(path, marker)
		markerInfo, err := os.Lstat(markerPath)
		if err != nil || !markerInfo.Mode().IsRegular() {
			return layout.Folder{}, false
		}
		data, err := os.ReadFile(markerPath)
		if err != nil || strings.TrimSpace(string(data)) == "" {
			return layout.Folder{}, false
		}
		if marker == layout.VersionFile && strings.TrimSpace(string(data)) != versionText {
			return layout.Folder{}, false
		}
	}
	for _, name := range layout.Launchers {
		if name == layout.Program {
			continue
		}
		target, err := os.Readlink(filepath.Join(path, name))
		if err != nil || target != layout.Program {
			return layout.Folder{}, false
		}
	}
	program, err := os.Lstat(filepath.Join(path, layout.Program))
	if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && !program.Mode().IsRegular() {
		return layout.Folder{}, false
	}
	return layout.Folder{Name: state.LinkTarget, Path: path, Version: versionText, Made: info.ModTime()}, true
}

// finish prunes and says what was done. It reads only the prefix, so a stop after the activation
// is finished by running install again. The folder that was live before this install is kept, so
// a run that started with it ends with it; every older one is removed.
func (options Options) finish(earlier layout.State, versionText, target string, already bool) int {
	state, err := layout.Read(options.Prefix)
	if err != nil {
		fmt.Fprintf(options.Stderr, "jst install: %s cannot be read after the install: %v\n", options.Prefix, err)
		return 1
	}
	keep := state.Before()
	if !already && earlier.Live != nil {
		keep = earlier.Live
	}
	before, keepName := "none", ""
	if keep != nil {
		before, keepName = keep.Name+" ("+keep.Version+")", keep.Name
	}
	removed, problems := layout.Prune(state, keepName)
	if already && earlier.AllFixed() {
		fmt.Fprintf(options.Stdout, "jst install: %s is already installed (%s); the version before: %s\n", versionText, target, before)
	} else {
		launchers := make([]string, 0, len(layout.Launchers))
		for _, name := range layout.Launchers {
			launchers = append(launchers, filepath.Join(layout.Bin(options.Prefix), name))
		}
		fmt.Fprintf(options.Stdout, "jackstay %s installed: %s, %s -> %s; the version before: %s\n", versionText,
			strings.Join(launchers, ", "), layout.Link(options.Prefix), filepath.Base(target), before)
	}
	for _, path := range removed {
		fmt.Fprintf(options.Stdout, "jst install: removed %s\n", path)
	}
	for _, problem := range problems {
		fmt.Fprintf(options.Stderr, "jst install: warning: %v\n", problem)
	}
	return 0
}

// binary is the file to install and the SOURCE it is written with: built from a checkout into
// staged, a given file, or the running binary.
func (options Options) binary(ctx context.Context, staged string) (path, source string, err error) {
	checkout := options.Checkout
	if checkout == "" && options.Binary == "" {
		if here, err := os.Getwd(); err == nil {
			checkout = FindCheckout(here)
		}
	}
	switch {
	case options.Binary != "":
		return options.Binary, "binary " + options.Binary, nil
	case checkout != "":
		built, err := Build(ctx, checkout, filepath.Join(staged, layout.Program))
		return built, checkout, err
	default:
		self, err := os.Executable()
		if err != nil {
			return "", "", err
		}
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			self = resolved
		}
		return self, "release", nil
	}
}

// FindCheckout is the nearest folder at or above start whose go.mod is jackstay's module ("" if none).
func FindCheckout(start string) string {
	for folder := start; ; folder = filepath.Dir(folder) {
		if data, err := os.ReadFile(filepath.Join(folder, "go.mod")); err == nil { // #nosec G304 -- a go.mod above the working folder
			first, _, _ := strings.Cut(string(data), "\n")
			if strings.TrimSpace(first) == "module "+Module {
				return folder
			}
		}
		if filepath.Dir(folder) == folder {
			return ""
		}
	}
}

// Build builds ./cmd/jst from the selected commit. A checkout with changes remains a trial build.
func Build(ctx context.Context, checkout, out string) (string, error) {
	return buildFromCheckout(ctx, checkout, out, BuildAs)
}

func buildFromCheckout(ctx context.Context, checkout, out string, build func(context.Context, string, string, string) (string, error)) (string, error) {
	commit, label, err := checkoutRevision(ctx, checkout)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(label, "+changes") {
		return build(ctx, checkout, out, label)
	}
	tree, err := os.MkdirTemp("", "jackstay-install-source.")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tree) }()
	if err := extractCommit(ctx, checkout, commit, tree); err != nil {
		return "", fmt.Errorf("cannot stage the build tree for %s: %w", label, err)
	}
	return build(ctx, tree, out, label)
}

// BuildAs builds ./cmd/jst of a tree (a checkout, or a tag's files) into out, naming commit.
func BuildAs(ctx context.Context, tree, out, commit string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	argv := []string{"go", "build", "-trimpath", "-ldflags", "-s -w -X " + Module + "/internal/version.Commit=" + commit, "-o", out, "./cmd/jst"}
	built, err := supervise.Capture(bounded, argv, tree, append(os.Environ(), "CGO_ENABLED=0"), nil)
	if err != nil {
		return "", fmt.Errorf("go build did not finish: %w", err)
	}
	if built.Status != 0 {
		text := strings.TrimSpace(string(built.Stderr) + string(built.Stdout))
		if len(text) > 2000 {
			text = text[len(text)-2000:]
		}
		return "", fmt.Errorf("go build failed: %s", text)
	}
	return out, nil
}

// BuildInputs are the pathspecs of what goes into the binary (`go build ./cmd/jst`): the packages
// under cmd/ and internal/ (their embedded files included) and the module files; tests do not.
// A change elsewhere (docs, the justfile, bin/, the release config) needs no install (W-020).
var BuildInputs = []string{"cmd", "internal", "go.mod", "go.sum", ":(exclude)*_test.go"}

// Commit is the checkout's short commit, with "+changes" when a build input has uncommitted changes.
func Commit(ctx context.Context, checkout string) (string, error) {
	_, label, err := checkoutRevision(ctx, checkout)
	return label, err
}

func checkoutRevision(ctx context.Context, checkout string) (commit, label string, err error) {
	git := gitcli.Git{Dir: checkout}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	commit, err = git.Line(bounded, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("%s: the commit cannot be read: %w", checkout, err)
	}
	label, err = git.Line(bounded, "rev-parse", "--short=7", commit)
	if err != nil {
		return "", "", fmt.Errorf("%s: the commit cannot be shortened: %w", checkout, err)
	}
	changed, err := git.Run(bounded, append([]string{"status", "--porcelain", "--"}, BuildInputs...)...)
	if err != nil {
		return "", "", fmt.Errorf("%s: its changes cannot be read: %w", checkout, err)
	}
	if strings.TrimSpace(string(changed)) != "" {
		label += "+changes"
	}
	return commit, label, nil
}

// BuildChanged says whether what goes into the binary differs between the commit installed and the
// checkout as it is (its commits and its uncommitted changes to the build inputs).
func BuildChanged(ctx context.Context, checkout, installed string) (bool, error) {
	git := gitcli.Git{Dir: checkout}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := git.Run(bounded, append([]string{"diff", "--quiet", installed, "--"}, BuildInputs...)...)
	var exitErr *gitcli.ExitError
	switch {
	case err == nil:
		return false, nil
	case errors.As(err, &exitErr) && exitErr.Code == 1:
		return true, nil
	}
	return false, fmt.Errorf("%s: the build inputs since %s cannot be compared: %w", checkout, installed, err)
}

// Identity is what a binary's `jst version` says, without "jackstay ".
func Identity(ctx context.Context, binary string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, prove.Timeout)
	defer cancel()
	said, err := supervise.Capture(bounded, []string{binary, "version"}, "/", os.Environ(), nil)
	if err != nil {
		return "", fmt.Errorf("%s does not run: %w", binary, err)
	}
	text, found := strings.CutPrefix(strings.TrimSpace(string(said.Stdout)), "jackstay ")
	if said.Status != 0 || !found || !strings.Contains(text, " (") {
		return "", fmt.Errorf("%s is not a jackstay binary (`version` said %q)", binary, strings.TrimSpace(string(said.Stdout)))
	}
	return text, nil
}

// goingBack is why installing versionText over the live version is refused ("" when it is not).
func goingBack(state layout.State, versionText, prefix string) string {
	live := state.LiveVersion()
	if live == "" {
		return ""
	}
	order := Compare(versionOf(versionText), versionOf(live))
	switch {
	case order < 0:
		return fmt.Sprintf("%s is older than the installed %s: a version only moves forward; install a newer one", versionText, live)
	case order == 0 && versionText != live && samePath(prefix, AccountPrefix()):
		return fmt.Sprintf("%s and the installed %s are one version from two commits: a version names one commit (W-020); move the version", versionText, live)
	}
	return ""
}

func versionOf(text string) string {
	version, _ := splitVersion(text)
	return version
}

func splitVersion(text string) (version, commit string) {
	version, rest, _ := strings.Cut(text, " (")
	return version, strings.TrimSuffix(rest, ")")
}

// Compare orders two versions (-1, 0, 1) by Semantic Versioning precedence: a pre-release orders
// before its release (0.6.0-dev.4 < 0.6.0), pre-releases by their dot-separated identifiers
// (dev.3 < dev.4), and MAJOR.MINOR stands for MAJOR.MINOR.0. A text that is not a version orders
// before every version.
func Compare(left, right string) int {
	return semver.Compare("v"+left, "v"+right)
}

func samePath(left, right string) bool {
	resolve := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return filepath.Clean(path)
	}
	return resolve(left) == resolve(right)
}
