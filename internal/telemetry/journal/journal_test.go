package journal

import (
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"golang.org/x/sys/unix"
)

func line(traceID string, start, end time.Time) []byte {
	return []byte(fmt.Sprintf(`{"resourceSpans":[{"resource":{"attributes":[{"key":"jackstay.project","value":{"stringValue":"p"}}]},`+
		`"scopeSpans":[{"scope":{"name":"jackstay"},"spans":[{"traceId":"%s","spanId":"00f067aa0ba902b7","name":"jst test","kind":1,`+
		`"startTimeUnixNano":"%d","endTimeUnixNano":"%d","attributes":[{"key":"jackstay.exit_code","value":{"intValue":"3"}},`+
		`{"key":"jackstay.guard.rules","value":{"arrayValue":{"values":[{"stringValue":"a"}]}}}],"status":{"code":2}}]}]}]}`+"\n",
		traceID, start.UnixNano(), end.UnixNano()))
}

func readAll(t *testing.T, folder string, from, to, today time.Time) ([]Span, Result) {
	t.Helper()
	var spans []Span
	result := Read([]string{folder}, from, to, today, func(span Span) { spans = append(spans, span) })
	return spans, result
}

func TestBoundedReadSkipsOneOversizeLineAndKeepsTheNext(t *testing.T) {
	folder := t.TempDir()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	valid := line("aa", now.Add(-time.Minute), now)
	oversized := append([]byte(strings.Repeat(" ", 16384)), valid...)
	data := append(oversized, valid...)
	if err := os.WriteFile(filepath.Join(folder, FileName("2026-11-17")), data, 0o600); err != nil {
		t.Fatal(err)
	}
	count := 0
	result := ReadBounded([]string{folder}, now.Add(-time.Hour), now.Add(time.Hour), now, len(valid), func(Span) { count++ })
	if count != 1 || result.Unreadable != 1 || len(result.Problems) != 0 {
		t.Fatalf("%d spans, %+v", count, result)
	}
}

func TestReadDecodesAndSkipsTornLines(t *testing.T) {
	folder := t.TempDir()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	data := append(line("aa", now.Add(-time.Minute), now), []byte("{\"resourceSpans\":[{tor")...)
	if err := os.WriteFile(filepath.Join(folder, FileName("2026-11-17")), data, 0o600); err != nil {
		t.Fatal(err)
	}
	spans, result := readAll(t, folder, now.Add(-time.Hour), now.Add(time.Hour), now)
	if len(spans) != 1 || result.Unreadable != 1 {
		t.Fatalf("%d spans, %+v", len(spans), result)
	}
	span := spans[0]
	if span.Number("jackstay.exit_code") != 3 || span.Texts("jackstay.guard.rules")[0] != "a" || span.ResourceText("jackstay.project") != "p" ||
		span.StatusCode != StatusError || span.Duration() != time.Minute {
		t.Fatalf("%+v", span)
	}
}

// F6: a span that crosses midnight is in the next day's file and still overlaps the earlier range.
func TestReadOverlapAcrossMidnight(t *testing.T) {
	folder := t.TempDir()
	start := time.Date(2026, 11, 16, 23, 50, 0, 0, time.Local)
	end := time.Date(2026, 11, 17, 0, 10, 0, 0, time.Local)
	if err := os.WriteFile(filepath.Join(folder, FileName("2026-11-17")), line("bb", start, end), 0o600); err != nil {
		t.Fatal(err)
	}
	day16 := time.Date(2026, 11, 16, 0, 0, 0, 0, time.Local)
	spans, _ := readAll(t, folder, day16, day16.AddDate(0, 0, 1), end)
	if len(spans) != 1 {
		t.Fatalf("the span crossing midnight is missed: %d", len(spans))
	}
	spans, _ = readAll(t, folder, end.Add(time.Minute), end.Add(time.Hour), end)
	if len(spans) != 0 {
		t.Fatal("a span ended before the range is read")
	}
}

// F7: while both the plain file and its .gz exist, a date is read once.
func TestReadOneFilePerDate(t *testing.T) {
	folder := t.TempDir()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	plain := filepath.Join(folder, FileName("2026-11-17"))
	if err := os.WriteFile(plain, line("cc", now, now), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeGzip(plain, plain+".gz"); err != nil {
		t.Fatal(err)
	}
	spans, _ := readAll(t, folder, now.Add(-time.Hour), now.Add(time.Hour), now)
	if len(spans) != 1 {
		t.Fatalf("counted %d times", len(spans))
	}
	_ = os.Remove(plain)
	spans, _ = readAll(t, folder, now.Add(-time.Hour), now.Add(time.Hour), now)
	if len(spans) != 1 {
		t.Fatalf("the .gz alone: %d", len(spans))
	}
}

func TestUpkeepCompressesAndDeletes(t *testing.T) {
	folder := t.TempDir()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	write := func(daysAgo int) string {
		day := now.AddDate(0, 0, -daysAgo).Format(time.DateOnly)
		place := filepath.Join(folder, FileName(day))
		if err := os.WriteFile(place, line("dd", now, now), 0o600); err != nil {
			t.Fatal(err)
		}
		return place
	}
	fresh, older, oldest := write(3), write(20), write(200)
	// A .gz left half-made by a stopped upkeep beside its plain file.
	broken := write(30)
	if err := os.WriteFile(broken+".gz", []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	UpkeepWithin(folder, DefaultRetention, now, time.Hour)
	exists := func(place string) bool { _, err := os.Stat(place); return err == nil }
	if !exists(fresh) || exists(fresh+".gz") {
		t.Fatal("a fresh file was touched")
	}
	if exists(older) || !exists(older+".gz") || !decodesWhole(older+".gz") {
		t.Fatal("an older file was not compressed")
	}
	if exists(broken) || !decodesWhole(broken+".gz") {
		t.Fatal("a broken .gz was not made again")
	}
	if exists(oldest) || exists(oldest+".gz") {
		t.Fatal("a file past retention was kept")
	}
	reader, _ := os.Open(older + ".gz")
	unpacked, _ := gzip.NewReader(reader)
	defer func() { _ = reader.Close() }()
	buffer := make([]byte, 20)
	if _, err := unpacked.Read(buffer); err != nil || !strings.HasPrefix(string(buffer), `{"resourceSpans"`) {
		t.Fatalf("content %q %v", buffer, err)
	}
}

// An upkeep past its bound before the first file leaves every file as it was (the bound's own
// test: a negative bound is over at once, whatever the machine's speed).
func TestUpkeepStopsAtItsBound(t *testing.T) {
	folder := t.TempDir()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	place := filepath.Join(folder, FileName(now.AddDate(0, 0, -200).Format(time.DateOnly)))
	if err := os.WriteFile(place, line("ee", now, now), 0o600); err != nil {
		t.Fatal(err)
	}
	UpkeepWithin(folder, DefaultRetention, now, -time.Nanosecond)
	if _, err := os.Stat(place); err != nil {
		t.Fatalf("an upkeep past its bound removed a file: %v", err)
	}
}

func TestAppendSeparatesAnInterruptedWriteFromTheNewSpan(t *testing.T) {
	folder := t.TempDir()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	day := now.Format(time.DateOnly)
	place := filepath.Join(folder, FileName(day))
	old := append(line("aa", now.Add(-time.Minute), now), []byte("{\"resourceSpans\":[{torn")...)
	if err := os.WriteFile(place, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if created, err := AppendLine(folder, day, line("bb", now, now)); created || err != nil {
		t.Fatalf("created %v, error %v", created, err)
	}
	spans, result := readAll(t, folder, now.Add(-time.Hour), now.Add(time.Hour), now)
	if result.Unreadable != 1 || len(spans) != 2 || spans[0].TraceID != "aa" || spans[1].TraceID != "bb" {
		t.Fatalf("recovered spans: %+v, result %+v", spans, result)
	}
}

func TestAppendGivesUpOnAHeldLock(t *testing.T) {
	folder := t.TempDir()
	if _, err := AppendLine(folder, "2026-11-17", []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(filepath.Join(folder, FileName("2026-11-17")), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	created, err := AppendLine(folder, "2026-11-17", []byte("{}\n"))
	if err != ErrLocked || created || time.Since(started) < LockWait {
		t.Fatalf("created %v err %v after %v", created, err, time.Since(started))
	}
}

func TestConcurrentAppendsLeaveWholeLines(t *testing.T) {
	folder := t.TempDir()
	payload := strings.Repeat("x", 9000)
	// An append that cannot take the lock within LockWait writes nothing (ErrLocked); a loaded
	// machine can make some do so, so the count expected leaves those out.
	var group sync.WaitGroup
	var dropped atomic.Int64
	for worker := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for count := range 25 {
				if _, err := AppendLine(folder, "2026-11-17", []byte(fmt.Sprintf("%d-%d %s\n", worker, count, payload))); err == ErrLocked {
					dropped.Add(1)
				} else if err != nil {
					t.Error(err)
				}
			}
		}()
	}
	group.Wait()
	data, _ := os.ReadFile(filepath.Join(folder, FileName("2026-11-17")))
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if want := 200 - int(dropped.Load()); len(lines) != want || want == 0 {
		t.Fatalf("%d lines, %d expected (%d appends gave up on the lock)", len(lines), want, dropped.Load())
	}
	for _, text := range lines {
		if !strings.HasSuffix(text, payload) {
			t.Fatal("a line was mixed with another")
		}
	}
}

func TestUnwritableFolderIsSwallowed(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(parent, 0o700) }()
	exporter := &Exporter{Folder: filepath.Join(parent, "telemetry")}
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	_, span := provider.Tracer("jackstay").Start(context.Background(), "x")
	span.End()
	if err := exporter.ExportSpans(context.Background(), recorder.Ended()); err != nil || exporter.Dropped != 1 {
		t.Fatalf("err %v, dropped %d", err, exporter.Dropped)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Fatalf("written: %v", entries)
	}
}

// A folder that cannot be read and a broken .gz are problems, not an empty answer.
func TestReadReportsProblems(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	locked := t.TempDir()
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(locked, 0o700) }()
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, FileName("2026-11-17")+".gz"), []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "none")
	result := Read([]string{locked, broken, missing}, now.Add(-time.Hour), now.Add(time.Hour), now, func(Span) {})
	if result.Folders != 1 || len(result.Problems) != 2 {
		t.Fatalf("%+v", result)
	}
}

// A range given in UTC still finds today's local file.
func TestReadRangeInAnotherZone(t *testing.T) {
	zone := time.FixedZone("UTC+2", 2*3600)
	previous := time.Local
	time.Local = zone
	defer func() { time.Local = previous }()
	folder := t.TempDir()
	now := time.Date(2026, 11, 17, 1, 0, 0, 0, zone) // 2026-11-16 23:00 UTC
	if err := os.WriteFile(filepath.Join(folder, FileName("2026-11-17")), line("ee", now.Add(-time.Minute), now), 0o600); err != nil {
		t.Fatal(err)
	}
	spans, _ := readAll(t, folder, now.Add(-30*time.Minute).UTC(), now.Add(time.Hour).UTC(), now.UTC())
	if len(spans) != 1 {
		t.Fatalf("%d spans", len(spans))
	}
}
