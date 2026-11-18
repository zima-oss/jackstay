package release

import (
	"strings"
	"testing"
)

func TestVersions(t *testing.T) {
	cases := []struct{ last, bump, want string }{
		{"0.4.2", "major", "0.5.0"}, {"0.4.2", "minor", "0.5.0"}, {"0.4.2", "patch", "0.4.3"},
		{"1.4.2", "major", "2.0.0"}, {"1.4.2", "minor", "1.5.0"}, {"1.4.2", "patch", "1.4.3"}, {"1.4.2", "", "1.4.2"},
	}
	for _, c := range cases {
		last, err := ParseVersion(c.last)
		if err != nil {
			t.Fatal(err)
		}
		if got := last.Next(c.bump).String(); got != c.want {
			t.Errorf("%s %q: got %s want %s", c.last, c.bump, got, c.want)
		}
	}
	for _, bad := range []string{"1.0", "01.0.0", "1.0.0-rc1", "v1.0.0", ""} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRequestEncodeIsStable(t *testing.T) {
	one, two, three, commit := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("a", 40), strings.Repeat("c", 40)
	r := Request{Version: "0.2.0", Entries: map[string]string{"b.md": two, "a.md": one}, Override: &three, MadeOn: commit}
	want := `{"version": "0.2.0", "entries": {"a.md": "` + one + `", "b.md": "` + two + `"}, "override": "` + three + `", "made_on": "` + commit + `"}` + "\n"
	if got := string(r.Encode()); got != want {
		t.Fatalf("got %s", got)
	}
	back, err := DecodeRequest(r.Encode(), "sha1")
	if err != nil || back.Version != "0.2.0" || *back.Override != three || back.Entries["a.md"] != one {
		t.Fatalf("got %+v %v", back, err)
	}
	for _, bad := range []string{
		`{"version": "0.2", "entries": {}, "override": null, "made_on": "` + commit + `"}`,
		`{"version": "0.2.0", "entries": {}, "override": null, "made_on": "` + commit + `", "more": 1}`,
		`{"version": "0.2.0", "entries": {"Bad.md": "` + one + `"}, "override": null, "made_on": "` + commit + `"}`,
		`{"version": "0.2.0", "entries": {}, "made_on": "` + commit + `"}`,
		`{"version": "0.2.0", "entries": {"a.md": "x"}, "override": null, "made_on": "` + commit + `"}`,
		`{"version": "0.2.0", "entries": {}, "override": null, "made_on": "x"}`,
	} {
		if _, err := DecodeRequest([]byte(bad), "sha1"); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := DecodeRequest(r.Encode(), "sha256"); err == nil {
		t.Error("sha1 ids accepted in a sha256 repository")
	}
}

func TestVersionFileReadsItsResult(t *testing.T) {
	good, problem := versionFile(map[string]any{"path": "VERSION", "match": `^(?P<version>[0-9]+\.[0-9]+\.[0-9]+)$`}, "version", "x")
	if problem != "" {
		t.Fatal(problem)
	}
	out, err := good.replace([]byte("0.1.0\n"), "0.2.0")
	if err != nil || string(out) != "0.2.0\n" {
		t.Fatalf("%q %v", out, err)
	}
	pinned, _ := versionFile(map[string]any{"path": "VERSION", "match": `^(?P<version>0\.1\.0)$`}, "version", "x")
	if _, err := pinned.replace([]byte("0.1.0\n"), "0.2.0"); err == nil {
		t.Fatal("a pattern that cannot read its result was accepted")
	}
	if _, problem := versionFile(map[string]any{"path": "V", "match": `(?P<other>.)`}, "version", "x"); problem == "" {
		t.Fatal("a pattern without the group was accepted")
	}
}

func TestSettingsRefuseConfigurationAsOutput(t *testing.T) {
	loaded := map[string]any{
		"changes": map[string]any{"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"},
		"release": map[string]any{"first": "0.1.0", "files": []any{map[string]any{"path": "jackstay.json", "match": `"v": "(?P<version>[^"]+)"`}}},
	}
	_, problems := SettingsOf(loaded)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, " "), "configuration") {
		t.Fatalf("got %q", problems)
	}
}

// --- an end-to-end run against a local bare repository and a fake GitHub ---

func TestUnmatchedGroupIsAnErrorNotAPanic(t *testing.T) {
	file, problem := versionFile(map[string]any{"path": "V", "match": `^(?P<version>[0-9]+\.[0-9]+\.[0-9]+)?$`}, "version", "x")
	if problem != "" {
		t.Fatal(problem)
	}
	if _, err := file.captures([]byte("\n")); err == nil {
		t.Fatal("no error")
	}
	if _, err := file.replace([]byte("\n"), "1.0.0"); err == nil {
		t.Fatal("no error")
	}
}

func TestNamesBeginningWithADashAreRefused(t *testing.T) {
	loaded := map[string]any{
		"changes": map[string]any{"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"},
		"release": map[string]any{"first": "0.1.0", "tag": "-v{version}", "files": []any{map[string]any{"path": "VERSION", "match": `^(?P<version>.+)$`}}},
	}
	if _, problems := SettingsOf(loaded); !strings.Contains(strings.Join(problems, " "), "must not begin with `-`") {
		t.Fatalf("got %q", problems)
	}
}
