package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pinned = "      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n"

func TestTheExampleProfileInJackstaysDocsIsValidAndUsesEveryCapability(t *testing.T) {
	loaded, err := Load(filepath.Join("..", "..", "..", "docs", "profiles", "example", "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "example" || loaded.About == "" || len(loaded.Required) == 0 || len(loaded.Forbidden) == 0 ||
		len(loaded.RequiredScopes) == 0 || len(loaded.RunWith) == 0 || len(loaded.RequiredKeys) == 0 ||
		len(loaded.Defaults) == 0 || len(loaded.Workflows) == 0 || len(loaded.Values) == 0 ||
		len(loaded.Rules) == 0 || len(loaded.LeaveOut) == 0 {
		t.Fatalf("the example leaves a capability out: %+v", loaded)
	}
	if loaded.Forbidden[0].Instead == "" || loaded.Workflows[0].Text == "" || len(loaded.Workflows[0].When) == 0 {
		t.Fatalf("%+v", loaded)
	}
}

func write(t *testing.T, folder, name, text string) string {
	t.Helper()
	path := filepath.Join(folder, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAProfileThatCannotBeUsedIsRefusedWithItsReason(t *testing.T) {
	cases := map[string]struct{ profile, template, want string }{
		"unknown key":       {`{"jackstay_profile": 1, "name": "x", "color": "red"}`, "", "unknown key `color`"},
		"format":            {`{"jackstay_profile": 2, "name": "x"}`, "", "`jackstay_profile` must be 1"},
		"no name":           {`{"jackstay_profile": 1}`, "", "`name` must be a non-empty string"},
		"path leaves":       {`{"jackstay_profile": 1, "name": "x", "files": {"required": [{"path": "../x", "why": "w"}]}}`, "", "clean relative path"},
		"forbidden instead": {`{"jackstay_profile": 1, "name": "x", "files": {"forbidden": [{"path": "a", "why": "w"}]}}`, "", "instead"},
		"default type":      {`{"jackstay_profile": 1, "name": "x", "jackstay_json": {"defaults": {"procs": {"default_minutes": "many"}}}}`, "", "procs.default_minutes"},
		"default unknown":   {`{"jackstay_profile": 1, "name": "x", "jackstay_json": {"defaults": {"nothing": {}}}}`, "", "unknown key `nothing`"},
		"default adopt":     {`{"jackstay_profile": 1, "name": "x", "jackstay_json": {"defaults": {"adopt": {}}}}`, "", "does not set adopt"},
		"leave out":         {`{"jackstay_profile": 1, "name": "x", "agents": {"leave_out": ["nothing"]}}`, "", "no item of the block"},
		"jackstay's value":  {`{"jackstay_profile": 1, "name": "x", "values": {"jackstay_version": "v1"}}`, "", "a name jackstay defines itself"},
		"unpinned":          {`{"jackstay_profile": 1, "name": "x", "workflows": {"w": {"file": "w.yml"}}}`, "      - uses: actions/checkout@v4\n", "not pinned"},
		"undefined value":   {`{"jackstay_profile": 1, "name": "x", "workflows": {"w": {"file": "w.yml"}}}`, pinned + "runs-on: @@runner@@\n", "@@runner@@"},
		"template outside":  {`{"jackstay_profile": 1, "name": "x", "workflows": {"w": {"file": "../w.yml"}}}`, "", "clean relative path"},
		"when":              {`{"jackstay_profile": 1, "name": "x", "workflows": {"w": {"file": "w.yml", "when": ["nothing"]}}}`, pinned, "no section"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			folder := t.TempDir()
			path := write(t, folder, "profile.json", testCase.profile)
			if testCase.template != "" {
				write(t, folder, "w.yml", testCase.template)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("want %q, got %v", testCase.want, err)
			}
		})
	}
}

func TestAProfilesValuesAndWorkflowsAreRead(t *testing.T) {
	folder := t.TempDir()
	write(t, folder, "flows/b.yml", pinned+"runs-on: @@runner@@ @@jackstay_version@@\n")
	write(t, folder, "flows/a.yml", "name: a\n")
	path := write(t, folder, "profile.json", `{"jackstay_profile": 1, "name": "x",
		"values": {"runner": "self-hosted"},
		"workflows": {"b": {"file": "flows/b.yml", "when": ["release"]}, "a": {"file": "flows/a.yml"}}}`)
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Workflows) != 2 || loaded.Workflows[0].Name != "a" || loaded.Workflows[1].When[0] != "release" {
		t.Fatalf("%+v", loaded.Workflows)
	}
}
