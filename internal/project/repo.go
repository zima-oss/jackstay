package project

import (
	"errors"
	"io/fs"
	"os"
)

// Repo is one checkout's files (a repository's root, or a worktree's folder), as jackstay reads and
// creates them: every path is written from Root and goes through Inside first, so no command can
// forget that a path jackstay.json or the tree names must stay inside the repository. It is the only way
// the git-and-files commands (cards, lint, id, new, view, changes) touch a project's files.
type Repo struct {
	Root string
}

// Path is relative as a real path inside the repository, or an error naming where it leads.
func (repo Repo) Path(relative string) (string, error) {
	return Inside(repo.Root, relative)
}

// ReadFile reads a file of the repository.
func (repo Repo) ReadFile(relative string) ([]byte, error) {
	place, err := repo.Path(relative)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(place) // #nosec G304 -- a path inside the repository
}

// ReadDir lists a folder of the repository; a folder that is not there lists nothing.
func (repo Repo) ReadDir(relative string) ([]os.DirEntry, error) {
	place, err := repo.Path(relative)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(place)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return entries, err
}

// IsDir says whether relative is a folder inside the repository.
func (repo Repo) IsDir(relative string) bool {
	place, err := repo.Path(relative)
	if err != nil {
		return false
	}
	info, err := os.Stat(place)
	return err == nil && info.IsDir()
}

// IsFile says whether relative is a regular file inside the repository.
func (repo Repo) IsFile(relative string) bool {
	place, err := repo.Path(relative)
	if err != nil {
		return false
	}
	info, err := os.Stat(place)
	return err == nil && info.Mode().IsRegular()
}

// Create makes a new file inside the repository (its folder must exist) and writes data to it;
// fs.ErrExist when one of that name is there. A file not written whole is removed.
func (repo Repo) Create(relative string, data []byte) error {
	place, err := repo.Path(relative)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(place, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302 G304 -- a project file, read by everyone
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(place)
		return errors.Join(writeErr, closeErr)
	}
	return nil
}

// MkdirAll makes a folder inside the repository.
func (repo Repo) MkdirAll(relative string) error {
	place, err := repo.Path(relative)
	if err != nil {
		return err
	}
	return os.MkdirAll(place, 0o750)
}
