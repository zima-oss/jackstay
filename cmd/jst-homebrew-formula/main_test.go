package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func releaseFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	var checksums strings.Builder
	for _, platform := range platforms {
		name := "jackstay_1.2.3_" + platform.system + "_" + platform.architecture + ".tar.gz"
		path := filepath.Join(directory, name)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		compressed := gzip.NewWriter(file)
		archive := tar.NewWriter(compressed)
		for _, entry := range []struct {
			name string
			mode int64
		}{
			{"jst", 0755}, {"LICENSE", 0644}, {"licenses/example/LICENSE", 0644},
		} {
			if err := archive.WriteHeader(&tar.Header{Name: entry.name, Mode: entry.mode, Size: 1, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := archive.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents)
		checksums.WriteString(hex.EncodeToString(digest[:]) + "  " + name + "\n")
	}
	if err := os.WriteFile(filepath.Join(directory, "checksums.txt"), []byte(checksums.String()), 0644); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestRenderFormula(t *testing.T) {
	directory := releaseFixture(t)
	formula, err := render("1.2.3", directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range platforms {
		name := "jackstay_1.2.3_" + platform.system + "_" + platform.architecture + ".tar.gz"
		if !strings.Contains(formula, name) {
			t.Errorf("formula omits %s", name)
		}
	}
	for _, line := range []string{"on_macos do", "on_linux do", "on_arm do", "on_intel do", `bin.install "jst"`, `pkgshare.install "LICENSE", "licenses"`, `shell_output("#{bin}/jst --version")`} {
		if !strings.Contains(formula, line) {
			t.Errorf("formula omits %s", line)
		}
	}
}

func TestRenderRejectsInvalidRelease(t *testing.T) {
	for _, trial := range []struct {
		name   string
		change func(t *testing.T, directory string)
	}{
		{"changed archive", func(t *testing.T, directory string) {
			path := filepath.Join(directory, "jackstay_1.2.3_darwin_arm64.tar.gz")
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte("changed")); err != nil {
				t.Fatal(err)
			}
			file.Close()
		}},
		{"missing checksum", func(t *testing.T, directory string) {
			path := filepath.Join(directory, "checksums.txt")
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(contents), "\n")
			os.WriteFile(path, []byte(strings.Join(lines[1:], "\n")), 0644)
		}},
		{"missing binary", func(t *testing.T, directory string) {
			// A missing archive is also a missing binary for the formula.
			os.Remove(filepath.Join(directory, "jackstay_1.2.3_darwin_arm64.tar.gz"))
		}},
	} {
		t.Run(trial.name, func(t *testing.T) {
			directory := releaseFixture(t)
			trial.change(t, directory)
			if _, err := render("1.2.3", directory); err == nil {
				t.Fatal("accepted invalid release")
			}
		})
	}
	if _, err := render("../1.2.3", releaseFixture(t)); err == nil {
		t.Fatal("accepted invalid version")
	}
}

func TestArchiveRejectsTraversal(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "archive-*.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	if err := archive.WriteHeader(&tar.Header{Name: "../outside", Mode: 0644, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = archiveDigestAndLayout(file)
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("archive traversal: %v", err)
	}
}
