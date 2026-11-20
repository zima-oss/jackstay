// Package sourceview materializes pinned Git objects for reviewers without a checkout.
// Git links and symbolic links are inert regular files in the projection.
package sourceview

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// Revisions are resolved Git object IDs, never branch names or user-supplied options.
type Revisions struct {
	Base     string
	Head     string
	Previous string
}

type entry struct {
	mode string
	oid  string
	path string
}

// Build creates a fresh projection. On failure it removes the incomplete projection.
// The caller owns destination and removes it when the reviewer exits.
func Build(ctx context.Context, repositoryRoot, destination string, revisions Revisions) (err error) {
	for _, revision := range []string{revisions.Base, revisions.Head} {
		if !objectID(revision) {
			return fmt.Errorf("invalid pinned Git object ID %q", revision)
		}
	}
	if revisions.Previous != "" && !objectID(revisions.Previous) {
		return fmt.Errorf("invalid previous Git object ID %q", revisions.Previous)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return fmt.Errorf("create review projection: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(destination))
		}
	}()
	git := gitReader{root: repositoryRoot}
	for _, snapshot := range []struct{ name, revision string }{{"head", revisions.Head}, {"base", revisions.Base}, {"previous", revisions.Previous}} {
		if snapshot.revision == "" {
			continue
		}
		if err = git.snapshot(ctx, filepath.Join(destination, snapshot.name), snapshot.revision); err != nil {
			return fmt.Errorf("materialize %s: %w", snapshot.name, err)
		}
	}
	changes := filepath.Join(destination, "changes")
	if err = os.Mkdir(changes, 0o700); err != nil {
		return err
	}
	for _, change := range []struct{ name, revision string }{{"base", revisions.Base}, {"previous", revisions.Previous}} {
		if change.revision == "" {
			continue
		}
		if err = git.change(ctx, changes, change.name, change.revision, revisions.Head); err != nil {
			return fmt.Errorf("materialize %s changes: %w", change.name, err)
		}
	}
	return nil
}

func objectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, digit := range value {
		if !strings.ContainsRune("0123456789abcdef", digit) {
			return false
		}
	}
	return true
}

type gitReader struct {
	root            string
	attributeSource string
}

type streamPipe struct{ *io.PipeWriter }

func (pipe streamPipe) StreamEnded() { _ = pipe.Close() }

type checkedWriter struct {
	io.Writer
	mutex sync.Mutex
	err   error
}

func (writer *checkedWriter) Write(data []byte) (int, error) {
	count, err := writer.Writer.Write(data)
	writer.mutex.Lock()
	if writer.err == nil {
		writer.err = err
	}
	writer.mutex.Unlock()
	return count, err
}

func (writer *checkedWriter) Err() error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.err
}

func (writer *checkedWriter) StreamEnded() {
	if done, ok := writer.Writer.(interface{ StreamEnded() }); ok {
		done.StreamEnded()
	}
}

type diagnosticBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (buffer *diagnosticBuffer) Write(data []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	const maximum = 64 << 10
	if remaining := maximum - buffer.buffer.Len(); remaining > 0 {
		_, _ = buffer.buffer.Write(data[:min(remaining, len(data))])
	}
	return len(data), nil
}

func (buffer *diagnosticBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.String()
}

func (git gitReader) stream(ctx context.Context, input []byte, output io.Writer, args ...string) error {
	argv := append([]string{"git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false"}, args...)
	env := append(gitcli.WithoutRepository(os.Environ()), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "LC_ALL=C")
	if git.attributeSource != "" {
		env = append(env, "GIT_ATTR_SOURCE="+git.attributeSource)
	}
	var stderr diagnosticBuffer
	checked := &checkedWriter{Writer: output}
	outcome, err := supervise.Run(nil, supervise.Spec{Argv: argv, Dir: git.root, Env: env, Mode: supervise.Group,
		Input: input, Context: ctx, Grace: supervise.CaptureGrace, KillWait: supervise.CaptureGrace,
		ReadBound: supervise.CaptureGrace, StdoutTo: checked, StderrTo: &stderr})
	if err != nil {
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("git %s: %w", args[0], ctx.Err())
	}
	if checked.Err() != nil || outcome.Status() != 0 || outcome.Unread != "" {
		return fmt.Errorf("git %s: exit %d, unread %q, output %v: %s", args[0], outcome.Status(), outcome.Unread, checked.Err(), stderr.String())
	}
	return nil
}

// parseStream keeps Git output in a bounded pipe while supervise owns the entire process group.
func (git gitReader) parseStream(ctx context.Context, input []byte, parse func(io.Reader) error, args ...string) error {
	reader, writer := io.Pipe()
	parsed := make(chan error, 1)
	go func() {
		defer func() { _ = reader.Close() }()
		parsed <- parse(reader)
	}()
	runErr := git.stream(ctx, input, streamPipe{writer}, args...)
	_ = writer.Close()
	parseErr := <-parsed
	return errors.Join(runErr, parseErr)
}

func (git gitReader) snapshot(ctx context.Context, destination, revision string) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	var entries []entry
	err := git.parseStream(ctx, nil, func(input io.Reader) error {
		reader := bufio.NewReader(input)
		for {
			record, readErr := reader.ReadBytes(0)
			if readErr == io.EOF && len(record) == 0 {
				return nil
			}
			if readErr != nil || record[len(record)-1] != 0 {
				return fmt.Errorf("truncated Git tree record: %w", readErr)
			}
			item, err := parseEntry(record[:len(record)-1])
			if err != nil {
				return err
			}
			entries = append(entries, item)
		}
	}, "ls-tree", "-r", "-z", "--full-tree", revision)
	if err != nil {
		return err
	}
	return git.blobs(ctx, destination, entries)
}

func parseEntry(record []byte) (entry, error) {
	metadata, path, found := bytes.Cut(record, []byte{'\t'})
	fields := bytes.Fields(metadata)
	if !found || len(fields) != 3 || !objectID(string(fields[2])) || len(path) == 0 {
		return entry{}, fmt.Errorf("invalid Git tree entry")
	}
	mode, kind, oid := string(fields[0]), string(fields[1]), string(fields[2])
	if (kind != "blob" || (mode != "100644" && mode != "100755" && mode != "120000")) && (kind != "commit" || mode != "160000") {
		return entry{}, fmt.Errorf("unsupported Git tree mode %s %s", mode, kind)
	}
	name := string(path)
	if !filepath.IsLocal(name) {
		return entry{}, fmt.Errorf("unsafe Git tree path %q", name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == "." || component == ".." || component == "" || strings.EqualFold(component, ".git") {
			return entry{}, fmt.Errorf("unsafe Git tree path %q", name)
		}
	}
	return entry{mode: mode, oid: oid, path: name}, nil
}

func (git gitReader) blobs(ctx context.Context, destination string, entries []entry) error {
	var requested bytes.Buffer
	var blobs []entry
	directories := make(map[string]bool)
	for _, item := range entries {
		if item.mode == "160000" {
			if err := writeBlob(destination, item.path, strings.NewReader("Submodule "+item.oid+"\n"), int64(len(item.oid)+11), directories); err != nil {
				return err
			}
		} else {
			blobs = append(blobs, item)
			requested.WriteString(item.oid + "\n")
		}
	}
	if len(blobs) == 0 {
		return nil
	}
	return git.parseStream(ctx, requested.Bytes(), func(input io.Reader) error {
		reader := bufio.NewReader(input)
		for _, item := range blobs {
			header, err := reader.ReadString('\n')
			if err != nil {
				return err
			}
			size, err := blobSize(item, header)
			if err != nil {
				return err
			}
			if err := writeBlob(destination, item.path, reader, size, directories); err != nil {
				return err
			}
			separator, err := reader.ReadByte()
			if err != nil || separator != '\n' {
				return fmt.Errorf("invalid Git blob separator: %w", err)
			}
		}
		if _, err := reader.ReadByte(); err != io.EOF {
			return fmt.Errorf("extra Git blob bytes: %w", err)
		}
		return nil
	}, "cat-file", "--batch")
}

func blobSize(item entry, header string) (int64, error) {
	fields := strings.Fields(header)
	if len(fields) == 2 && fields[0] == item.oid && fields[1] == "missing" {
		return 0, fmt.Errorf("git blob %s for %q is missing locally; fetch the blob before reviewing this partial clone", item.oid, item.path)
	}
	if len(fields) != 3 || fields[0] != item.oid || fields[1] != "blob" {
		return 0, fmt.Errorf("unexpected Git blob header %q for %q", header, item.path)
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("invalid Git blob size %q for %q", fields[2], item.path)
	}
	return size, nil
}

func writeBlob(destination, name string, input io.Reader, size int64, directories map[string]bool) error {
	if err := makeExactParents(destination, name, directories); err != nil {
		return err
	}
	path := filepath.Join(destination, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("source path %q collides with another tracked path on this filesystem: %w", name, err)
	}
	if err != nil {
		return err
	}
	_, copyErr := io.CopyN(file, input, size)
	return errors.Join(copyErr, file.Close())
}

func makeExactParents(destination, name string, known map[string]bool) error {
	parent := destination
	for _, component := range strings.Split(filepath.Dir(name), string(filepath.Separator)) {
		if component == "." {
			break
		}
		path := filepath.Join(parent, component)
		if known[path] {
			parent = path
			continue
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create source directory %q: %w", path, err)
			}
			members, readErr := os.ReadDir(parent)
			if readErr != nil {
				return readErr
			}
			found := false
			for _, member := range members {
				if member.Name() == component && member.IsDir() {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("source directory %q collides with another tracked path on this filesystem", filepath.Join(parent, component))
			}
		}
		known[path] = true
		parent = path
	}
	return nil
}

func (git gitReader) change(ctx context.Context, destination, name, from, head string) error {
	git.attributeSource = head
	patch := filepath.Join(destination, name+".patch")
	if err := git.diffToFile(ctx, patch, from, head); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(destination, name+".paths"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return err
	}
	parseErr := git.parseStream(ctx, nil, func(input io.Reader) error {
		reader := bufio.NewReader(input)
		encoder := json.NewEncoder(file)
		for {
			status, readErr := readField(reader)
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil || status == "" {
				return fmt.Errorf("truncated changed-path status: %w", readErr)
			}
			change := changedPath{Status: status}
			first, readErr := readField(reader)
			if readErr != nil || first == "" {
				return fmt.Errorf("truncated changed path: %w", readErr)
			}
			switch status[0] {
			case 'R', 'C':
				change.Old = first
				change.New, readErr = readField(reader)
				if readErr != nil || change.New == "" {
					return fmt.Errorf("truncated renamed path: %w", readErr)
				}
			case 'D':
				change.Old = first
			default:
				change.New = first
			}
			if err := encoder.Encode(change); err != nil {
				return err
			}
		}
	}, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=none", "--find-renames", "--name-status", "-z", from, head, "--")
	return errors.Join(parseErr, file.Close())
}

type changedPath struct {
	Status string `json:"status"`
	Old    string `json:"old,omitempty"`
	New    string `json:"new,omitempty"`
}

func readField(reader *bufio.Reader) (string, error) {
	value, err := reader.ReadBytes(0)
	if err != nil {
		return "", err
	}
	return string(value[:len(value)-1]), nil
}

// diffToFile writes the patch from from to head, less the contents of `.env` files: a reviewer
// may not read them (docs/design/review.md section 3.4); the change index still names them.
func (git gitReader) diffToFile(ctx context.Context, destination, from, head string) error {
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return err
	}
	runErr := git.stream(ctx, nil, file, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=none", "--find-renames", "--submodule=short", "--full-index", "--binary", from, head, "--", ".", ":(exclude,glob)**/.env", ":(exclude,glob)**/.env.*")
	return errors.Join(runErr, file.Close())
}
