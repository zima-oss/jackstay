package trash

import "golang.org/x/sys/unix"

func renameAbsent(source, target string) error {
	return unix.RenamexNp(source, target, unix.RENAME_EXCL)
}

func exchangeFiles(first, second string) error {
	return unix.RenamexNp(first, second, unix.RENAME_SWAP)
}
