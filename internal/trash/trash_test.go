package trash

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMoveListRestore(t *testing.T) {
	work := t.TempDir()
	folder := filepath.Join(t.TempDir(), ".trash")
	item := filepath.Join(work, "gate-receipts")
	if err := os.MkdirAll(filepath.Join(item, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(item, "inner", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	entry, err := Move(folder, item, "jackstay", "s", DefaultKeepDays, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(item); err == nil {
		t.Fatal("the item is still there")
	}
	if entries := Entries(folder); len(entries) != 1 || entries[0].ID != entry.ID || entries[0].Kind != "folder" {
		t.Fatalf("entries %v", entries)
	}
	second, err := Move(folder, filepath.Join(work, "missing"), "jackstay", "s", DefaultKeepDays, now)
	if err == nil {
		t.Fatalf("a missing path was moved: %v", second)
	}
	if code := restore(folder, []string{entry.ID}, os.Stdout, os.Stderr); code != 0 {
		t.Fatalf("restore %d", code)
	}
	if data, err := os.ReadFile(filepath.Join(item, "inner", "x")); err != nil || string(data) != "x" {
		t.Fatalf("restored %q %v", data, err)
	}
	if len(Entries(folder)) != 0 {
		t.Fatal("the entry stays after a restore")
	}
}

func TestEntryIDIsNotReusedAfterRestoreInSameSecond(t *testing.T) {
	work := t.TempDir()
	folder := filepath.Join(t.TempDir(), ".trash")
	item := filepath.Join(work, "edits")
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(item, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Move(folder, item, "jackstay", "", DefaultKeepDays, now)
	if err != nil {
		t.Fatal(err)
	}
	if code := restore(folder, []string{first.ID}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("restore first = %d", code)
	}
	if err := os.WriteFile(item, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Move(folder, item, "jackstay", "", DefaultKeepDays, now)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatalf("reused %q after restoring its prior entry", first.ID)
	}
	if code := restore(folder, []string{first.ID}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("stale restore ID found a new entry: %d", code)
	}
	if entries := Entries(folder); len(entries) != 1 || entries[0].ID != second.ID {
		t.Fatalf("new entry lost after stale restore: %+v", entries)
	}
}

func TestLegacyCounterOnlyEntryIDRemainsReadable(t *testing.T) {
	folder := filepath.Join(t.TempDir(), ".trash")
	legacyID := "20261116-120000-1"
	place := filepath.Join(folder, legacyID)
	if err := os.MkdirAll(place, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := Entry{Path: filepath.Join(t.TempDir(), "old"), MovedAt: "2026-11-16T12:00:00Z", Kind: "file"}
	if err := writeRecord(place, entry); err != nil {
		t.Fatal(err)
	}
	if entries := Entries(folder); len(entries) != 1 || entries[0].ID != legacyID {
		t.Fatalf("legacy entry not readable: %+v", entries)
	}
}

func TestRestoreRefusesWhatIsThere(t *testing.T) {
	work := t.TempDir()
	folder := filepath.Join(t.TempDir(), ".trash")
	item := filepath.Join(work, "a")
	if err := os.WriteFile(item, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := Move(folder, item, "jackstay", "", DefaultKeepDays, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(item, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := restore(folder, []string{entry.ID}, os.Stdout, os.Stderr); code != 1 {
		t.Fatalf("restore over a file: %d", code)
	}
	if code := restore(folder, []string{entry.ID, "--to", filepath.Join(work, "b")}, os.Stdout, os.Stderr); code != 0 {
		t.Fatalf("restore --to: %d", code)
	}
}

func TestTheJackstayHomeIsNotMoved(t *testing.T) {
	home := t.TempDir()
	folder := filepath.Join(home, ".trash")
	if _, err := Move(folder, home, "jackstay", "", DefaultKeepDays, time.Now()); err == nil {
		t.Fatal("jackstay's own folder was moved")
	}
	if _, err := Move(folder, filepath.Dir(home), "jackstay", "", DefaultKeepDays, time.Now()); err == nil {
		t.Fatal("a folder holding jackstay's own was moved")
	}
}

func TestPruneReadsEachEntrysOwnDays(t *testing.T) {
	work := t.TempDir()
	folder := filepath.Join(t.TempDir(), ".trash")
	moved := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"short", "long"} {
		if err := os.WriteFile(filepath.Join(work, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Two checkouts that share a project name and keep different days.
	if _, err := Move(folder, filepath.Join(work, "short"), "same", "", 3, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(folder, filepath.Join(work, "long"), "same", "", 30, moved); err != nil {
		t.Fatal(err)
	}
	if removed := Prune(folder, moved.Add(2*24*time.Hour)); removed != 0 {
		t.Fatalf("removed %d after 2 days", removed)
	}
	if removed := Prune(folder, moved.Add(8*24*time.Hour)); removed != 1 || Entries(folder)[0].Path != filepath.Join(work, "long") {
		t.Fatalf("after 8 days removed %d, left %+v; want only the 3-day entry gone", removed, Entries(folder))
	}
	if removed := Prune(folder, moved.Add(31*24*time.Hour)); removed != 1 {
		t.Fatalf("removed %d after 31 days", removed)
	}
}

func TestPruneReadsTheOriginOfAnEntryWithoutDays(t *testing.T) {
	keeping := t.TempDir()
	config := `{"jackstay_schema": 2, "project": "keeping", "trash": {"keep_days": 30}}`
	if err := os.WriteFile(filepath.Join(keeping, "jackstay.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keeping, "edits"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), ".trash")
	moved := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	entry, err := Move(folder, filepath.Join(keeping, "edits"), "keeping", "", 30, moved)
	if err != nil {
		t.Fatal(err)
	}
	entry.KeepDays = nil // an entry written before entries recorded their days
	if err := writeRecord(filepath.Join(folder, entry.ID), entry); err != nil {
		t.Fatal(err)
	}
	if removed := Prune(folder, moved.Add(8*24*time.Hour)); removed != 0 {
		t.Fatalf("removed %d entries of a project that keeps 30 days, after 8", removed)
	}
	if removed := Prune(folder, moved.Add(31*24*time.Hour)); removed != 1 {
		t.Fatalf("removed %d after 31 days", removed)
	}
}

func TestTrashKeepsWhatItMovesWithZeroDays(t *testing.T) {
	project := t.TempDir()
	config := `{"jackstay_schema": 2, "project": "p", "trash": {"keep_days": 0}}`
	if err := os.WriteFile(filepath.Join(project, "jackstay.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "item"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	t.Chdir(project)
	var out, errs strings.Builder
	if status := Command([]string{"item"}, nil, &out, &errs); status != 0 {
		t.Fatalf("jst trash item = %d: %s", status, errs.String())
	}
	folder, err := Folder()
	if err != nil {
		t.Fatal(err)
	}
	if entries := Entries(folder); len(entries) != 1 || *entries[0].KeepDays != 0 {
		t.Fatalf("entries after jst trash = %+v; want the one moved, recording 0 days", entries)
	}
}

func TestAFailedRemovalAcrossVolumesKeepsTheCopy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes files from a folder it cannot write")
	}
	renameItem = func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.EXDEV} }
	t.Cleanup(func() { renameItem = os.Rename })
	work := t.TempDir()
	locked := filepath.Join(work, "tree", "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join(work, "tree", "loose"), filepath.Join(locked, "held")} {
		if err := os.WriteFile(name, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	folder := filepath.Join(t.TempDir(), ".trash")
	entry, err := Move(folder, filepath.Join(work, "tree"), "jackstay", "", 0, time.Now())
	if err == nil || entry.ID == "" || !strings.Contains(err.Error(), entry.ID) || !entry.Held {
		t.Fatalf("Move = %+v, %v; want the kept entry held and named in an error", entry, err)
	}
	if removed := Prune(folder, time.Now().Add(365*24*time.Hour)); removed != 0 || !Entries(folder)[0].Held {
		t.Fatalf("pruning removed %d; a held entry stays, whatever its days", removed)
	}
	for _, name := range []string{"loose", filepath.Join("locked", "held")} {
		if _, err := os.Stat(filepath.Join(folder, entry.ID, "item", "tree", name)); err != nil {
			t.Fatalf("the copy lost %s: %v", name, err)
		}
	}
}

func TestKeepDaysReadsTheProjectsSetting(t *testing.T) {
	project := t.TempDir()
	config := `{"jackstay_schema": 2, "project": "p", "trash": {"keep_days": 30}}`
	if err := os.WriteFile(filepath.Join(project, "jackstay.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if days := KeepDays(project); days != 30 {
		t.Fatalf("KeepDays = %d, want the project's 30", days)
	}
	if days := KeepDays(t.TempDir()); days != DefaultKeepDays {
		t.Fatalf("KeepDays without a project = %d, want %d", days, DefaultKeepDays)
	}
}

func TestTrashRecordsTheDaysOfTheItemsOwnCheckout(t *testing.T) {
	short, long := t.TempDir(), t.TempDir()
	for folder, days := range map[string]int{short: 7, long: 30} {
		config := fmt.Sprintf(`{"jackstay_schema": 2, "project": "same", "trash": {"keep_days": %d}}`, days)
		if err := os.WriteFile(filepath.Join(folder, "jackstay.json"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(long, "edits"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	t.Chdir(short)
	var out, errs strings.Builder
	if status := Command([]string{filepath.Join(long, "edits")}, nil, &out, &errs); status != 0 {
		t.Fatalf("jst trash = %d: %s", status, errs.String())
	}
	folder, err := Folder()
	if err != nil {
		t.Fatal(err)
	}
	if entries := Entries(folder); len(entries) != 1 || *entries[0].KeepDays != 30 || entries[0].Held {
		t.Fatalf("entries = %+v; want one, not held, with the 30 days of the checkout it came from", entries)
	}
}

func TestAPruneDuringAMoveLeavesItsCopy(t *testing.T) {
	renameItem = func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.EXDEV} }
	work := t.TempDir()
	folder := filepath.Join(t.TempDir(), ".trash")
	pruned := -1
	removeOriginal = func(source string) error {
		// Another command, keeping zero days, prunes while the original goes.
		pruned = Prune(folder, time.Now().Add(time.Hour))
		return os.RemoveAll(source)
	}
	t.Cleanup(func() { renameItem, removeOriginal = os.Rename, os.RemoveAll })
	if err := os.WriteFile(filepath.Join(work, "edits"), []byte("only copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := Move(folder, filepath.Join(work, "edits"), "p", "", 0, time.Now())
	if err != nil || pruned != 0 || entry.Held {
		t.Fatalf("Move = %+v, %v, pruned %d during it; want a whole move that nothing pruned", entry, err, pruned)
	}
	if data, err := os.ReadFile(filepath.Join(folder, entry.ID, "item", "edits")); err != nil || string(data) != "only copy" {
		t.Fatalf("the copy: %q, %v", data, err)
	}
}

func TestTrashingAWholeCheckoutKeepsItsOwnDays(t *testing.T) {
	parent := t.TempDir()
	checkout := filepath.Join(parent, "checkout")
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"jackstay_schema": 2, "project": "checkout", "trash": {"keep_days": 30}}`
	if err := os.WriteFile(filepath.Join(checkout, "jackstay.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	t.Chdir(parent)
	var out, errs strings.Builder
	if status := Command([]string{"checkout"}, nil, &out, &errs); status != 0 {
		t.Fatalf("jst trash checkout = %d: %s", status, errs.String())
	}
	folder, err := Folder()
	if err != nil {
		t.Fatal(err)
	}
	entries := Entries(folder)
	if len(entries) != 1 || *entries[0].KeepDays != 30 || entries[0].Project != "checkout" {
		t.Fatalf("entries = %+v; want the checkout's own project and 30 days", entries)
	}
	moved, _ := time.Parse(time.RFC3339, entries[0].MovedAt)
	if removed := Prune(folder, moved.Add(8*24*time.Hour)); removed != 0 {
		t.Fatalf("pruning after 8 days removed %d of a checkout that keeps 30", removed)
	}
}

func TestRestoreKeepsTheCurrentFileWhenTheMoveFails(t *testing.T) {
	folder := filepath.Join(t.TempDir(), ".trash")
	target := filepath.Join(t.TempDir(), "item")
	if err := os.WriteFile(target, []byte("edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := Move(folder, target, "p", "", DefaultKeepDays, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("committed"), 0o600); err != nil {
		t.Fatal(err)
	}
	written, err := Describe(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := Mark(folder, entry.ID, "discard", written); err != nil {
		t.Fatal(err)
	}
	exchangeRestore = func(string, string) error { return syscall.EIO }
	t.Cleanup(func() { exchangeRestore = exchangeFiles })
	var out, errs strings.Builder
	if code := restore(folder, []string{entry.ID}, &out, &errs); code != 1 {
		t.Fatalf("restore = %d, want 1: %s", code, errs.String())
	}
	read, err := os.ReadFile(target)
	if err != nil || string(read) != "committed" {
		t.Fatalf("target = %q, %v; want original committed content", read, err)
	}
	kept, err := os.ReadFile(filepath.Join(folder, entry.ID, "item", filepath.Base(target)))
	if err != nil || string(kept) != "edit" {
		t.Fatalf("trash = %q, %v; want retained edit", kept, err)
	}
}

func TestRestoreKeepsAnEditWrittenBeforeExchange(t *testing.T) {
	folder := filepath.Join(t.TempDir(), ".trash")
	target := filepath.Join(t.TempDir(), "item")
	if err := os.WriteFile(target, []byte("edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := Move(folder, target, "p", "", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("committed"), 0o600); err != nil {
		t.Fatal(err)
	}
	written, err := Describe(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := Mark(folder, entry.ID, "discard", written); err != nil {
		t.Fatal(err)
	}
	injected := false
	exchangeRestore = func(source, destination string) error {
		if injected {
			return exchangeFiles(source, destination)
		}
		injected = true
		if removed := Prune(folder, time.Now().Add(time.Hour)); removed != 0 {
			t.Errorf("pruned %d entries during restore", removed)
		}
		if err := os.WriteFile(target, []byte("new edit"), 0o600); err != nil {
			return err
		}
		return exchangeFiles(source, destination)
	}
	t.Cleanup(func() { exchangeRestore = exchangeFiles })
	var out, errs strings.Builder
	if code := restore(folder, []string{entry.ID}, &out, &errs); code != 1 || !strings.Contains(errs.String(), "changed during restore") {
		t.Fatalf("restore = %d: %s, want the new edit to block installation", code, errs.String())
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "new edit" {
		t.Fatalf("target = %q, %v, want the one changed during restore", data, err)
	}
	item := filepath.Join(folder, entry.ID, "item", filepath.Base(target))
	if data, err := os.ReadFile(item); err != nil || string(data) != "edit" {
		t.Fatalf("original = %q, %v, want the trash copy to remain", data, err)
	}
}
