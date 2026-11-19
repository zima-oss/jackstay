package receipts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/version"
)

func TestPortableEvidenceAcrossSeparateStores(t *testing.T) {
	producer := t.TempDir()
	if err := os.WriteFile(filepath.Join(producer, "jackstay.json"),
		[]byte(`{"jackstay_schema":2,"project":"producer"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(producer)
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	clock := supervise.RealClock()
	store, err := OpenStore("producer", nil, clock)
	if err != nil {
		t.Fatal(err)
	}
	const privateMarker = "private-fixture-value"
	material := Material{Schema: Schema, Project: "producer", Jackstay: version.Version,
		Command: []string{"test-program", privateMarker}, Manifest: strings.Repeat("b", 64),
		Facts: Facts{Env: map[string]string{"PRIVATE_INPUT": privateMarker}}}
	encoded, err := json.Marshal(material)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	key := sha256Text(encoded)
	now := clock.Now()
	receipt := Receipt{Schema: Schema, Project: "producer", Jackstay: version.Version, Head: head,
		Outcome: OutcomePassed, EvidenceCount: 3, Material: material, Key: key,
		StartedEpoch: epochOf(now.Add(-time.Second)), EndedEpoch: epochOf(now)}
	if _, err := store.Write(receipt); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if status := cli.Dispatch([]string{"receipts", "export", key}, nil, &stdout, &stderr); status != 0 {
		t.Fatalf("export exit %d: %s", status, &stderr)
	}
	data := bytes.Clone(stdout.Bytes())
	digest := sha256Text(data)
	if bytes.Contains(data, []byte(privateMarker)) || bytes.Contains(data, []byte("test-program")) || !strings.Contains(stderr.String(), digest) {
		t.Fatal("the export disclosed private inputs or omitted the independent digest")
	}

	consumer := t.TempDir()
	t.Chdir(consumer)
	t.Setenv("JACKSTAY_HOME", filepath.Join(consumer, "unused-state"))
	path := filepath.Join(consumer, "evidence.json")
	verify := func(data []byte, digest, head, key string) int {
		t.Helper()
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		stdout.Reset()
		stderr.Reset()
		return cli.Dispatch([]string{"receipts", "verify", "--sha256", digest, "--head", head, "--key", key, path}, nil, &stdout, &stderr)
	}
	if status := verify(data, digest, head, key); status != 0 || !strings.Contains(stdout.String(), "verified 3 tests") {
		t.Fatalf("verify exit %d: %s", status, &stderr)
	}
	for _, test := range []struct {
		name   string
		data   []byte
		digest string
		head   string
		key    string
	}{
		{"bytes modified", append(bytes.Clone(data), '\n'), digest, head, key},
		{"different HEAD", data, digest, strings.Repeat("c", 40), key},
		{"different policy key", data, digest, head, strings.Repeat("d", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if status := verify(test.data, test.digest, test.head, test.key); status != 1 {
				t.Fatalf("verify exit %d: %s", status, &stderr)
			}
		})
	}
	for _, outcome := range []string{OutcomeReused, OutcomeUnjudged} {
		altered := bytes.Replace(data, []byte(`"outcome":"passed"`), []byte(`"outcome":"`+outcome+`"`), 1)
		if status := verify(altered, sha256Text(altered), head, key); status != 1 {
			t.Fatalf("verified a %s outcome: %s", outcome, &stdout)
		}
	}
}
