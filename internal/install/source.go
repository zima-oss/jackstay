package install

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zima-oss/jackstay/internal/gitcli"
)

// extractCommit materializes only the tracked files of the pinned commit into a private folder.
// Git's archive takes attributes from the commit, not from concurrent edits to the checkout.
func extractCommit(ctx context.Context, checkout, commit, tree string) error {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	git := gitcli.Git{Dir: checkout}
	data, err := git.Run(bounded, "archive", "--format=tar", commit)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(tree)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	archive := tar.NewReader(bytes.NewReader(data))
	for {
		entry, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Typeflag == tar.TypeXGlobalHeader {
			continue // Git records the commit ID in the global PAX header.
		}
		if err := extractEntry(root, archive, entry); err != nil {
			return fmt.Errorf("%s: %w", entry.Name, err)
		}
	}
}

func extractEntry(root *os.Root, archive *tar.Reader, entry *tar.Header) error {
	if entry.Typeflag == tar.TypeDir {
		return root.MkdirAll(entry.Name, 0o700)
	}
	if err := root.MkdirAll(filepath.Dir(entry.Name), 0o700); err != nil {
		return err
	}
	switch entry.Typeflag {
	case tar.TypeReg:
		mode := os.FileMode(0o600)
		if entry.Mode&0o100 != 0 {
			mode |= 0o100
		}
		file, err := root.OpenFile(entry.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, archive)
		return errors.Join(copyErr, file.Close())
	case tar.TypeSymlink:
		if filepath.IsAbs(entry.Linkname) || !filepath.IsLocal(filepath.Join(filepath.Dir(entry.Name), entry.Linkname)) { // #nosec G305 -- this is the containment check; writes use os.Root.
			return errors.New("symbolic link leaves the commit's source tree")
		}
		return root.Symlink(entry.Linkname, entry.Name)
	default:
		return fmt.Errorf("unexpected archive entry type %d", entry.Typeflag)
	}
}
