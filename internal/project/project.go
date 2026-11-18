// Package project is the project a command runs in: the folder of its jackstay.json, the sections the
// command reads (internal/config judges them), its name, and paths inside its repository.
package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zima-oss/jackstay/internal/config"
)

// Project is a project found from a folder.
type Project struct {
	Root   string
	Config config.Config
	Name   string
}

// Open is the project around start, with the sections need names usable (a broken section that is
// not needed takes nothing away). An error when there is no jackstay.json or a needed part is unusable.
func Open(start string, need []string) (Project, error) {
	root, loaded, err := config.Load(start, need)
	if err != nil {
		return Project{}, err
	}
	if root == "" {
		return Project{}, fmt.Errorf("no %s at or above %s", config.Name, start)
	}
	return Project{Root: root, Config: loaded, Name: config.ProjectName(root, loaded, start)}, nil
}

// Here is the project around the working folder.
func Here(need []string) (Project, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return Project{}, err
	}
	return Open(workDir, need)
}

// Inside is relative (written from the repository's root) as a real path, refused when it or a
// link on its way leads out of the repository. A path that does not exist yet is judged by the
// nearest folder above it that does.
func Inside(root, relative string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(realRoot, relative)
	existing, rest := joined, ""
	for {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			real := filepath.Join(resolved, rest)
			if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
				return "", fmt.Errorf("%s leads out of the repository (%s); jackstay writes and reads only inside it", relative, real)
			}
			return real, nil
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", err
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
}

// Problem is a failure a command reports with its exit status.
type Problem struct {
	Code    int
	Message string
}

func (failure *Problem) Error() string { return failure.Message }

// Fail is a Problem with exit status 2 (the request, jackstay.json, git or a state folder unusable).
func Fail(format string, values ...any) error {
	return &Problem{Code: 2, Message: fmt.Sprintf(format, values...)}
}
