// Package expected is the owner's list of projects whose guard must be heard from
// (`~/.jackstay/.all/expected-projects`, one marker name per line; docs/design/implementation.md section 7.7).
// It is changed under flock on `expected-projects.lock` and
// replaced by an atomic write, so two changes at once each keep the other's.
package expected

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/homedir"
)

// FileName is the list's name under `<jackstay home>/.all`.
const FileName = "expected-projects"

// Path is the list's path.
func Path() (string, error) { return homedir.SharedPath(FileName) }

// Read is the list, sorted, each name once. No list is no project expected; a list that exists
// and cannot be read whole is an error, never an empty list.
func Read() ([]string, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	return readFile(path)
}

func readFile(path string) ([]string, error) {
	file, err := os.Open(path) // #nosec G304 -- jackstay's own file under its home
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	seen := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			seen[line] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return sorted(seen), nil
}

// Change reads the list under its lock, applies change to the set of names and writes the result
// back; it returns the new list.
func Change(change func(names map[string]bool)) ([]string, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	held, err := durable.Lock(context.Background(), path)
	if err != nil {
		return nil, err
	}
	defer held.Release()
	current, err := readFile(path)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, name := range current {
		names[name] = true
	}
	change(names)
	list := sorted(names)
	var text strings.Builder
	for _, name := range list {
		text.WriteString(name + "\n")
	}
	return list, durable.WriteAtomic(path, []byte(text.String()), 0o600, 0)
}

func sorted(names map[string]bool) []string {
	list := make([]string, 0, len(names))
	for name := range names {
		list = append(list, name)
	}
	sort.Strings(list)
	return list
}
