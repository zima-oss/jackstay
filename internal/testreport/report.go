// Package testreport transports a single structured report from a test attempt.
package testreport

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// PathVariable is the private report path supplied to a file-based judge's command.
const PathVariable = "JACKSTAY_TEST_REPORT"

// Report binds one declared file to a private attempt directory before the producer starts.
type Report struct {
	root *os.Root
	name string
	path string
}

// ValidReportName permits one portable file name with no globs or directory components.
func ValidReportName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	for _, letter := range name {
		valid := letter >= 'a' && letter <= 'z' || letter >= 'A' && letter <= 'Z' ||
			letter >= '0' && letter <= '9' || letter == '.' || letter == '_' || letter == '-'
		if !valid {
			return false
		}
	}
	return true
}

// OpenReport anchors the report inside an existing private directory.
func OpenReport(directory, name string) (*Report, error) {
	if !ValidReportName(name) {
		return nil, errors.New("the report needs a single portable file name")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		_ = root.Close()
		if err == nil {
			err = errors.New("the report path already exists")
		}
		return nil, err
	}
	return &Report{root: root, name: name, path: filepath.Join(absolute, name)}, nil
}

// Path is the absolute filename the producer receives.
func (report *Report) Path() string { return report.path }

// Close releases the directory handle. The caller owns the directory's cleanup.
func (report *Report) Close() error { return report.root.Close() }

// Read opens only a regular file in the original attempt directory. Nonblocking open makes
// the regular-file check safe even if the producer leaves a FIFO in place of the report.
func (report *Report) Read() (*os.File, error) {
	file, err := report.root.OpenFile(report.name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && info.Mode().IsRegular() {
		return file, nil
	}
	_ = file.Close()
	if err != nil {
		return nil, err
	}
	return nil, errors.New("the report is not a regular file")
}
