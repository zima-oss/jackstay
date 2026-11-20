package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/adopt/jsondoc"
	"github.com/zima-oss/jackstay/internal/homedir"
)

// sandboxChange is what the sandbox repair does to sandbox.filesystem.allowWrite.
type sandboxChange struct {
	// Removed are entries that name the jackstay home itself (an earlier setup wrote one): they let
	// the sandbox write evidence (#59).
	Removed []string
	// Added are the grants allowWrite did not cover yet, in SandboxGrants' order.
	Added []string
	// Kept are other entries that still let the sandbox write evidence; setup never narrows an
	// entry it did not write, so the owner does.
	Kept []string
}

// errNothingToChange says allowWrite already covers every grant and names no jackstay home.
var errNothingToChange = errors.New("nothing to change")

// prepareSandbox plans the repair of the account's sandbox settings: it removes allowWrite
// entries naming the jackstay home itself and adds each grant not yet covered. Every other key and
// value stays, in its order (the ordered model adopt edits settings with). A deny entry is an
// owner decision, never something setup removes or overrides.
func prepareSandbox(settingsPath, stateHome string, grants []string) ([]byte, []byte, os.FileMode, sandboxChange, error) {
	var change sandboxChange
	original, mode, err := readProfile(settingsPath)
	if err != nil {
		return nil, nil, 0, change, err
	}
	if len(original) == 0 {
		return nil, nil, 0, change, fmt.Errorf("settings file is absent or empty")
	}
	parsed, err := jsondoc.Parse(original)
	settings, isObject := parsed.(*jsondoc.Object)
	if err != nil || !isObject {
		return nil, nil, 0, change, fmt.Errorf("settings is not a single JSON object with unique keys")
	}
	sandbox := settings.Object("sandbox")
	if sandbox == nil {
		return nil, nil, 0, change, fmt.Errorf("sandbox is not a JSON object")
	}
	if enabled, _ := sandbox.Get("enabled"); enabled != true {
		return nil, nil, 0, change, fmt.Errorf("sandbox is not explicitly enabled")
	}
	filesystem := sandbox.Object("filesystem")
	if raw, exists := sandbox.Get("filesystem"); exists && filesystem == nil {
		return nil, nil, 0, change, fmt.Errorf("sandbox.filesystem is not a JSON object (%v)", raw)
	}
	if filesystem == nil {
		filesystem = jsondoc.NewObject()
	}
	denied, err := textList(filesystem, "denyWrite")
	if err != nil {
		return nil, nil, 0, change, err
	}
	for _, grant := range grants {
		real := realPath(grant)
		if entry := covering(denied, real); entry != "" {
			return nil, nil, 0, change, fmt.Errorf("denyWrite entry %q covers %s; review that entry yourself", entry, grant)
		}
		if entry := inside(denied, real); entry != "" {
			return nil, nil, 0, change, fmt.Errorf("denyWrite entry %q is inside %s; review that entry yourself", entry, grant)
		}
	}
	allowed, err := textList(filesystem, "allowWrite")
	if err != nil {
		return nil, nil, 0, change, err
	}
	home := realPath(stateHome)
	kept := []any{}
	for _, raw := range allowed {
		entry := raw.(string)
		if namesFolder(entry, home) {
			change.Removed = append(change.Removed, entry)
			continue
		}
		kept = append(kept, entry)
	}
	for _, grant := range grants {
		if covering(kept, realPath(grant)) == "" {
			entry := accountRelative(grant)
			change.Added = append(change.Added, entry)
			kept = append(kept, entry)
		}
	}
	for _, evidence := range sandboxEvidence(stateHome, nil) {
		if entry := covering(kept, realPath(evidence)); entry != "" && covering(denied, realPath(evidence)) == "" {
			change.Kept = appendOnce(change.Kept, entry)
		}
	}
	if len(change.Removed) == 0 && len(change.Added) == 0 {
		return original, original, mode, change, errNothingToChange
	}
	filesystem.Set("allowWrite", kept)
	sandbox.Set("filesystem", filesystem)
	return original, jsondoc.Format(settings), mode, change, nil
}

// textList is the list of strings at key (none when it is absent).
func textList(object *jsondoc.Object, key string) ([]any, error) {
	raw, exists := object.Get(key)
	if !exists {
		return nil, nil
	}
	list, isList := raw.([]any)
	if !isList {
		return nil, fmt.Errorf("sandbox.filesystem.%s is not a list", key)
	}
	for _, entry := range list {
		if _, isText := entry.(string); !isText {
			return nil, fmt.Errorf("sandbox.filesystem.%s has a non-string entry", key)
		}
	}
	return list, nil
}

// namesFolder says the entry is the folder itself, or the folder with `/**`.
func namesFolder(entry, folder string) bool {
	trimmed := strings.TrimSuffix(strings.TrimSuffix(homedir.Expand(entry), "/**"), "/")
	return filepath.IsAbs(trimmed) && !strings.ContainsAny(trimmed, "*?[") && realPath(trimmed) == folder
}

// accountRelative writes a path under the account's home as `~/…`, as people write these
// entries; Claude Code expands it the same way doctor does (homedir.Expand).
func accountRelative(path string) string {
	account := homedir.AccountHome()
	if rest, found := strings.CutPrefix(path, account+string(filepath.Separator)); found && account != "" {
		return "~/" + rest
	}
	return path
}

func appendOnce(list []string, item string) []string {
	for _, present := range list {
		if present == item {
			return list
		}
	}
	return append(list, item)
}

// sandboxDiff shows only the changed allowWrite entries, so unrelated settings and possible
// private values are not copied into terminal logs or the JSON plan.
func sandboxDiff(path string, change sandboxChange) string {
	var diff strings.Builder
	fmt.Fprintf(&diff, "  --- %s\n  +++ %s\n  @@ sandbox.filesystem.allowWrite @@\n", path, path)
	for _, entry := range change.Removed {
		fmt.Fprintf(&diff, "  -%s\n", strconv.Quote(entry))
	}
	for _, entry := range change.Added {
		fmt.Fprintf(&diff, "  +%s\n", strconv.Quote(entry))
	}
	return diff.String()
}

// makeGrants makes each grant before the settings name it (a folder, or the lock file empty),
// so a sandbox that binds paths finds them in place.
func makeGrants(grants []string) error {
	for _, grant := range grants {
		if strings.HasSuffix(grant, ".lock") {
			if err := os.MkdirAll(filepath.Dir(grant), 0o700); err != nil {
				return err
			}
			file, err := os.OpenFile(grant, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- jackstay's own lock under its home
			if err != nil {
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(grant, 0o700); err != nil {
			return err
		}
	}
	return nil
}
