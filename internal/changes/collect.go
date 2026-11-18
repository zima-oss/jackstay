package changes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
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
	unlock, err := lockCollect(project)
	if err != nil {
		return report(stderr, "changes collect", err)
	}
	defer unlock()
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
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- git's own folder
	if err != nil {
		return nil, &Problem{Code: 2, Message: "the collectors' lock cannot be opened: " + err.Error()}
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil { // #nosec G115 -- a descriptor fits an int
		_ = file.Close()
		return nil, &Problem{Code: 2, Message: "the collectors' lock: " + err.Error()}
	}
	return func() { _ = file.Close() }, nil
}

// Collect is `jst changes collect`: write the section of every entry under the version's heading into
// `changes.into`, then remove the entries. A rerun after a stop finishes the first run.
func Collect(project Project, version, date string, dryRun bool, stdout, stderr io.Writer) (int, error) {
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
	read := map[string][]byte{}
	var invalid []string
	for _, listing := range listed.Entries {
		for _, problem := range EntryProblems(listing, settings) {
			invalid = append(invalid, listing.File.Path+": "+problem)
		}
		entries = append(entries, listing.Entry)
		read[listing.File.Path] = listing.Data // what was rendered is what may be removed
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
	if existing := VersionHeading(text, settings.Heading, version); existing != "" {
		return finishRerun(project, listed, text, existing, dryRun, stdout, stderr)
	}
	if len(entries) == 0 {
		fmt.Fprintf(stderr, "jst changes: no entries in %s: nothing to collect\n", settings.Dir)
		return 1, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The paths collect writes and removes are clean, so git holds what was there: after a stop,
	// `git restore` (W-047).
	status, err := project.Git.Run(ctx, "status", "--porcelain", "--untracked-files=all", "--", settings.Into, settings.Dir)
	if err != nil {
		return 2, err
	}
	if len(bytes.TrimSpace(status)) > 0 {
		fmt.Fprintf(stderr, "jst changes: %s or %s has uncommitted changes; commit or drop them first\n", settings.Into, settings.Dir)
		return 1, nil
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
	if err := WriteInRepo(project.Root, settings.Into, []byte(Insert(text, section, settings.Newest, HeadingPattern(settings.Heading)))); err != nil {
		return 2, err
	}
	var removed, changed []string
	for _, listing := range listed.Entries {
		now, _, err := worktree.Read(listing.File.Path)
		if err != nil || !bytes.Equal(now, read[listing.File.Path]) {
			changed = append(changed, listing.File.Path)
			continue
		}
		removed = append(removed, listing.File.Path)
	}
	if err := Remove(project, removed); err != nil {
		return 2, err
	}
	fmt.Fprint(stdout, section)
	fmt.Fprintf(stdout, "\njst changes: wrote `%s` into %s and removed %d entr%s\n", heading, settings.Into, len(removed), plural(len(removed), "y", "ies"))
	if len(changed) > 0 {
		fmt.Fprintf(stderr, "jst changes: kept %s: changed while being collected; its new text is not in %s (put it there by hand, then remove the entry)\n",
			strings.Join(changed, ", "), settings.Into)
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

// finishRerun removes the entries when every one is already a block of the version's section:
// the first run wrote the changelog and stopped before removing them.
func finishRerun(project Project, listed Listed, text, heading string, dryRun bool, stdout, stderr io.Writer) (int, error) {
	lines := strings.Split(text, "\n")
	anyHeading := HeadingPattern(project.Settings.Heading)
	start := 0
	for index, line := range lines {
		if strings.TrimRight(line, " \t") == heading {
			start = index
			break
		}
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		if anyHeading.MatchString(lines[index]) {
			end = index
			break
		}
	}
	own := strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n")
	var done, missing []string
	for _, listing := range listed.Entries {
		held := holdsBlock(own, listing.Entry.Text)
		if listing.Entry.Typed {
			held = holdsLines(own, item(listing.Entry))
		}
		if held {
			done = append(done, listing.File.Path)
		} else {
			missing = append(missing, listing.File.Path)
		}
	}
	if len(listed.Entries) > 0 && len(missing) == 0 {
		if !dryRun {
			// Each entry is removed only if it is still the bytes that were found in the section.
			var unchanged, changed []string
			for _, listing := range listed.Entries {
				now, _, err := project.Worktree().Read(listing.File.Path)
				if err != nil || !bytes.Equal(now, listing.Data) {
					changed = append(changed, listing.File.Path)
					continue
				}
				unchanged = append(unchanged, listing.File.Path)
			}
			if err := Remove(project, unchanged); err != nil {
				return 2, err
			}
			if len(changed) > 0 {
				fmt.Fprintf(stderr, "jst changes: kept %s: changed since it was read\n", strings.Join(changed, ", "))
				return 1, nil
			}
			done = unchanged
		}
		suffix := ""
		if dryRun {
			suffix = " (dry run: nothing removed)"
		}
		fmt.Fprintf(stdout, "%s is in %s already; removed the %d entr%s it holds%s\n", heading, project.Settings.Into, len(done), plural(len(done), "y", "ies"), suffix)
		return 0, nil
	}
	message := fmt.Sprintf("jst changes: %s has `%s` already", project.Settings.Into, heading)
	if len(missing) > 0 {
		message += ", and entries not in it: " + strings.Join(missing, ", ")
	}
	fmt.Fprintln(stderr, message)
	return 1, nil
}

// holdsBlock says whether block is a whole block of section: after a blank line or the start,
// before a blank line or the end.
func holdsBlock(section, block string) bool {
	pattern := regexp.MustCompile(`(?:^|\n\n)` + regexp.QuoteMeta(strings.TrimSpace(block)) + `(?:\n\n|$)`)
	return pattern.MatchString(section)
}

// holdsLines says whether lines are whole lines of section (typed items follow each other without
// a blank line between them).
func holdsLines(section, lines string) bool {
	pattern := regexp.MustCompile(`(?:^|\n)` + regexp.QuoteMeta(lines) + `(?:\n|$)`)
	return pattern.MatchString(section)
}

// Remove deletes files of the project: tracked ones with `git rm`, the others from disk.
func Remove(project Project, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, filePath := range paths {
		if _, err := InRepo(project.Root, filePath); err != nil {
			return err
		}
	}
	tracked := map[string]bool{}
	out, err := project.Git.Run(ctx, append([]string{"ls-files", "-z", "--"}, paths...)...)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(string(out), "\x00") {
		if name != "" {
			tracked[name] = true
		}
	}
	var gitPaths []string
	for _, filePath := range paths {
		if tracked[filePath] {
			gitPaths = append(gitPaths, filePath)
			continue
		}
		full, err := InRepo(project.Root, filePath)
		if err != nil {
			return err
		}
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if len(gitPaths) > 0 {
		if _, err := project.Git.Run(ctx, append([]string{"rm", "-q", "--"}, gitPaths...)...); err != nil {
			return err
		}
	}
	return nil
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
