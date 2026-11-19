package changes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/delivery"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

func runCollect(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := newFlags("changes collect", stderr)
	version := flags.String("version", "", "the version of the section")
	date := flags.String("date", "", "the date in the heading (default today)")
	dryRun := flags.Bool("dry-run", false, "print the section and change nothing")
	if flags.Parse(args) != nil || flags.NArg() > 0 || *version == "" {
		fmt.Fprintln(stderr, "usage: jst changes collect --version V [--date D] [--dry-run]")
		return 2
	}
	project, err := LoadProject()
	if err != nil {
		return report(stderr, "changes collect", err)
	}
	if project.Settings.Release {
		fmt.Fprintln(stderr, "jst changes collect: this project has a `release` section: versions are written by `jst release prepare`, through the release pull request")
		return 2
	}
	if *date == "" {
		*date = time.Now().Format("2006-01-02") // the default of a command run by hand
	}
	code, err := Collect(project, *version, *date, *dryRun, stdout, stderr)
	if err != nil {
		return report(stderr, "changes collect", err)
	}
	return code
}

// lockCollect holds a lock in the repository's git folder so two collects in one checkout do not
// both write the changelog (docs/design/implementation.md section 4.3).
func lockCollect(project Project) (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lockPath := filepath.Join(project.Root, ".jackstay-changes.lock")
	if gitPath, err := project.Git.Line(ctx, "rev-parse", "--git-path", "jackstay-changes.lock"); err == nil && gitPath != "" {
		lockPath = gitPath
		if !filepath.IsAbs(lockPath) {
			lockPath = filepath.Join(project.Root, lockPath)
		}
	}
	held, err := flock.WaitFile(ctx, lockPath)
	if err != nil {
		return nil, &Problem{Code: 2, Message: "the collectors' lock: " + err.Error()}
	}
	return held.Release, nil
}

// Collect is `jst changes collect`: write the section of every entry under the version's heading into
// `changes.into`, then remove the entries. A rerun after a stop finishes the first run.
func Collect(project Project, version, date string, dryRun bool, stdout, stderr io.Writer) (int, error) {
	if !dryRun {
		unlock, err := lockCollect(project)
		if err != nil {
			return 2, err
		}
		defer unlock()
	}
	settings := project.Settings
	worktree := project.Worktree()
	listed, err := ListEntries(worktree, settings.Dir)
	if err != nil {
		return 2, err
	}
	for _, other := range listed.Others {
		fmt.Fprintf(stderr, "jst changes: %s is not an entry (an entry is `<slug>.md`, as `jst changes new` writes it); left as it is\n", other)
	}
	entries := make([]Entry, 0, len(listed.Entries))
	files := make([]File, 0, len(listed.Entries))
	var invalid []string
	for _, listing := range listed.Entries {
		for _, problem := range EntryProblems(listing, settings) {
			invalid = append(invalid, listing.File.Path+": "+problem)
		}
		entries = append(entries, listing.Entry)
		file := listing.File
		file.Blob = BlobID(project.Format, listing.Data)
		files = append(files, file)
	}
	if len(invalid) > 0 {
		fmt.Fprintf(stderr, "jst changes: invalid entries, nothing collected:\n  %s\n", strings.Join(invalid, "\n  "))
		return 1, nil
	}
	changelog, _, err := worktree.Read(settings.Into)
	if err != nil {
		return 2, err
	}
	text := string(changelog)
	heading := Heading(settings.Heading, version, date)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if existing := VersionHeading(text, settings.Heading, version); existing != "" {
		if dryRun {
			fmt.Fprintf(stderr, "jst changes: %s has %s already; no new section to write\n", settings.Into, existing)
			return 1, nil
		}
		return resumeCollect(ctx, project, version, existing, stdout, stderr)
	}
	if len(entries) == 0 {
		fmt.Fprintf(stderr, "jst changes: no entries in %s: nothing to collect\n", settings.Dir)
		return 1, nil
	}
	head, err := project.Git.Line(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return 2, err
	}
	// The paths collect writes and removes must be clean. Ignored entries do not appear in
	// status, so also require each entry to be in the index before writing the changelog.
	status, err := project.Git.Run(ctx, "status", "--porcelain", "--untracked-files=all", "--", settings.Into, settings.Dir)
	if err != nil {
		return 2, err
	}
	if len(bytes.TrimSpace(status)) > 0 {
		fmt.Fprintf(stderr, "jst changes: %s or %s has uncommitted changes; commit or drop them first\n", settings.Into, settings.Dir)
		return 1, nil
	}
	paths := collectPaths(files)
	tracked, err := trackedPaths(ctx, project, paths)
	if err != nil {
		return 2, err
	}
	for _, name := range paths {
		if !tracked[name] {
			fmt.Fprintf(stderr, "jst changes: %s is not tracked; commit or move it first\n", name)
			return 1, nil
		}
	}
	section := Section(heading, entries)
	if dryRun {
		fmt.Fprint(stdout, section)
		return 0, nil
	}
	if now, _, err := worktree.Read(settings.Into); err != nil || !bytes.Equal(now, changelog) {
		fmt.Fprintf(stderr, "jst changes: %s changed while the section was made; nothing collected\n", settings.Into)
		return 1, nil
	}
	next := []byte(Insert(text, section, settings.Newest, HeadingPattern(settings.Heading)))
	plan := collectPlan{Head: head, Version: version, Destination: settings.Into, Changelog: BlobID(project.Format, next), Files: files}
	planPath, err := collectPlanPath(ctx, project)
	if err != nil {
		return 2, err
	}
	record, err := readCollection(planPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 2, err
	}
	if record == nil || record.Outcome == delivery.Complete || !record.Plan.same(plan) {
		record = delivery.New(plan)
	}
	if err := record.Begin(planPath, "write-changelog"); err != nil {
		return 2, fmt.Errorf("saving the collection plan: %w", err)
	}
	if err := WriteInRepo(project.Root, settings.Into, next); err != nil {
		return collectOutcome(planPath, record, 2, err)
	}
	fmt.Fprint(stdout, section)
	return finishCollect(ctx, project, planPath, record, heading, stdout, stderr)
}

// collectPlan identifies the exact files that produced a changelog, in one worktree at one HEAD.
// Rendered text alone cannot tell an entry from a later file that happens to say the same thing.
type collectPlan struct {
	Head        string
	Version     string
	Destination string
	Changelog   string
	Files       []File
}

func (plan collectPlan) same(other collectPlan) bool {
	return plan.Head == other.Head && plan.Version == other.Version && plan.Destination == other.Destination &&
		plan.Changelog == other.Changelog && slices.Equal(plan.Files, other.Files)
}

func readCollection(planPath string) (*delivery.Record[collectPlan], error) {
	record, legacy, err := delivery.Read[collectPlan](planPath)
	if !errors.Is(err, delivery.ErrUnversioned) {
		return record, err
	}
	var plan collectPlan
	if err := strictjson.Unmarshal(legacy, &plan); err != nil {
		return nil, fmt.Errorf("reading the legacy collection plan: %w", err)
	}
	return delivery.New(plan), nil
}

func collectPlanPath(ctx context.Context, project Project) (string, error) {
	return project.Git.Line(ctx, "rev-parse", "--path-format=absolute", "--git-path", "jackstay-changes.json")
}

func collectPaths(files []File) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func trackedPaths(ctx context.Context, project Project, paths []string) (map[string]bool, error) {
	out, err := project.Git.Run(ctx, append([]string{"ls-files", "-z", "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	tracked := make(map[string]bool, len(paths))
	for _, name := range strings.Split(string(out), "\x00") {
		tracked[name] = true
	}
	return tracked, nil
}

func resumeCollect(ctx context.Context, project Project, version, heading string, stdout, stderr io.Writer) (int, error) {
	planPath, err := collectPlanPath(ctx, project)
	if err != nil {
		return 2, err
	}
	record, err := readCollection(planPath)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stderr, "jst changes: %s has %s already and no saved collection plan; entries were left in place\n", project.Settings.Into, heading)
		return 1, nil
	}
	if err != nil {
		return 2, err
	}
	plan := record.Plan
	if plan.Version != version || plan.Destination != project.Settings.Into || len(plan.Files) == 0 {
		fmt.Fprintf(stderr, "jst changes: the saved collection plan does not match %s; entries were left in place\n", heading)
		return 1, nil
	}
	if record.Outcome == delivery.Complete {
		fmt.Fprintf(stderr, "jst changes: the collection of %s already completed; entries were left in place\n", heading)
		return 1, nil
	}
	if err := record.Begin(planPath, "remove-entries"); err != nil {
		return 2, err
	}
	fmt.Fprintf(stdout, "jst changes: resuming delivery %s (attempt %d)\n", record.ID, record.Attempt)
	return finishCollect(ctx, project, planPath, record, heading, stdout, stderr)
}

func finishCollect(ctx context.Context, project Project, planPath string, record *delivery.Record[collectPlan], heading string, stdout, stderr io.Writer) (int, error) {
	if record.Phase != "remove-entries" {
		if err := record.Checkpoint(planPath, "remove-entries", delivery.Running); err != nil {
			return 2, err
		}
	}
	code, err := removeCollectedEntries(ctx, project, record.Plan, heading, stdout, stderr)
	return collectOutcome(planPath, record, code, err)
}

func collectOutcome(planPath string, record *delivery.Record[collectPlan], code int, err error) (int, error) {
	outcome := delivery.Interrupted
	if code == 0 && err == nil {
		outcome = delivery.Complete
	}
	if checkpointErr := record.Checkpoint(planPath, record.Phase, outcome); checkpointErr != nil {
		return 2, errors.Join(err, fmt.Errorf("recording the collection outcome: %w", checkpointErr))
	}
	return code, err
}

func removeCollectedEntries(ctx context.Context, project Project, plan collectPlan, heading string, stdout, stderr io.Writer) (int, error) {
	onDisk, err := project.Worktree().List(project.Settings.Dir)
	if err != nil {
		return 2, err
	}
	current := make(map[string]File, len(onDisk))
	for _, file := range onDisk {
		current[file.Path] = file
	}
	tracked, err := trackedPaths(ctx, project, collectPaths(plan.Files))
	if err != nil {
		return 2, err
	}
	var remove, changed []string
	for _, expected := range plan.Files {
		if path.Dir(expected.Path) != path.Clean(project.Settings.Dir) || !expected.Regular() {
			return 2, fmt.Errorf("the collection plan names an invalid entry: %q", expected.Path)
		}
		actual, exists := current[expected.Path]
		switch {
		case exists && (actual != expected || !tracked[expected.Path]):
			changed = append(changed, expected.Path)
		case tracked[expected.Path]:
			remove = append(remove, expected.Path)
		}
	}
	head, err := project.Git.Line(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return 2, err
	}
	changelog, _, err := project.Worktree().Read(plan.Destination)
	if err != nil {
		return 2, err
	}
	if head != plan.Head || BlobID(project.Format, changelog) != plan.Changelog {
		fmt.Fprintln(stderr, "jst changes: HEAD or the changelog differs from the saved collection plan; entries were left in place")
		return 1, nil
	}
	if len(remove) > 0 {
		// git rm also verifies the disk and index against HEAD and never removes untracked files.
		if _, err := project.Git.Run(ctx, append([]string{"rm", "-q", "--"}, remove...)...); err != nil {
			return 2, err
		}
	}
	fmt.Fprintf(stdout, "\njst changes: %s is in %s; removed %d entr%s\n", heading, plan.Destination, len(remove), plural(len(remove), "y", "ies"))
	if len(changed) > 0 {
		fmt.Fprintf(stderr, "jst changes: kept %s: changed while being collected; check their current text against %s\n",
			strings.Join(changed, ", "), plan.Destination)
		return 1, nil
	}
	return 0, nil
}

// VersionHeading is the line of the changelog that is this version's heading, with any date.
func VersionHeading(text, template, version string) string {
	var pattern strings.Builder
	pattern.WriteString("^")
	rest := template
	for rest != "" {
		next, placeholder := len(rest), ""
		for _, candidate := range []string{"{version}", "{date}"} {
			if at := strings.Index(rest, candidate); at >= 0 && at < next {
				next, placeholder = at, candidate
			}
		}
		pattern.WriteString(regexp.QuoteMeta(rest[:next]))
		switch placeholder {
		case "{version}":
			pattern.WriteString(regexp.QuoteMeta(version))
		case "{date}":
			pattern.WriteString(".+")
		default:
			rest = ""
			continue
		}
		rest = rest[next+len(placeholder):]
	}
	pattern.WriteString(`\s*$`)
	matcher := regexp.MustCompile(pattern.String())
	for _, line := range strings.Split(text, "\n") {
		if matcher.MatchString(line) {
			return strings.TrimRight(line, " \t")
		}
	}
	return ""
}

// InRepo is the absolute path of a path of the repository, refused when it leaves the repository
// or when a symbolic link is on its way (a folder or the file itself): jackstay writes only the named file.
func InRepo(root, relative string) (string, error) {
	clean := path.Clean(filepath.ToSlash(relative))
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", &Problem{Code: 2, Message: relative + " is not a path inside the repository"}
	}
	current := root
	for _, part := range strings.Split(clean, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			break // the rest does not exist yet
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", &Problem{Code: 2, Message: relative + ": a symbolic link is on its path; jackstay writes only inside the repository's own folders"}
		}
	}
	return filepath.Join(root, filepath.FromSlash(clean)), nil
}

// WriteInRepo writes a file of the repository atomically, after InRepo's check.
func WriteInRepo(root, relative string, data []byte) error {
	target, err := InRepo(root, relative)
	if err != nil {
		return err
	}
	return WriteFileAtomic(target, data)
}

// WriteFileAtomic writes data to a temporary file in the same folder and renames it into place,
// keeping an existing file's permissions.
func WriteFileAtomic(target string, data []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- a folder of the project's own tree
		return err
	}
	temporary, err := os.CreateTemp(dir, ".jackstay-write-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		cleanup()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(name, mode); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, target); err != nil {
		cleanup()
		return err
	}
	return nil
}
