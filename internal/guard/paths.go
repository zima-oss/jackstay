package guard

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// under says whether `inner` is strictly inside `folder`.
func under(inner, folder string) bool {
	folder = strings.TrimRight(folder, "/")
	return strings.HasPrefix(inner, folder+"/") && inner != folder
}

func atOrUnder(inner, folder string) bool {
	return inner == strings.TrimRight(folder, "/") || under(inner, folder)
}

// insideTemporary says whether a path is strictly inside a temporary folder (never one itself).
// /var/folders/xx/yyyy/T is a person's whole temporary folder: only what is inside one counts.
func (reader *reader) insideTemporary(target string) bool {
	for _, root := range reader.exact {
		if target == root {
			return false
		}
	}
	for _, root := range append(append([]string{}, temporaryRoots...), reader.exact...) {
		if under(target, root) {
			return true
		}
	}
	for _, root := range deepRoots {
		if under(target, root) && len(strings.Split(target[len(root)+1:], "/")) >= 4 {
			return true
		}
	}
	return false
}

var namesSomething = regexp.MustCompile(`[A-Za-z0-9]`)
var patternParts = regexp.MustCompile(`\[[^\]]*\]|[*?]`)

// named says whether one name of a path names something: `*`, `?*` and `.*` name everything.
func named(part string) bool {
	return namesSomething.MatchString(patternParts.ReplaceAllString(part, ""))
}

// ownInTemporary is false for everything right inside a temporary folder every session shares
// (`/tmp/*`, `"$TMPDIR"/*`): other sessions' scratch. A named folder in one is one's own.
func (reader *reader) ownInTemporary(target string) bool {
	for _, root := range reader.shared {
		root = strings.TrimRight(root, "/")
		if under(target, root) {
			first, _, _ := strings.Cut(target[len(root)+1:], "/")
			if !named(first) {
				return false
			}
		}
	}
	for _, root := range deepRoots {
		if under(target, root) {
			parts := strings.Split(target[len(root)+1:], "/")
			if len(parts) >= 4 && !named(parts[3]) {
				return false
			}
		}
	}
	return true
}

// settled is where deleting a path really deletes: links the line made, then links on disk, are
// followed. `into` says the path is entered (a trailing slash), so a link at its end is followed
// too. "" when a link leads somewhere that cannot be named.
func (reader *reader) settled(target string, into bool, links map[string]*string) string {
	target = flat(target)
	for range 4 {
		moved := false
		for link, leads := range links {
			if (target == link && into) || under(target, link) {
				if leads == nil {
					return ""
				}
				target = flat(*leads + target[len(link):])
				moved = true
				break
			}
		}
		if !moved {
			break
		}
	}
	if into {
		return realPathOfExisting(target)
	}
	return flat(path.Join(realPathOfExisting(path.Dir(target)), path.Base(target)))
}

// realPathOfExisting follows the links of the longest part of a path that exists.
func realPathOfExisting(target string) string {
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		return flat(resolved)
	}
	parent := path.Dir(target)
	if parent == target {
		return target
	}
	return flat(path.Join(realPathOfExisting(parent), path.Base(target)))
}

// regenerableName says whether one name of a path is a folder that is made again. A name the
// project declared counts only strictly inside the project's checkout (A13); `where` "" is a
// place that cannot be named.
func (reader *reader) regenerableName(name, where string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, pattern := range reader.regenerable {
		if matchName(pattern, name) {
			return true
		}
	}
	declared := false
	for _, pattern := range reader.declared {
		if matchName(pattern, name) {
			declared = true
		}
	}
	if !declared {
		return false
	}
	if where == "" {
		return true
	}
	return reader.projectRoot != "" && under(where, reader.projectRoot)
}

// disposableAt says whether removing an absolute path loses nothing.
func (reader *reader) disposableAt(target string, into bool, here *state) bool {
	lexical := flat(target)
	real := reader.settled(lexical, into, here.links)
	if real == "" {
		return false
	}
	roots := append(append(append([]string{}, temporaryRoots...), reader.exact...), deepRoots...)
	for _, form := range []string{lexical, real} {
		for _, root := range roots {
			if atOrUnder(root, form) {
				return false // a temporary folder itself, or something above one
			}
		}
	}
	if reader.insideTemporary(real) && reader.ownInTemporary(lexical) && reader.ownInTemporary(real) {
		// Under a temporary folder only the session's own goes: its scratch folder, or a folder
		// mktemp made in this command; /tmp and $TMPDIR are every session's (A3).
		own := under(lexical, freshFolder)
		if !own {
			inScratch := func(form string) bool {
				for _, root := range reader.scratch {
					if under(form, root) {
						return true
					}
				}
				return false
			}
			own = inScratch(lexical) && inScratch(real)
		}
		if !own {
			return false
		}
		found := reader.checkoutAround(real)
		return found == "" || reader.throwaway(found, here)
	}
	if into && strings.ContainsAny(lexical, "*?[") {
		return false // entered, and which folders it is cannot be said: one may be a link out
	}
	parts := strings.Split(real, "/")
	for index, part := range parts {
		if reader.regenerableName(part, strings.Join(parts[:index+1], "/")) {
			return true
		}
	}
	return false
}

// checkoutAround is the git checkout a path is in (a `.git` at it or above it inside the temporary
// folder), or one within two levels under it; "" when there is none.
func (reader *reader) checkoutAround(target string) string {
	if under(target, freshFolder) {
		return ""
	}
	for folder := target; ; {
		if fileExists(path.Join(folder, ".git")) {
			return folder
		}
		parent := path.Dir(folder)
		if parent == folder || !reader.insideTemporary(parent) {
			break
		}
		folder = parent
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "" // the link itself goes: nothing under it is touched
	}
	level := []string{target}
	for range 2 {
		var following []string
		for _, folder := range level {
			entries, err := os.ReadDir(folder)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				child := path.Join(folder, entry.Name())
				if fileExists(path.Join(child, ".git")) {
					return child
				}
				following = append(following, child)
			}
		}
		level = following
	}
	return ""
}

// throwaway says whether a checkout is one the command went to in a temporary folder of the
// session's own (a clone mktemp made, a scratch repository): what is uncommitted there is
// disposable. The folder the session runs in is never one.
func (reader *reader) throwaway(folder string, here *state) bool {
	if folder == "" {
		return false
	}
	real := reader.settled(folder, true, here.links)
	if real == "" || !reader.insideTemporary(real) {
		return false
	}
	for _, origin := range reader.origins {
		if atOrUnder(real, origin) || under(origin, real) {
			return false
		}
	}
	if under(real, freshFolder) {
		return true
	}
	var own []string
	for _, root := range reader.scratch {
		if under(real, root) {
			own = append(own, root)
		}
	}
	if len(own) == 0 {
		return false
	}
	top, common, ok := reader.questions.checkout(real)
	if !ok {
		return false
	}
	inOwn := func(target string) bool {
		for _, root := range own {
			if under(target, root) {
				return true
			}
		}
		return false
	}
	if !inOwn(common) {
		return false // a linked worktree of a repository elsewhere
	}
	for _, root := range own {
		if top == root {
			return false
		}
	}
	return inOwn(top)
}

// disposable says whether deleting a path and what is under it loses nothing, wherever the
// command may be running. Where the place is not known, only a path whose last name is
// regenerable and that is not entered passes: `rm -r x/.build` on a link removes the link.
func (reader *reader) disposable(target string, here *state) bool {
	into := strings.HasSuffix(target, "/") || strings.HasSuffix(target, "/.")
	if target == "" {
		return false
	}
	if path.IsAbs(target) {
		return reader.disposableAt(target, into, here)
	}
	if here.places == nil {
		parts := strings.Split(path.Clean(target), "/")
		for _, part := range parts {
			if part == ".." {
				return false
			}
		}
		return !into && reader.regenerableName(parts[len(parts)-1], "")
	}
	for _, folder := range here.places {
		if !reader.disposableAt(path.Join(folder, target)+map[bool]string{true: "/", false: ""}[into], into, here) {
			return false
		}
	}
	return true
}

// regenerableTail says whether a word whose start cannot be named (`"$ROOT/.build"`) still ends
// in a regenerable name written out and not entered: every unknown part in quotes, and the text
// after the last one `/NAME`.
func (reader *reader) regenerableTail(one arg) bool {
	if one.opaque || one.braced || one.spreads || len(one.parts) == 0 {
		return false
	}
	last := -1
	for index, part := range one.parts {
		if !part.known {
			if !part.quoted {
				return false
			}
			last = index
		}
	}
	var tail strings.Builder
	for _, part := range one.parts[last+1:] {
		if part.glob && !part.quoted {
			return false
		}
		tail.WriteString(part.text)
	}
	text := tail.String()
	cut := strings.LastIndexByte(text, '/')
	if cut < 0 || last < 0 {
		return false
	}
	return reader.regenerableName(text[cut+1:], "")
}
