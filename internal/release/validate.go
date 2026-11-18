package release

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/zima-oss/jackstay/internal/changes"
)

// Validate is the release rules that need the checkout (docs/design/release.md, section 1): ref
// names as git takes them; outputs that are tracked regular files (a changelog may be absent) with
// no link on their path; each version pattern reading its own raised result; a typed template.
// The rules that need no checkout are SettingsOf's.
func Validate(root string, repo Repo, settings Settings) []string {
	var problems []string
	for _, ref := range []struct{ key, name string }{
		{"release.branch", "refs/heads/" + settings.Branch},
		{"release.base", "refs/heads/" + settings.Base},
		{"release.tag", "refs/tags/" + settings.TagName(settings.First)},
	} {
		if _, err := repo.run("check-ref-format", ref.name); err != nil {
			problems = append(problems, fmt.Sprintf("`%s` gives %s, which git does not take as a ref name", ref.key, ref.name))
		}
	}
	tracked := map[string]string{}
	if out, err := repo.run("ls-files", "-s", "-z"); err == nil {
		for _, record := range strings.Split(string(out), "\x00") {
			meta, filePath, found := strings.Cut(record, "\t")
			if fields := strings.Fields(meta); found && len(fields) == 3 {
				tracked[filePath] = fields[0]
			}
		}
	}
	checkPath := func(key, filePath string, mayBeAbsent bool) {
		if problem := linkOnPath(root, filePath); problem != "" {
			problems = append(problems, fmt.Sprintf("`%s` (%s): %s", key, filePath, problem))
			return
		}
		mode, isTracked := tracked[filePath]
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(filePath)))
		switch {
		case err != nil && mayBeAbsent && !isTracked:
		case err != nil:
			problems = append(problems, fmt.Sprintf("`%s` (%s) is missing", key, filePath))
		case !info.Mode().IsRegular():
			problems = append(problems, fmt.Sprintf("`%s` (%s) is not a regular file", key, filePath))
		case !isTracked || (mode != changes.ModeFile && mode != changes.ModeExecutable):
			problems = append(problems, fmt.Sprintf("`%s` (%s) is not a tracked regular file", key, filePath))
		}
	}
	for index, file := range settings.Files {
		key := fmt.Sprintf("release.files[%d]", index)
		checkPath(key, file.Path, false)
		problems = append(problems, raisedReadBack(root, file, key, raiseVersion)...)
	}
	if settings.Build != nil {
		checkPath("release.build", settings.Build.Path, false)
		problems = append(problems, raisedReadBack(root, *settings.Build, "release.build", raiseBuild)...)
	}
	checkPath("changes.into", settings.Changes.Into, true)
	if settings.Changes.InternalInto != "" {
		checkPath("changes.internal_into", settings.Changes.InternalInto, true)
	}
	if problem := linkOnPath(root, path.Join(settings.Changes.Dir, "x")); problem != "" {
		problems = append(problems, "`changes.dir`: "+problem)
	}
	if settings.Changes.Template != "" {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(settings.Changes.Template)))
		if err != nil {
			problems = append(problems, "`changes.template`: "+err.Error())
		} else if problem := changes.TemplateProblem(data); problem != "" {
			problems = append(problems, "`changes.template`: "+problem)
		}
	}
	return problems
}

// linkOnPath says whether a folder on filePath (or the file itself) is a symbolic link.
func linkOnPath(root, filePath string) string {
	current := root
	for _, part := range strings.Split(path.Clean(filePath), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return ""
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "a symbolic link is on its path"
		}
	}
	return ""
}

// raisedReadBack tries the pattern on its file's current value raised by one: a pattern that
// would not read its own result is refused before any release.
func raisedReadBack(root string, file VersionFile, key string, raise func(string) string) []string {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file.Path)))
	if err != nil {
		return nil // reported as missing above
	}
	held, err := file.captures(data)
	if err != nil {
		return []string{fmt.Sprintf("`%s`: %v", key, err)}
	}
	if _, err := file.replace(data, raise(held)); err != nil {
		return []string{fmt.Sprintf("`%s`: %v", key, err)}
	}
	return nil
}

func raiseVersion(held string) string {
	version, err := ParseVersion(held)
	if err != nil {
		return held + ".1"
	}
	return fmt.Sprintf("%d.%d.%d", version.Major+1, version.Minor+1, version.Patch+1)
}

func raiseBuild(held string) string {
	var number uint64
	if _, err := fmt.Sscan(held, &number); err != nil {
		return held + "1"
	}
	return fmt.Sprint(number + 1)
}
