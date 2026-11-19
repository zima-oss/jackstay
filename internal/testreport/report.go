// Package testreport transports declared structured reports from a test attempt.
package testreport

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// PathVariable is the private report path supplied to a file-based judge's command.
const PathVariable = "JACKSTAY_TEST_REPORT"

// DirectoryVariable is the private report directory for a producer with several outputs.
const DirectoryVariable = "JACKSTAY_TEST_REPORT_DIR"

// MaxReports limits the number of explicitly declared files in one test attempt.
const MaxReports = 32

// FileJudge reports whether a configured judge reads a declared output file.
func FileJudge(name string) bool {
	return name == "junit" || name == "jest-json" || name == "trx" || name == "xcresult-json"
}

// Report binds declared files to a private attempt directory before the producer starts.
type Report struct {
	root  *os.Root
	names []string
	dir   string
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

// ValidReportNames checks a nonempty bounded list of distinct portable filenames. Case-fold
// names because deployments can share the configuration with a case-insensitive filesystem.
func ValidReportNames(names []string) bool {
	if len(names) == 0 || len(names) > MaxReports {
		return false
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		folded := strings.ToLower(name)
		if !ValidReportName(name) || seen[folded] {
			return false
		}
		seen[folded] = true
	}
	return true
}

// OpenReport anchors the report inside an existing private directory.
func OpenReport(directory, name string) (*Report, error) {
	return OpenReports(directory, []string{name})
}

// OpenReports binds the declared files and refuses an already populated path.
func OpenReports(directory string, names []string) (*Report, error) {
	if !ValidReportNames(names) {
		return nil, errors.New("the reports need distinct portable file names")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			_ = root.Close()
			if err == nil {
				err = errors.New("a report path already exists")
			}
			return nil, err
		}
	}
	return &Report{root: root, names: slices.Clone(names), dir: absolute}, nil
}

// Path is the absolute filename a single-file producer receives.
func (report *Report) Path() string { return filepath.Join(report.dir, report.names[0]) }

// Directory is the absolute directory a multiple-file producer receives.
func (report *Report) Directory() string { return report.dir }

// Count is the number of declared report files.
func (report *Report) Count() int { return len(report.names) }

// Close releases the directory handle. The caller owns the directory's cleanup.
func (report *Report) Close() error { return report.root.Close() }

// Read opens only a regular file in the original attempt directory. Nonblocking open makes
// the regular-file check safe even if the producer leaves a FIFO in place of the report.
func (report *Report) Read() (*os.File, error) {
	return report.ReadAt(0)
}

// ReadAt opens the declared file at position index with the same protections as Read.
func (report *Report) ReadAt(index int) (*os.File, error) {
	file, err := report.root.OpenFile(report.names[index], os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
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
