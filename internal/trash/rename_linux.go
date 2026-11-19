package trash

import "golang.org/x/sys/unix"

func renameAbsent(source, target string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
}

func exchangeFiles(first, second string) error {
	return unix.Renameat2(unix.AT_FDCWD, first, unix.AT_FDCWD, second, unix.RENAME_EXCHANGE)
}
