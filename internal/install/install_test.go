package install

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/layout"
)

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		left, right string
		want        int
	}{{"0.5.0", "0.4.3", 1}, {"0.4.3", "0.5.0", -1}, {"0.10.0", "0.9.9", 1}, {"1.0", "1.0.0", 0}, {"0.4.3", "0.4.3", 0}} {
		if got := Compare(tc.left, tc.right); got != tc.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tc.left, tc.right, got, tc.want)
		}
	}
}

func TestGoingBack(t *testing.T) {
	live := func(text string) layout.State {
		return layout.State{Live: &layout.Folder{Version: text}}
	}
	prefix := t.TempDir()
	if problem := goingBack(live("0.5.0 (abc)"), "0.4.3 (626b40c)", prefix); !strings.Contains(problem, "only moves forward") {
		t.Errorf("an older version taken: %q", problem)
	}
	if problem := goingBack(live("0.5.0 (abc)"), "0.5.1 (def)", prefix); problem != "" {
		t.Errorf("a newer version refused: %s", problem)
	}
	if problem := goingBack(live("0.5.0 (abc)"), "0.5.0 (def)", prefix); problem != "" {
		t.Errorf("one version from two commits refused outside the account's prefix: %s", problem)
	}
	if problem := goingBack(live("0.5.0 (abc)"), "0.5.0 (def)", AccountPrefix()); !strings.Contains(problem, "W-020") {
		t.Errorf("one version from two commits taken into the account's prefix: %q", problem)
	}
	// A `go install …@v0.5.0` build is named by its tag; no binary says which commit that is, so it
	// and a build named by a hash are two commits of one version, in either order.
	for _, pair := range [][2]string{{"0.5.0 (abc)", "0.5.0 (v0.5.0)"}, {"0.5.0 (v0.5.0)", "0.5.0 (abc)"}} {
		if problem := goingBack(live(pair[0]), pair[1], AccountPrefix()); !strings.Contains(problem, "W-020") {
			t.Errorf("%s over %s taken: %q", pair[1], pair[0], problem)
		}
	}
	if problem := goingBack(layout.State{}, "0.1.0 (a)", prefix); problem != "" {
		t.Errorf("a fresh prefix refused: %s", problem)
	}
}

func TestFindCheckout(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+Module+"\n\ngo 1.27.0\n"), 0o644)
	deep := filepath.Join(root, "internal", "x")
	os.MkdirAll(deep, 0o755)
	if got := FindCheckout(deep); got != root {
		t.Fatalf("FindCheckout = %q", got)
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/other\n"), 0o644)
	if got := FindCheckout(other); got != "" && got == other {
		t.Fatalf("another module taken for jst: %q", got)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"--checkout", "a", "--binary", "b"}, {"--nope"}} {
		var stderr bytes.Buffer
		if code := Run(args, nil, &stderr, &stderr); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	var stderr bytes.Buffer
	if code := RunUninstall([]string{"--prefix"}, nil, &stderr, &stderr); code != 2 {
		t.Errorf("uninstall --prefix with no folder: exit %d", code)
	}
}

func TestACheckoutIsInstalledOnlyAfterItsUncoveredTestsPassed(t *testing.T) {
	asked := ""
	refusing := Options{Prefix: AccountPrefix(), Uncovered: func(checkout, commit string) error {
		asked = checkout + " " + commit
		return errors.New("no passed `jst test uncovered`")
	}}
	if err := refusing.uncoveredPassed("/work/jackstay", "abc1234"); err == nil || asked != "/work/jackstay abc1234" {
		t.Fatalf("refused %v after asking %q", err, asked)
	}
	accepting := Options{Prefix: AccountPrefix(), Uncovered: func(string, string) error { return nil }}
	if err := accepting.uncoveredPassed("/work/jackstay", "abc1234"); err != nil {
		t.Fatal(err)
	}
	for _, each := range []struct {
		prefix, source, commit string
	}{
		{AccountPrefix(), "release", "abc1234"},
		{AccountPrefix(), "binary /tmp/jst", "abc1234"},
		{AccountPrefix(), "/work/jackstay", "abc1234+changes"},
		{filepath.Join(t.TempDir(), "trial"), "/work/jackstay", "abc1234"},
	} {
		asked = ""
		trial := refusing
		trial.Prefix = each.prefix
		if err := trial.uncoveredPassed(each.source, each.commit); err != nil || asked != "" {
			t.Errorf("%+v: %v (asked %q)", each, err, asked)
		}
	}
}
