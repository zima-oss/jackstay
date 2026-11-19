//go:build integration

package profile_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/adopt"
	"github.com/zima-oss/jackstay/internal/cli"
)

func TestABundleAdoptsTwoCheckoutsOfflineWithIdenticalPolicy(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	producer := t.TempDir()
	profile := save(t, producer, "profile.json", []byte(`{
		"jackstay_profile": 1,
		"name": "team",
		"jackstay_json": {"defaults": {"procs": {"default_minutes": 42}}},
		"workflows": {"team-gate": {"file": "flows/gate.yml"}},
		"agents": {"rules": ["Run the team gate before publishing."]}
	}`))
	save(t, producer, "flows/gate.yml", []byte("name: team gate @@jackstay_version@@\n"))
	var output, errors bytes.Buffer
	if status := cli.Dispatch([]string{"profile", "export", profile}, nil, &output, &errors); status != 0 {
		t.Fatalf("export exit %d: %s", status, &errors)
	}
	bundled := bytes.Clone(output.Bytes())
	digest := checksum(bundled)
	if !strings.Contains(errors.String(), digest) {
		t.Fatal("the producer omitted the exact bundle digest")
	}
	output.Reset()
	errors.Reset()
	save(t, producer, "profile.json", []byte(`{"workflows":{"team-gate":{"file":"flows/gate.yml"}},
		"agents":{"rules":["Run the team gate before publishing."]},"name":"team",
		"jackstay_json":{"defaults":{"procs":{"default_minutes":42}}},"jackstay_profile":1}`))
	if status := cli.Dispatch([]string{"profile", "export", profile}, nil, &output, &errors); status != 0 || !bytes.Equal(output.Bytes(), bundled) {
		t.Fatalf("equivalent profile produced different bundle bytes, exit %d: %s", status, &errors)
	}
	output.Reset()
	errors.Reset()
	save(t, producer, "flows/gate.yml", []byte("name: updated team gate @@jackstay_version@@\n"))
	if status := cli.Dispatch([]string{"profile", "export", profile}, nil, &output, &errors); status != 0 {
		t.Fatalf("export upgraded profile exit %d: %s", status, &errors)
	}
	upgraded := bytes.Clone(output.Bytes())
	for _, source := range []string{profile, filepath.Join(producer, "flows/gate.yml")} {
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
	}

	first := adoptIn(t, bundled, upgraded, digest)
	second := adoptIn(t, bundled, upgraded, digest)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("the same bundle wrote different profile outputs: %v and %v", first, second)
	}
}

func adoptIn(t *testing.T, bundled, upgraded []byte, digest string) map[string]string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "init", "-q", "-b", "main", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	const configText = `{"jackstay_schema":2,"project":"same","procs":{"max_minutes":300}}`
	configPath := save(t, root, "jackstay.json", []byte(configText))
	path := save(t, root, "policy.bundle.json", bundled)
	var output, errors bytes.Buffer
	if status := cli.Dispatch([]string{"profile", "verify", "--sha256", digest, path}, nil, &output, &errors); status != 0 {
		t.Fatalf("offline verify exit %d: %s", status, &errors)
	}
	options := adopt.Options{Dir: root, WorkingDir: root, Profile: "policy.bundle.json",
		Version: "0.5.99", Has: cli.Has, SettingsOnly: true}
	plan, err := adopt.Make(options)
	if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Path != "jackstay.json" {
		t.Fatalf("settings preview: %+v %v", plan, err)
	}
	if data, err := os.ReadFile(configPath); err != nil || string(data) != configText {
		t.Fatalf("preview wrote or removed settings: %s, %v", data, err)
	}
	if err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	options.Profile, options.SettingsOnly = "", false
	plan, err = adopt.Make(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, name := range []string{"jackstay.json", "AGENTS.md", ".github/workflows/team-gate.yml"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(data)
	}
	if settings := files["jackstay.json"]; !strings.Contains(settings, `"pin": "v0.5.99"`) ||
		!strings.Contains(settings, `"max_minutes": 300`) || !strings.Contains(settings, `"default_minutes": 42`) {
		t.Fatalf("pin or defaults were not applied: %s", settings)
	}
	if !strings.Contains(files[".github/workflows/team-gate.yml"], "name: team gate v0.5.99") {
		t.Fatal("the offline template did not use the recorded pin")
	}

	tampered := bytes.Replace(bundled, []byte(`"name":"team"`), []byte(`"name":"changed"`), 1)
	if bytes.Equal(tampered, bundled) {
		t.Fatal("fixture did not change")
	}
	modified := save(t, root, "modified.json", tampered)
	if failVerify(t, modified, digest, "SHA-256 does not match") != 1 {
		t.Fatal("accepted a modified bundle")
	}
	if failVerify(t, modified, checksum(tampered), "content SHA-256 does not match") != 1 {
		t.Fatal("accepted a modified payload with a mismatched internal checksum")
	}
	unsupported := bytes.Replace(bundled, []byte(`"jackstay_profile_bundle":1`), []byte(`"jackstay_profile_bundle":99`), 1)
	if failVerify(t, save(t, root, "unsupported.json", unsupported), checksum(unsupported), "unsupported profile bundle format") != 1 {
		t.Fatal("accepted an unsupported bundle version")
	}
	options.Profile = "unsupported.json"
	if _, err := adopt.Make(options); err == nil {
		t.Fatal("unsupported bundle was accepted for adoption")
	}
	if data, err := os.ReadFile(configPath); err != nil || string(data) != files["jackstay.json"] {
		t.Fatalf("incompatible upgrade changed existing settings: %s %v", data, err)
	}
	newName := "policy-v2.bundle.json"
	newPath := save(t, root, newName, upgraded)
	output.Reset()
	errors.Reset()
	if status := cli.Dispatch([]string{"profile", "verify", "--sha256", checksum(upgraded), newPath}, nil, &output, &errors); status != 0 {
		t.Fatalf("verify upgraded profile exit %d: %s", status, &errors)
	}
	change := func(profile, version, previous string) {
		t.Helper()
		options.Profile, options.Version, options.Pin, options.SettingsOnly = profile, version, true, true
		plan, err := adopt.Make(options)
		if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Path != "jackstay.json" {
			t.Fatalf("upgrade settings preview: %+v %v", plan, err)
		}
		if data, err := os.ReadFile(configPath); err != nil || string(data) != previous {
			t.Fatalf("preview changed the existing settings: %s, %v", data, err)
		}
		if err := plan.Apply(); err != nil {
			t.Fatal(err)
		}
		options.Profile, options.Pin, options.SettingsOnly = "", false, false
		plan, err = adopt.Make(options)
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Apply(); err != nil {
			t.Fatal(err)
		}
	}
	change(newName, "0.6.0", files["jackstay.json"])
	updated, err := os.ReadFile(configPath)
	if err != nil || !bytes.Contains(updated, []byte(`"pin": "v0.6.0"`)) ||
		!bytes.Contains(updated, []byte(`"max_minutes": 300`)) {
		t.Fatalf("updated configuration did not preserve the local setting: %s %v", updated, err)
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github/workflows/team-gate.yml"))
	if err != nil || !bytes.Contains(workflow, []byte("name: updated team gate v0.6.0")) {
		t.Fatalf("the workflow was not updated: %s %v", workflow, err)
	}
	change("policy.bundle.json", "0.5.99", string(updated))
	for name, expected := range files {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != expected {
			t.Fatalf("rollback did not restore %s: %s %v", name, data, err)
		}
	}
	return files
}

func failVerify(t *testing.T, path, digest, reason string) int {
	t.Helper()
	var output, errors bytes.Buffer
	status := cli.Dispatch([]string{"profile", "verify", "--sha256", digest, path}, nil, &output, &errors)
	if !strings.Contains(errors.String(), reason) {
		t.Fatalf("no %q in %s", reason, &errors)
	}
	return status
}

func save(t *testing.T, root, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func checksum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
