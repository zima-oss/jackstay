package receipts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/version"
)

func newStore(t *testing.T) (Store, *fakeClock) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	clock := newClock()
	store, err := OpenStore("Demo", map[string]any{}, clock)
	if err != nil {
		t.Fatal(err)
	}
	return store, clock
}

func receiptAt(key string, started, ended time.Time) Receipt {
	return Receipt{Schema: Schema, Key: key, Outcome: OutcomePassed, Jackstay: version.Version, StartedEpoch: epochOf(started),
		EndedEpoch: epochOf(ended), Seconds: 1.5, EvidenceCount: 3}
}

func TestTheStoreIsTheProjectsReceiptsFolder(t *testing.T) {
	store, _ := newStore(t)
	want := filepath.Join(os.Getenv("JACKSTAY_HOME"), "^demo", "receipts")
	if store.Folder != want {
		t.Fatalf("folder %s, want %s", store.Folder, want)
	}
}

func TestAReceiptIsUsedOnlyWhenEveryRuleHolds(t *testing.T) {
	store, clock := newStore(t)
	now := clock.Now()
	if _, why := store.Read("k1"); why != "no receipt" {
		t.Fatal(why)
	}
	if _, err := store.Write(receiptAt("k1", now.Add(-time.Minute), now)); err != nil {
		t.Fatal(err)
	}
	if receipt, why := store.Read("k1"); receipt == nil || receipt.Path == "" {
		t.Fatalf("not used: %s", why)
	}
	cases := map[string]struct {
		edit func(*Receipt)
		why  string
	}{
		"another key":     {func(r *Receipt) { r.Key = "other" }, "not a receipt for this key"},
		"an older schema": {func(r *Receipt) { r.Schema = 2 }, "not a receipt for this key"},
		"not a pass":      {func(r *Receipt) { r.Outcome = OutcomeFailed }, "not a receipt for this key"},
		"another version": {func(r *Receipt) { r.Jackstay = "0.0.1" }, "written by jackstay 0.0.1"},
		"too old":         {func(r *Receipt) { r.EndedEpoch = epochOf(now.Add(-73 * time.Hour)) }, "h old (limit 72 h)"},
		"from the future": {func(r *Receipt) { r.EndedEpoch = epochOf(now.Add(time.Hour)) }, "h old"},
		"no time":         {func(r *Receipt) { r.EndedEpoch = 0 }, "time cannot be read"},
	}
	for name, c := range cases {
		receipt := receiptAt("k2", now.Add(-time.Minute), now)
		c.edit(&receipt)
		if _, err := writeRaw(store, "k2", receipt); err != nil {
			t.Fatal(err)
		}
		if got, why := store.Read("k2"); got != nil || !strings.Contains(why, c.why) {
			t.Errorf("%s: used=%v why=%q", name, got != nil, why)
		}
	}
	if err := os.WriteFile(filepath.Join(store.Folder, "k3.json"), []byte("{damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, why := store.Read("k3"); why != "the receipt file is not JSON" {
		t.Fatal(why)
	}
}

func writeRaw(store Store, key string, receipt Receipt) (string, error) {
	data, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	place := filepath.Join(store.Folder, key+".json")
	return place, os.WriteFile(place, data, 0o600)
}

func TestAFailureNoteNewerThanTheRunsStartStopsItsUse(t *testing.T) {
	store, clock := newStore(t)
	start := clock.Now()
	if !store.NoteFailure("k", OutcomeFailed) {
		t.Fatal("note not written")
	}
	end := clock.Now()
	if _, err := store.Write(receiptAt("k", start, end)); err != nil {
		t.Fatal(err)
	}
	if _, why := store.Read("k"); !strings.Contains(why, "failed after the receipt's run had started") {
		t.Fatal(why)
	}
	// A receipt whose run started after the note is used.
	if _, err := store.Write(receiptAt("k", clock.Now(), clock.Now())); err != nil {
		t.Fatal(err)
	}
	if receipt, why := store.Read("k"); receipt == nil {
		t.Fatal(why)
	}
}

func TestFailureNotesNeverMoveBackInTime(t *testing.T) {
	store, clock := newStore(t)
	store.NoteFailure("k", OutcomeFailed)
	newest := clock.Now()
	clock.Advance(time.Second)
	store.NoteFailure("k", OutcomeStopped)
	latest := clock.now
	notes, _ := filepath.Glob(filepath.Join(store.Folder, "k.failed.*"))
	if len(notes) != 2 {
		t.Fatalf("one note per failure: %v", notes)
	}
	when, found := store.FailureTime("k")
	if !found || when.Before(newest) || when.After(latest) {
		t.Fatalf("newest %v, got %v", latest, when)
	}
	// A note that cannot be read counts at its file's time; another key's notes do not count.
	damaged := filepath.Join(store.Folder, "j.failed.00000000000000000001-1")
	if err := os.WriteFile(damaged, []byte("?"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(damaged, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if when, found := store.FailureTime("j"); !found || !when.Equal(stamp) {
		t.Fatalf("damaged note: %v %v", when, found)
	}
	if _, found := store.FailureTime("none"); found {
		t.Fatal("a key without notes has a failure")
	}
	// An earlier jackstay's single note of exactly the prefix is still read.
	if err := os.WriteFile(filepath.Join(store.Folder, "old.failed"), []byte(`{"at_epoch": 1700000000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if when, found := store.FailureTime("old"); !found || when.Unix() != 1700000000 {
		t.Fatalf("old note: %v", when)
	}
}

func TestPruningRemovesWhatCanNoLongerChangeAnything(t *testing.T) {
	store, clock := newStore(t)
	if err := os.MkdirAll(store.Folder, 0o700); err != nil {
		t.Fatal(err)
	}
	now := clock.Now()
	files := map[string]time.Duration{
		"old.json": 80 * time.Hour, "old.failed.1-1": 80 * time.Hour, "old.json.1.partial": 80 * time.Hour, "old.failed": 80 * time.Hour,
		"young.json": 30 * time.Hour, "day.failed.2-2": 23 * time.Hour, "other.txt": 500 * time.Hour,
	}
	for name, age := range files {
		place := filepath.Join(store.Folder, name)
		if err := os.WriteFile(place, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(place, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	if removed := store.Prune(); removed != 4 {
		t.Fatalf("removed %d", removed)
	}
	for _, kept := range []string{"young.json", "day.failed.2-2", "other.txt"} {
		if _, err := os.Stat(filepath.Join(store.Folder, kept)); err != nil {
			t.Errorf("%s pruned", kept)
		}
	}
	// max_age_hours below a day still keeps a day.
	store.MaxAge = time.Hour
	if store.Prune() != 1 {
		t.Fatal("max_age_hours above the floor")
	}
	if _, err := os.Stat(filepath.Join(store.Folder, "day.failed.2-2")); err != nil {
		t.Fatal("a note younger than a day was pruned")
	}
	if MaxAge(map[string]any{"max_age_hours": 2.0}) != 2*time.Hour || MaxAge(nil) != 72*time.Hour ||
		MaxAge(map[string]any{"max_age_hours": -1.0}) != 72*time.Hour {
		t.Fatal("MaxAge")
	}
}

func TestRemovingAReceipt(t *testing.T) {
	store, clock := newStore(t)
	if removed, err := store.Remove("k"); removed || err != nil {
		t.Fatal("nothing to remove")
	}
	if _, err := store.Write(receiptAt("k", clock.Now(), clock.Now())); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.Remove("k"); !removed || err != nil {
		t.Fatal("not removed")
	}
	if removed, _ := store.Remove(""); removed {
		t.Fatal("no key")
	}
	partials, _ := filepath.Glob(filepath.Join(store.Folder, "*.partial"))
	if len(partials) != 0 {
		t.Fatalf("temporaries left: %v", partials)
	}
}
