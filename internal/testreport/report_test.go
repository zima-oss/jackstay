package testreport

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReportReadsOnlyANewRegularFileInItsOriginalDirectory(t *testing.T) {
	folder := t.TempDir()
	for _, name := range []string{"../other.xml", "dir/report.xml", "/tmp/report.xml", "report*.xml", ".", ""} {
		if report, err := OpenReport(folder, name); err == nil {
			_ = report.Close()
			t.Fatalf("accepted %q", name)
		}
	}
	report, err := OpenReport(folder, "tests.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	if file, err := report.Read(); err == nil {
		_ = file.Close()
		t.Fatal("read a missing report")
	}
	other := filepath.Join(t.TempDir(), "other.xml")
	if err := os.WriteFile(other, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, report.Path()); err != nil {
		t.Fatal(err)
	}
	if file, err := report.Read(); err == nil {
		_ = file.Close()
		t.Fatal("followed a report symlink")
	}
	if err := os.Remove(report.Path()); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(report.Path(), 0o600); err != nil {
		t.Fatal(err)
	}
	if file, err := report.Read(); err == nil {
		_ = file.Close()
		t.Fatal("accepted a FIFO report")
	}
	if err := os.Remove(report.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report.Path(), []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	if stale, err := OpenReport(folder, "tests.xml"); err == nil {
		_ = stale.Close()
		t.Fatal("accepted a report from an earlier attempt")
	}
	file, err := report.Read()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil || string(got) != "report" {
		t.Fatalf("report: %q %v", got, err)
	}
}
