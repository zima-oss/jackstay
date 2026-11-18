//go:build !darwin && !linux

package procs

import "errors"

// errNoTable says the process table is not read on this system.
var errNoTable = &TableError{Err: errors.New("the process table is read on macOS and Linux only")}

// Table is nil where the process table is not read (neither macOS nor Linux): `jst bg` then works by deadline only.
func Table() map[int]Process { return nil }

// ReadTable fails where the process table is not read.
func ReadTable() (map[int]Process, error) { return nil, errNoTable }

// Row fails where the process table is not read.
func Row(_ int) (Process, bool, error) { return Process{}, false, errNoTable }

// ReadTableWithSessions fails where the process table is not read.
func ReadTableWithSessions() (map[int]Process, error) { return nil, errNoTable }
