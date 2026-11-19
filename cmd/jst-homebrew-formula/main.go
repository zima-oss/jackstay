// Command jst-homebrew-formula renders a tap formula from verified release archives.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

var platforms = []struct{ system, architecture string }{
	{"darwin", "arm64"},
	{"darwin", "amd64"},
	{"linux", "arm64"},
	{"linux", "amd64"},
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: jst-homebrew-formula VERSION DIST_DIRECTORY")
		os.Exit(2)
	}
	formula, err := render(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "jst-homebrew-formula:", err)
		os.Exit(1)
	}
	fmt.Print(formula)
}

func render(version, directory string) (string, error) {
	if !releaseVersion.MatchString(version) {
		return "", fmt.Errorf("invalid release version %q", version)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	manifest, err := openDist(root, "checksums.txt")
	if err != nil {
		return "", err
	}
	const manifestLimit = 64 << 10
	checksums, err := io.ReadAll(io.LimitReader(manifest, manifestLimit+1))
	_ = manifest.Close()
	if err != nil || len(checksums) > manifestLimit {
		return "", errors.New("checksums.txt is unreadable or oversized")
	}
	entries := make(map[string]string)
	for line := range strings.SplitSeq(strings.TrimSpace(string(checksums)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			return "", fmt.Errorf("invalid checksums.txt line %q", line)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", fmt.Errorf("invalid digest for %q: %w", fields[1], err)
		}
		if _, exists := entries[fields[1]]; exists {
			return "", fmt.Errorf("duplicate checksum for %q", fields[1])
		}
		entries[fields[1]] = strings.ToLower(fields[0])
	}
	var result strings.Builder
	fmt.Fprintf(&result, "class Jackstay < Formula\n  desc \"Reliable commands for coding agents in Git repositories\"\n  homepage \"https://github.com/zima-oss/jackstay\"\n  version \"%s\"\n  license \"Apache-2.0\"\n\n", version)
	for index, platform := range platforms {
		name := fmt.Sprintf("jackstay_%s_%s_%s.tar.gz", version, platform.system, platform.architecture)
		want, exists := entries[name]
		if !exists {
			return "", fmt.Errorf("checksums.txt has no %s", name)
		}
		archive, err := openDist(root, name)
		if err != nil {
			return "", err
		}
		actual, err := archiveDigestAndLayout(archive)
		if closeErr := archive.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		if actual != want {
			return "", fmt.Errorf("%s: checksum mismatch", name)
		}
		if index == 0 || index == 2 {
			if index > 0 {
				result.WriteString("  end\n\n")
			}
			if platform.system == "darwin" {
				result.WriteString("  on_macos do\n")
			} else {
				result.WriteString("  on_linux do\n")
			}
		}
		if platform.architecture == "arm64" {
			result.WriteString("    on_arm do\n")
		} else {
			result.WriteString("    on_intel do\n")
		}
		fmt.Fprintf(&result, "      url \"https://github.com/zima-oss/jackstay/releases/download/v%s/%s\"\n      sha256 \"%s\"\n    end\n", version, name, want)
	}
	result.WriteString("  end\n\n  def install\n    bin.install \"jst\"\n    pkgshare.install \"LICENSE\", \"licenses\"\n  end\n\n  test do\n    assert_match version.to_s, shell_output(\"#{bin}/jst --version\")\n  end\nend\n")
	return result.String(), nil
}

func openDist(root *os.Root, name string) (*os.File, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && info.Mode().IsRegular() {
		return file, nil
	}
	_ = file.Close()
	return nil, fmt.Errorf("%s is not a readable regular file", name)
}

func archiveDigestAndLayout(archive *os.File) (string, error) {
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, archive); err != nil {
		return "", err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return "", err
	}
	defer func() { _ = compressed.Close() }()
	const unpackLimit = 512 << 20
	unpacked := &io.LimitedReader{R: compressed, N: unpackLimit + 1}
	reader := tar.NewReader(unpacked)
	foundBinary, foundLicense, foundDependencyLicenses := false, false, false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if path.IsAbs(header.Name) || slices.Contains(strings.Split(header.Name, "/"), "..") ||
			strings.Contains(header.Name, "\\") || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir) {
			return "", errors.New("archive contains an unsafe path or file type")
		}
		switch header.Name {
		case "jst":
			foundBinary = header.Typeflag == tar.TypeReg && header.Mode&0111 != 0
		case "LICENSE":
			foundLicense = header.Typeflag == tar.TypeReg
		default:
			if strings.HasPrefix(header.Name, "licenses/") && header.Typeflag == tar.TypeReg {
				foundDependencyLicenses = true
			}
		}
	}
	_, err = io.CopyN(io.Discard, unpacked, unpacked.N)
	if unpacked.N == 0 {
		return "", errors.New("archive exceeds the unpacked size limit")
	}
	if !errors.Is(err, io.EOF) {
		return "", err
	}
	if !foundBinary || !foundLicense || !foundDependencyLicenses {
		return "", errors.New("archive must contain executable jst, LICENSE and licenses/ files at root")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
