// Package licences collects the licence files of the third-party modules compiled into jst, for a
// release archive (docs/design/implementation.md section 6; docs/licences.md): MIT and BSD-3-Clause ask that
// their notice and text go with a binary that contains them. The modules are the ones `go list
// -deps` finds for ./cmd/jst on every platform released, so the set follows go.mod by itself; their
// files are read from the module cache.
package licences

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Platforms are the GOOS values a release builds (.goreleaser.yaml).
var Platforms = []string{"darwin", "linux"}

// Package is the package whose binary is released.
const Package = "./cmd/jst"

// licenceName matches a module's top-level licence files: its licence, a notice, a patent grant.
var licenceName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|patents)([._-].*)?$`)

// Module is one third-party module in the binary.
type Module struct {
	Path, Version, Dir string
}

// listBudget bounds each `go list`.
const listBudget = 5 * time.Minute

// Modules are the third-party modules compiled into Package from the module at tree, on every
// platform, sorted by path.
func Modules(ctx context.Context, tree string) ([]Module, error) {
	found := map[string]Module{}
	for _, platform := range Platforms {
		bounded, cancel := context.WithTimeout(ctx, listBudget)
		list := exec.CommandContext(bounded, "go", "list", "-deps", "-f",
			"{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{with .Replace}}{{.Dir}}{{else}}{{.Dir}}{{end}}{{end}}{{end}}", Package)
		list.Dir, list.Env = tree, append(os.Environ(), "GOOS="+platform, "CGO_ENABLED=0", "GOFLAGS=-mod=readonly")
		var stderr bytes.Buffer
		list.Stderr = &stderr
		out, err := list.Output()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("go list -deps %s (GOOS=%s): %v: %s", Package, platform, err, strings.TrimSpace(stderr.String()))
		}
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Split(line, "\t")
			if len(fields) != 3 {
				continue
			}
			if fields[2] == "" {
				return nil, fmt.Errorf("%s %s is not in the module cache (go mod download)", fields[0], fields[1])
			}
			found[fields[0]] = Module{Path: fields[0], Version: fields[1], Dir: fields[2]}
		}
	}
	modules := make([]Module, 0, len(found))
	for _, module := range found {
		modules = append(modules, module)
	}
	slices.SortFunc(modules, func(left, right Module) int { return strings.Compare(left.Path, right.Path) })
	return modules, nil
}

// Collect writes the licence files of modules into out, which it makes and which must not exist
// (nothing of anyone's is ever removed): out/<module path>/<file>. A module with no
// licence file is an error, and out is removed again.
func Collect(modules []Module, out string) ([]string, error) {
	if err := os.Mkdir(out, 0o755); err != nil { // #nosec G301 -- shipped in a public archive
		return nil, fmt.Errorf("%s must be a new folder: %w", out, err)
	}
	var written []string
	fail := func(err error) ([]string, error) {
		_ = os.RemoveAll(out)
		return nil, err
	}
	for _, module := range modules {
		entries, err := os.ReadDir(module.Dir)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", module.Path, err))
		}
		count := 0
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !licenceName.MatchString(entry.Name()) {
				continue
			}
			place := filepath.Join(out, filepath.FromSlash(module.Path), entry.Name())
			if err := copyFile(filepath.Join(module.Dir, entry.Name()), place); err != nil {
				return fail(fmt.Errorf("%s: %w", module.Path, err))
			}
			written = append(written, place)
			count++
		}
		if count == 0 {
			return fail(fmt.Errorf("%s %s has no licence file in %s: it cannot be shipped in a binary until it is read (docs/licences.md)",
				module.Path, module.Version, module.Dir))
		}
	}
	return written, nil
}

func copyFile(from, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil { // #nosec G301 -- shipped in a public archive
		return err
	}
	source, err := os.Open(from) // #nosec G304 -- a file of the module cache
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G302 G304 -- shipped in a public archive
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, source)
	return errors.Join(copyErr, target.Close())
}

// Main is cmd/jst-licences OUT: the licence files of the modules in jst, collected into OUT from
// the module in the working folder (goreleaser's before hook).
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: go run ./cmd/jst-licences OUT")
		return 2
	}
	modules, err := Modules(context.Background(), ".")
	if err == nil {
		_, err = Collect(modules, args[0])
	}
	if err != nil {
		fmt.Fprintf(stderr, "jst-licences: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "jst-licences: the licence files of %d modules in %s\n", len(modules), args[0])
	return 0
}
