package judge

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/supervise"
)

func goSelectedReader(t *testing.T, selection GoSelection, durations bool) *Reader {
	t.Helper()
	reader, err := New(Options{Kind: GoJSON, GoSelection: &selection, GoDurations: durations})
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func feedGoSelection(t *testing.T, reader *Reader, events ...string) {
	t.Helper()
	for _, event := range events {
		if err := reader.Line(supervise.Stdout, event); err != nil {
			t.Fatal(err)
		}
	}
}

func goElapsed(action, pkg, name string, seconds float64) string {
	return fmt.Sprintf(`{"Action":%q,"Package":%q,"Test":%q,"Elapsed":%g}`, action, pkg, name, seconds)
}

func TestGoSelectionCountsOnlyTheWitnessedDescendant(t *testing.T) {
	selection := GoSelection{Package: "example.invalid/fixture", Name: "TestParent/child"}
	reader := goSelectedReader(t, selection, true)
	feedGoSelection(t, reader,
		goEvent("start", selection.Package, "", ""),
		goEvent("run", selection.Package, "TestParent", ""),
		goEvent("run", selection.Package, selection.Name, ""),
		goElapsed("pass", selection.Package, selection.Name, 0.25),
		goElapsed("pass", selection.Package, "TestParent", 0.3),
		goEvent("pass", selection.Package, "", ""),
	)
	verdict := reader.Verdict(End{})
	if verdict.Outcome != Passed || verdict.Counted != 1 {
		t.Fatalf("selected subtest: %+v", verdict)
	}
	samples, truncated := reader.GoDurations()
	if truncated || len(samples) != 1 || samples[0] != (GoCaseDuration{selection.Package, selection.Name, 0.25}) {
		t.Fatalf("selected subtest duration: %+v, truncated %t", samples, truncated)
	}
}

func TestGoSelectionAnAncestorAloneIsNoMatch(t *testing.T) {
	selection := GoSelection{Package: "fixture", Name: "TestParent/missing"}
	reader := goSelectedReader(t, selection, true)
	feedGoSelection(t, reader,
		goEvent("start", "fixture", "", ""),
		goEvent("run", "fixture", "TestParent", ""),
		goElapsed("pass", "fixture", "TestParent", 0.01),
		goEvent("pass", "fixture", "", ""),
	)
	if verdict := reader.Verdict(End{}); verdict.Outcome != NothingRun || verdict.Counted != 0 {
		t.Fatalf("ancestor-only outcome: %+v", verdict)
	}
	if samples, truncated := reader.GoDurations(); truncated || len(samples) != 0 {
		t.Fatalf("ancestor-only timing: %+v, truncated %t", samples, truncated)
	}
}

func TestGoSelectionRejectsOutsideRunsWithoutHidingFailures(t *testing.T) {
	selection := GoSelection{Package: "fixture", Name: "TestWanted"}
	reader := goSelectedReader(t, selection, true)
	feedGoSelection(t, reader,
		goEvent("start", "fixture", "", ""),
		goEvent("run", "fixture", "TestOther", ""),
		goEvent("fail", "fixture", "TestOther", ""),
		goEvent("fail", "fixture", "", ""),
	)
	if verdict := reader.Verdict(End{}); verdict.Outcome != Failed || verdict.Counted != 0 {
		t.Fatalf("outside failing case: %+v", verdict)
	}
	reader = goSelectedReader(t, selection, false)
	feedGoSelection(t, reader,
		goEvent("start", "fixture", "", ""),
		goEvent("run", "fixture", "TestOther", ""),
		goEvent("pass", "fixture", "TestOther", ""),
		goEvent("pass", "fixture", "", ""),
	)
	if verdict := reader.Verdict(End{}); verdict.Outcome != Unjudged ||
		!strings.Contains(verdict.Why, "outside") {
		t.Fatalf("outside passing case: %+v", verdict)
	}
}

func TestGoSelectionRequiresTheSelectedPackage(t *testing.T) {
	reader := goSelectedReader(t, GoSelection{Package: "wanted", Name: "TestCase"}, false)
	feedGoSelection(t, reader, goEvent("start", "other", "", ""), goEvent("pass", "other", "", ""))
	if verdict := reader.Verdict(End{}); verdict.Outcome != Unjudged ||
		!strings.Contains(verdict.Why, "selected Go package") {
		t.Fatalf("missing selected package: %+v", verdict)
	}
}

func TestGoDurationsPreserveRepeatsAndCapSamples(t *testing.T) {
	reader := goSelectedReader(t, GoSelection{Package: "fixture", Name: "TestCase"}, true)
	feedGoSelection(t, reader, goEvent("start", "fixture", "", ""))
	for index := range maxGoDurationSamples + 1 {
		feedGoSelection(t, reader, goEvent("run", "fixture", "TestCase", ""),
			goElapsed("pass", "fixture", "TestCase", float64(index)/10))
	}
	feedGoSelection(t, reader, goEvent("pass", "fixture", "", ""))
	samples, truncated := reader.GoDurations()
	if !truncated || len(samples) != maxGoDurationSamples || samples[0].Seconds != 0 ||
		samples[1].Seconds != 0.1 || samples[len(samples)-1].Seconds != 51.1 {
		t.Fatalf("duration sample cap: %d samples, truncated %t", len(samples), truncated)
	}
	samples[0].Seconds = 100
	again, _ := reader.GoDurations()
	if again[0].Seconds != 0 {
		t.Fatal("duration samples were returned by reference")
	}
}

func TestGoDurationsRejectInvalidOrIncompleteStreams(t *testing.T) {
	for _, event := range []string{
		goElapsed("pass", "fixture", "TestCase", -1),
		`{"Action":"pass","Package":"fixture","Test":"TestCase","Elapsed":1e309}`,
	} {
		reader := goSelectedReader(t, GoSelection{Package: "fixture", Name: "TestCase"}, true)
		feedGoSelection(t, reader, goEvent("start", "fixture", "", ""),
			goEvent("run", "fixture", "TestCase", ""), event)
		if verdict := reader.Verdict(End{}); verdict.Outcome != Unjudged {
			t.Fatalf("invalid elapsed event %q: %+v", event, verdict)
		}
		if samples, truncated := reader.GoDurations(); truncated || len(samples) != 0 {
			t.Fatalf("invalid samples %+v, truncated %t", samples, truncated)
		}
	}
}

func TestGoJSONWithoutTimingsIgnoresAnUnusableElapsedField(t *testing.T) {
	reader, err := New(Options{Kind: GoJSON})
	if err != nil {
		t.Fatal(err)
	}
	feedGoSelection(t, reader,
		goEvent("start", "fixture", "", ""),
		goEvent("run", "fixture", "TestCase", ""),
		`{"Action":"pass","Package":"fixture","Test":"TestCase","Elapsed":1e309}`,
		goEvent("pass", "fixture", "", ""),
	)
	if verdict := reader.Verdict(End{}); verdict.Outcome != Passed || verdict.Counted != 1 {
		t.Fatalf("baseline Go evidence changed: %+v", verdict)
	}
}

func TestGoSelectionOptionsRequireGoJSONAndAValidIdentity(t *testing.T) {
	for _, options := range []Options{
		{Kind: Exit, GoSelection: &GoSelection{Package: "fixture", Name: "TestCase"}},
		{Kind: Exit, GoDurations: true},
		{Kind: GoJSON, GoSelection: &GoSelection{Package: "", Name: "TestCase"}},
		{Kind: GoJSON, GoSelection: &GoSelection{Package: "fixture", Name: "TestCase//child"}},
	} {
		if _, err := New(options); err == nil {
			t.Fatalf("accepted invalid options %+v", options)
		}
	}
}
