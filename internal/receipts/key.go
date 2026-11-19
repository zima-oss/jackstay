package receipts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/judge"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/testreport"
	"github.com/zima-oss/jackstay/internal/version"
)

// Unkeyable says no key can be made for this tree and command; the run goes on without one.
type Unkeyable struct{ Why string }

func (problem *Unkeyable) Error() string { return problem.Why }

func noKey(format string, values ...any) error {
	return &Unkeyable{Why: fmt.Sprintf(format, values...)}
}

// ignoreFiles is every .gitignore of the checkout, as a pathspec.
const ignoreFiles = ":(top,glob)**/.gitignore"

// gitBudget bounds one git question of the key; toolchainBudget one toolchain command.
const (
	gitBudget       = 120 * time.Second
	toolchainBudget = 60 * time.Second
)

// makeVariables are always in the key: they carry `make X=1` to an inner make.
var makeVariables = []string{"MAKEFLAGS", "MFLAGS", "MAKEOVERRIDES"}

// jobServer is make's job server address, which differs from run to run.
var jobServer = regexp.MustCompile(`(--jobserver-(?:auth|fds))=\S+`)

// Keying is what a key is made from.
type Keying struct {
	// Root is the checkout's root ("" without a jackstay.json); Config its jackstay.json.
	Root   string
	Config config.Config
	// Project, Command and Dir (the folder it runs in) are part of the key.
	Project string
	Command []string
	Dir     string
	// Scope is a scope's definition (`jst test <scope>`); nil for `jst test --`.
	Scope any
	// Environ is the environment the command gets (nil: jackstay's own).
	Environ []string
}

// Key is a key and what went into it.
type Key struct {
	Value    string
	Material Material
	// Stamps are each input's identity and times by name: equal stamps before and after a run
	// mean no input was rewritten meanwhile, also not to the same content.
	Stamps map[string]string
}

// Material is everything the key is a digest of.
type Material struct {
	Schema     int               `json:"schema"`
	Jackstay   string            `json:"jackstay"`
	Project    string            `json:"project"`
	Command    []string          `json:"command"`
	Cwd        string            `json:"cwd"`
	Manifest   string            `json:"manifest"`
	Files      int               `json:"files"`
	Rules      Rules             `json:"rules"`
	Facts      Facts             `json:"facts"`
	Refs       map[string]string `json:"refs"`
	MergeBases map[string]string `json:"merge_bases"`
	Scope      any               `json:"scope,omitempty"`
	Head       string            `json:"head,omitempty"`
}

// Rules are the input lists and the command's patterns, so a changed rule is a new key.
type Rules struct {
	Inputs     any            `json:"inputs"`
	Exclude    any            `json:"exclude"`
	ExtraFiles any            `json:"extra_files"`
	Evidence   any            `json:"evidence"`
	Veto       any            `json:"veto"`
	Adapter    map[string]any `json:"adapter,omitempty"`
}

// Facts are the machine and environment the result is taken to depend on.
type Facts struct {
	ExecutionVersion int                `json:"execution_version"`
	System           string             `json:"system"`
	Build            string             `json:"build"`
	Machine          string             `json:"machine"`
	Toolchain        []ToolSaid         `json:"toolchain"`
	Env              map[string]string  `json:"env"`
	EnvPatterns      []string           `json:"env_patterns"`
	Make             map[string]*string `json:"make"`
}

// ToolSaid is one toolchain command and what it printed.
type ToolSaid struct {
	Command []string `json:"command"`
	Said    string   `json:"said"`
}

// Section is the receipts section of a configuration (empty when there is none).
func Section(loaded config.Config) map[string]any { return loaded.Section("receipts") }

// texts is a list of strings of the section (nil when absent).
func texts(value any) []string {
	elements, _ := value.([]any)
	var out []string
	for _, element := range elements {
		if text, ok := element.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// KeyOf is the key of running keying.Command in keying.Dir of the checkout: a SHA-256 over the
// declared inputs read from the working tree (ignore rules only from committed .gitignore files),
// the toolchain, the machine, the named environment, refs and merge bases, the rules themselves
// and jackstay's version. An *Unkeyable when there can be none.
func KeyOf(ctx context.Context, keying Keying) (Key, error) {
	if keying.Root == "" {
		return Key{}, noKey("there is no jackstay.json at or above this folder")
	}
	section := Section(keying.Config)
	git := gitcli.Git{Dir: keying.Root}
	files, err := inputFiles(ctx, git, section)
	if err != nil {
		return Key{}, err
	}
	manifest := sha256.New()
	stamps := make(map[string]string, len(files))
	for _, name := range files {
		kind, digest, stamp, err := hashInput(filepath.Join(keying.Root, name))
		if err != nil {
			return Key{}, err
		}
		fmt.Fprintf(manifest, "%s %s %s\x00", kind, digest, name)
		stamps[name] = stamp
	}
	entry := CommandEntry(section, keying.Command)
	refs, bases := map[string]string{}, map[string]string{}
	for _, name := range append(texts(section["refs"]), texts(entry["refs"])...) {
		commit, err := askGit(ctx, git, "rev-parse", "--verify", "--quiet", name+"^{commit}")
		if err != nil {
			return Key{}, err
		}
		refs[name] = commit
	}
	for _, name := range append(texts(section["merge_bases"]), texts(entry["merge_bases"])...) {
		base, err := askGit(ctx, git, "merge-base", "HEAD", name)
		if err != nil {
			return Key{}, err
		}
		bases[name] = base
	}
	facts, err := EnvironmentFacts(ctx, section, keying.Dir, keying.Environ)
	if err != nil {
		return Key{}, err
	}
	relative, err := filepath.Rel(realPath(keying.Root), realPath(keying.Dir))
	if err != nil {
		relative = keying.Dir
	}
	material := Material{
		Schema: Schema, Jackstay: version.Version, Project: keying.Project, Command: slices.Clone(keying.Command),
		Cwd: relative, Manifest: hex.EncodeToString(manifest.Sum(nil)), Files: len(files),
		Rules: Rules{Inputs: section["inputs"], Exclude: section["exclude"], ExtraFiles: section["extra_files"],
			Evidence: entry["evidence"], Veto: entry["veto"]},
		Facts: facts, Refs: refs, MergeBases: bases, Scope: keying.Scope,
	}
	if name, _ := entry["judge"].(string); keying.Scope == nil && judge.AdapterVersion(judge.Kind(name)) != 0 {
		material.Rules.Adapter = map[string]any{"judge": name, "version": judge.AdapterVersion(judge.Kind(name))}
		if testreport.FileJudge(name) {
			if reports, found := entry["reports"]; found {
				material.Rules.Adapter["reports"] = reports
			} else {
				material.Rules.Adapter["report"] = entry["report"]
			}
			if name == string(judge.XCResultJSON) {
				material.Rules.Adapter["xcresult_bundles"] = entry["xcresult_bundles"]
			}
			if name == string(judge.JUnit) {
				material.Rules.Adapter["empty_exit"] = entry["empty_exit"]
			}
		}
	}
	if section["include_head"] == true {
		material.Head, _ = askGit(ctx, git, "rev-parse", "HEAD")
	}
	data, err := json.Marshal(material)
	if err != nil {
		return Key{}, noKey("the key's material cannot be written: %v", err)
	}
	digest := sha256.Sum256(data)
	return Key{Value: hex.EncodeToString(digest[:]), Material: material, Stamps: stamps}, nil
}

// askGit is git's answer as one trimmed word, or an *Unkeyable naming the question.
func askGit(ctx context.Context, git gitcli.Git, args ...string) (string, error) {
	out, err := runGit(ctx, git, nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runGit runs git under the key's budget; any failure is an *Unkeyable.
func runGit(ctx context.Context, git gitcli.Git, input []byte, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, gitBudget)
	defer cancel()
	out, err := git.RunInput(bounded, input, args...)
	if err != nil {
		var exitErr *gitcli.ExitError
		if errors.As(err, &exitErr) {
			said := strings.TrimSpace(exitErr.Stderr)
			if len(said) > 200 {
				said = said[:200]
			}
			return nil, noKey("git %s failed: %s", strings.Join(args[:min(2, len(args))], " "), said)
		}
		return nil, noKey("git could not be run: %v", err)
	}
	return out, nil
}

// names are the NUL-separated names of git's -z output.
func names(listed []byte) []string {
	var out []string
	for _, name := range bytes.Split(listed, []byte{0}) {
		if len(name) > 0 {
			out = append(out, string(name))
		}
	}
	return out
}

// onlyCommittedIgnoreRules refuses a key unless every ignore rule git applies to the listing is a
// committed one: a new or edited .gitignore could hide a source file the build still compiles.
func onlyCommittedIgnoreRules(ctx context.Context, git gitcli.Git) error {
	listed, err := runGit(ctx, git, nil, "ls-files", "-z", "-v", "--", ignoreFiles)
	if err != nil {
		return err
	}
	tracked := map[string]bool{}
	for _, item := range names(listed) {
		if len(item) < 3 {
			continue
		}
		tag, name := item[:1], item[2:]
		if tag != "H" {
			return noKey("%s: git is told not to look at this ignore file (assume-unchanged or skip-worktree), "+
				"so whether its rules are the committed ones cannot be known", name)
		}
		tracked[name] = true
	}
	changed, err := runGit(ctx, git, nil, "diff", "--name-only", "-z", "HEAD", "--", ignoreFiles)
	if err != nil {
		return err
	}
	if found := names(changed); len(found) > 0 {
		return noKey("%s differs from the commit (HEAD): only committed ignore rules may hide a file from the key; "+
			"commit the change or take it back", found[0])
	}
	others, err := runGit(ctx, git, nil, "ls-files", "-z", "--others", "--", ignoreFiles)
	if err != nil {
		return err
	}
	untracked := names(others)
	if len(untracked) == 0 {
		return nil
	}
	// An untracked .gitignore changes nothing where a committed rule already ignores its folder.
	folders := map[string]string{}
	for _, name := range untracked {
		folder := path.Dir(name)
		if folder == "." || onlyDots(folder) {
			return noKey("%s is not committed: only committed ignore rules may hide a file from the key; commit it or remove it", name)
		}
		folders[folder] = name
	}
	var asked bytes.Buffer
	for _, folder := range sortedKeys(folders) {
		asked.WriteString(folder)
		asked.WriteByte(0)
	}
	said, err := checkIgnore(ctx, git, asked.Bytes())
	if err != nil {
		return err
	}
	fields := bytes.Split(said, []byte{0})
	for index := 0; index+3 < len(fields); index += 4 {
		pattern := string(fields[index+2])
		if tracked[string(fields[index])] && pattern != "" && !strings.HasPrefix(pattern, "!") {
			delete(folders, string(fields[index+3]))
		}
	}
	if len(folders) > 0 {
		left := make([]string, 0, len(folders))
		for _, name := range folders {
			left = append(left, name)
		}
		sort.Strings(left)
		return noKey("%s is not committed and no committed rule ignores its folder: only committed ignore rules may "+
			"hide a file from the key (commit it, remove it, or ignore the folder in a committed .gitignore)", left[0])
	}
	return nil
}

// checkIgnore asks git which rule ignores each folder; status 1 (none is ignored) is an answer.
func checkIgnore(ctx context.Context, git gitcli.Git, asked []byte) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, gitBudget)
	defer cancel()
	out, err := git.RunInput(bounded, asked, "check-ignore", "--no-index", "-v", "-z", "-n", "--stdin")
	var exitErr *gitcli.ExitError
	switch {
	case errors.As(err, &exitErr) && exitErr.Code == 1:
		return out, nil
	case errors.As(err, &exitErr):
		return nil, noKey("git check-ignore failed: %s", strings.TrimSpace(exitErr.Stderr))
	case err != nil:
		return nil, noKey("git could not be run: %v", err)
	}
	return out, nil
}

func onlyDots(folder string) bool {
	for _, part := range strings.Split(folder, "/") {
		if part != "." && part != ".." {
			return false
		}
	}
	return true
}

func sortedKeys[Value any](members map[string]Value) []string {
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// inputFiles are the input paths, relative to the checkout, sorted: the tracked files under
// `inputs` and the untracked ones the checkout's own .gitignore files do not ignore, less
// `exclude`, plus `extra_files`.
func inputFiles(ctx context.Context, git gitcli.Git, section map[string]any) ([]string, error) {
	inputs := texts(section["inputs"])
	if len(inputs) == 0 {
		return nil, noKey("jackstay.json names no receipts.inputs")
	}
	extra := texts(section["extra_files"])
	for _, each := range append(slices.Clone(inputs), extra...) {
		if filepath.IsAbs(each) || slices.Contains(strings.Split(each, "/"), "..") || strings.HasPrefix(each, ":") {
			return nil, noKey("jackstay.json: an input must be a plain path inside the checkout: %s", each)
		}
	}
	exclude := texts(section["exclude"])
	if err := onlyCommittedIgnoreRules(ctx, git); err != nil {
		return nil, err
	}
	found := map[string]bool{}
	for _, each := range inputs {
		// Only committed ignore rules count: --exclude-per-directory leaves out a person's own ignore
		// file and .git/info/exclude (what they hide is still compiled by SwiftPM).
		listed, err := runGit(ctx, git, nil, "ls-files", "-z", "--cached", "--others", "--exclude-per-directory=.gitignore",
			"--", ":(literal)"+each)
		if err != nil {
			return nil, err
		}
		named := names(listed)
		if len(named) == 0 {
			return nil, noKey("jackstay.json: the input %q names no file in this checkout", each)
		}
		for _, name := range named {
			found[name] = true
		}
	}
	for name := range found {
		if Excluded(name, exclude) {
			delete(found, name)
		}
	}
	for _, name := range extra {
		found[name] = true
	}
	if len(found) == 0 {
		return nil, noKey("jackstay.json: the inputs, less the excluded, are no file at all")
	}
	return sortedKeys(found), nil
}

// Excluded says whether a path (relative, with /) is left out by one of the patterns. A pattern is
// for the whole path from the root: `*.md` is a file at the root, `**/*.md` one anywhere, `docs/**`
// everything under docs.
func Excluded(name string, patterns []string) bool {
	trimmed := strings.TrimRight(name, "/")
	for _, pattern := range patterns {
		if patternRegexp(pattern).MatchString(trimmed) {
			return true
		}
	}
	return false
}

// patternRegexp is an exclude pattern over a whole path: `*` and `?` stay inside one folder, `**`
// crosses folders, `**/` is any folders or none.
func patternRegexp(pattern string) *regexp.Regexp {
	var built strings.Builder
	built.WriteString("^")
	for index := 0; index < len(pattern); {
		switch {
		case strings.HasPrefix(pattern[index:], "**/"):
			built.WriteString("(?:.*/)?")
			index += 3
		case strings.HasPrefix(pattern[index:], "**"):
			built.WriteString(".*")
			index += 2
		case pattern[index] == '*':
			built.WriteString("[^/]*")
			index++
		case pattern[index] == '?':
			built.WriteString("[^/]")
			index++
		default:
			built.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
			index++
		}
	}
	built.WriteString("$")
	return regexp.MustCompile(built.String())
}

// stampOf is a file's identity and times: it changes when the file is rewritten, also with the
// same content.
func stampOf(status *unix.Stat_t) string {
	return fmt.Sprintf("%d:%d:%d:%d", status.Ino, status.Size, status.Mtim.Nano(), status.Ctim.Nano())
}

// hashInput is (kind, digest, stamp) of one input: a file's permission bits and content, a link's
// target (and, when that is a file, its bits and content), or that it is missing.
func hashInput(place string) (kind, digest, stamp string, err error) {
	var status unix.Stat_t
	if lstatErr := unix.Lstat(place, &status); lstatErr != nil {
		return "missing", "-", "-", nil
	}
	stamp = stampOf(&status)
	switch status.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		target, readErr := os.Readlink(place)
		if readErr != nil {
			return "", "", "", noKey("%s could not be read: %v", place, readErr)
		}
		var followed unix.Stat_t
		statErr := unix.Stat(place, &followed)
		if statErr == nil && followed.Mode&unix.S_IFMT == unix.S_IFDIR {
			return "", "", "", noKey("%s is a link to a folder (%s): the files behind it would not be in the key "+
				"(name the real folder in receipts.inputs, or leave the link out with receipts.exclude if the tests do not read it)",
				place, target)
		}
		linkDigest := sha256.New()
		linkDigest.Write([]byte(target))
		if statErr == nil && followed.Mode&unix.S_IFMT == unix.S_IFREG {
			// What the link leads to is read by the build, wherever it is.
			stamp += ">" + stampOf(&followed)
			content, contentErr := fileDigest(place)
			if contentErr != nil {
				return "", "", "", contentErr
			}
			fmt.Fprintf(linkDigest, "\x00%o\x00%s", followed.Mode&0o7777, content)
		}
		return "link", hex.EncodeToString(linkDigest.Sum(nil)), stamp, nil
	case unix.S_IFDIR:
		return "", "", "", noKey("%s is a folder, not a file: a submodule or another repository inside the inputs "+
			"(leave it out with receipts.exclude if the tests do not read it)", place)
	case unix.S_IFREG:
		content, contentErr := fileDigest(place)
		if contentErr != nil {
			return "", "", "", contentErr
		}
		return fmt.Sprintf("file:%o", status.Mode&0o7777), content, stamp, nil
	default:
		return "", "", "", noKey("%s is neither a file nor a link", place)
	}
}

func fileDigest(place string) (string, error) {
	file, err := os.Open(place)
	if err != nil {
		return "", noKey("%s could not be read: %v", place, err)
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", noKey("%s could not be read: %v", place, err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// EnvironmentFacts are the machine, the toolchain's words, the named variables and make's, as a
// key reads them (internal/testing keys a gate's step marks by them); an *Unkeyable on failure.
func EnvironmentFacts(ctx context.Context, section map[string]any, dir string, environ []string) (Facts, error) {
	if environ == nil {
		environ = os.Environ()
	}
	// Earlier keys were made with the launcher's toolchain rather than the test's.
	// A receipt or gate mark using those facts needs a new run.
	facts := Facts{ExecutionVersion: 1, System: systemVersion(), Build: systemBuild(), Machine: machine(), Toolchain: []ToolSaid{},
		Env: map[string]string{}, Make: map[string]*string{}}
	tools, _ := section["toolchain"].([]any)
	for _, raw := range tools {
		tool := texts(raw)
		if len(tool) == 0 {
			return Facts{}, noKey("jackstay.json: receipts.toolchain must be a list of commands, each a list of words")
		}
		said, err := runTool(ctx, tool, dir, environ)
		if err != nil {
			return Facts{}, err
		}
		facts.Toolchain = append(facts.Toolchain, ToolSaid{Command: tool, Said: said})
	}
	patterns := texts(section["env"])
	values := map[string]string{}
	for _, pair := range environ {
		name, value, _ := strings.Cut(pair, "=")
		values[name] = value
	}
	for name, value := range values {
		// A lock's "held" variable and the outer run's id change with every run and say nothing a
		// test reads: in the key they would give every run a key of its own.
		if strings.HasSuffix(name, "_LOCK_HELD") || name == supervise.RunIDVariable || name == supervise.RunProjectVariable ||
			name == flock.HeldVariable {
			continue
		}
		for _, pattern := range patterns {
			if matched, _ := path.Match(strings.ReplaceAll(pattern, "[!", "[^"), name); matched {
				facts.Env[name] = value
				break
			}
		}
	}
	facts.EnvPatterns = slices.Sorted(slices.Values(patterns))
	if facts.EnvPatterns == nil {
		facts.EnvPatterns = []string{}
	}
	for _, name := range makeVariables {
		if value, set := values[name]; set {
			cleaned := jobServer.ReplaceAllString(value, "$1")
			facts.Make[name] = &cleaned
		} else {
			facts.Make[name] = nil
		}
	}
	return facts, nil
}

// runTool runs one toolchain command and returns what it printed (output, then errors).
func runTool(ctx context.Context, tool []string, dir string, env []string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, toolchainBudget)
	defer cancel()
	captured, err := supervise.Capture(bounded, tool, dir, env, nil)
	switch {
	case err != nil:
		return "", noKey("the toolchain command %s could not be run: %v", strings.Join(tool, " "), err)
	case captured.Status != 0:
		return "", noKey("the toolchain command %s failed (%d)", strings.Join(tool, " "), captured.Status)
	case captured.Incomplete != "":
		return "", noKey("the toolchain command %s gave incomplete output: %s", strings.Join(tool, " "), captured.Incomplete)
	}
	return string(captured.Stdout) + string(captured.Stderr), nil
}

// machine is the machine's kind (arm64, x86_64).
func machine() string {
	var names unix.Utsname
	if err := unix.Uname(&names); err == nil {
		return unix.ByteSliceToString(names.Machine[:])
	}
	return runtime.GOARCH
}

// CommandEntry is the receipts.commands entry for a command: one whose `argv` is exactly its
// words, else the one with the longest `argv_prefix` its words begin with; nil without either.
func CommandEntry(section map[string]any, command []string) map[string]any {
	entries, _ := section["commands"].([]any)
	var best map[string]any
	bestLength := 0
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		if argv, has := entry["argv"]; has && slices.Equal(texts(argv), command) {
			return entry
		}
	}
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		prefix := texts(entry["argv_prefix"])
		if len(prefix) > 0 && len(prefix) <= len(command) && slices.Equal(command[:len(prefix)], prefix) && len(prefix) > bestLength {
			best, bestLength = entry, len(prefix)
		}
	}
	return best
}
