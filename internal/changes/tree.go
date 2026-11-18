package changes

import (
	"crypto/sha1" // #nosec G505 -- git's object ids, not a security hash
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Git file modes as git writes them.
const (
	ModeFile       = "100644"
	ModeExecutable = "100755"
	ModeLink       = "120000"
)

// File is one file of a tree: its path from the repository's root, its git mode and blob id.
type File struct {
	Path string
	Mode string
	Blob string
}

// Regular says whether the file is a regular file (not a link, not a submodule).
func (file File) Regular() bool { return file.Mode == ModeFile || file.Mode == ModeExecutable }

// Tree is a set of files jackstay reads entries from: the working tree, or a commit's tree.
type Tree interface {
	// List is the files directly inside dir (a path from the root), sorted by name; none when
	// dir does not exist.
	List(dir string) ([]File, error)
	// Read is a file's content; exists is false when there is no such file.
	Read(filePath string) (data []byte, exists bool, err error)
}

// BlobID is git's id of a blob with this content, for a repository of the given object format
// ("sha1" or "sha256").
func BlobID(format string, data []byte) string {
	header := fmt.Sprintf("blob %d\x00", len(data))
	if format == "sha256" {
		sum := sha256.Sum256(append([]byte(header), data...))
		return hex.EncodeToString(sum[:])
	}
	sum := sha1.Sum(append([]byte(header), data...)) // #nosec G401 -- git's object id
	return hex.EncodeToString(sum[:])
}

// Worktree is the files of a checkout on disk. Format is the repository's object format, for
// the blob ids it reports.
type Worktree struct {
	Root   string
	Format string
}

// List implements Tree. A symbolic link on the folder's path is refused: entries are read and
// removed only inside the repository's own folders.
func (worktree Worktree) List(dir string) ([]File, error) {
	if _, err := InRepo(worktree.Root, path.Join(dir, "x")); err != nil {
		return nil, err
	}
	names, err := os.ReadDir(filepath.Join(worktree.Root, filepath.FromSlash(dir)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []File
	for _, item := range names {
		if item.IsDir() {
			continue
		}
		filePath := path.Join(dir, item.Name())
		info, err := os.Lstat(filepath.Join(worktree.Root, filepath.FromSlash(filePath)))
		if err != nil {
			return nil, err
		}
		file := File{Path: filePath, Mode: ModeFile}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			file.Mode = ModeLink
			target, err := os.Readlink(filepath.Join(worktree.Root, filepath.FromSlash(filePath)))
			if err != nil {
				return nil, err
			}
			file.Blob = BlobID(worktree.Format, []byte(target))
		case !info.Mode().IsRegular():
			continue
		default:
			if info.Mode()&0o111 != 0 {
				file.Mode = ModeExecutable
			}
			data, err := os.ReadFile(filepath.Join(worktree.Root, filepath.FromSlash(filePath)))
			if err != nil {
				return nil, err
			}
			file.Blob = BlobID(worktree.Format, data)
		}
		files = append(files, file)
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return files, nil
}

// Read implements Tree. A link is not followed: its content is its target, as git stores it; a
// link on a folder of the path is refused.
func (worktree Worktree) Read(filePath string) ([]byte, bool, error) {
	if _, err := InRepo(worktree.Root, path.Join(path.Dir(filePath), "x")); err != nil {
		return nil, false, err
	}
	full := filepath.Join(worktree.Root, filepath.FromSlash(filePath))
	info, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		return []byte(target), true, err
	}
	data, err := os.ReadFile(full)
	return data, err == nil, err
}

// Listed is what a folder of entries holds: the entries in name order, the override and the
// request when there, and the other `.md` files that are not entries (a README beside them).
type Listed struct {
	Entries  []Listing
	Override *File
	Request  *File
	Others   []string
}

// Listing is an entry file and what was read from it.
type Listing struct {
	File     File
	Entry    Entry
	Problems []string
	Data     []byte // the bytes the entry was read from
}

// ListEntries reads the entries folder dir of a tree. Which files are entries (docs/design/implementation.md section 4.3):
// `<slug>.md`; the two `_release` files are jackstay's own; any other `.md` is named in Others, and
// every other file is ignored.
func ListEntries(tree Tree, dir string) (Listed, error) {
	files, err := tree.List(dir)
	if err != nil {
		return Listed{}, err
	}
	var listed Listed
	for index := range files {
		file := files[index]
		name := path.Base(file.Path)
		switch {
		case name == OverrideName:
			listed.Override = &file
		case name == RequestName:
			listed.Request = &file
		case strings.HasSuffix(name, ".md") && IsSlug(strings.TrimSuffix(name, ".md")):
			data, _, err := tree.Read(file.Path)
			if err != nil {
				return Listed{}, err
			}
			entry, problems := Parse(name, data)
			listed.Entries = append(listed.Entries, Listing{File: file, Entry: entry, Problems: problems, Data: data})
		case strings.HasSuffix(name, ".md") && !strings.HasPrefix(name, "."):
			listed.Others = append(listed.Others, name)
		}
	}
	return listed, nil
}
