package receipts

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
)

func TestCIReceiptProvenanceBindsTrustRunAndInput(t *testing.T) {
	folder := t.TempDir()
	receiptPath := filepath.Join(folder, "receipt.json")
	attestationPath := filepath.Join(folder, "attestation.json")
	privatePath := filepath.Join(folder, "signing-key.pem")
	publicPath := filepath.Join(folder, "trusted-key.pem")
	head := strings.Repeat("a", 40)
	key := strings.Repeat("b", 64)
	manifest := strings.Repeat("c", 64)
	receipt := portableEvidence{Schema: evidenceSchema, Jackstay: "1.2.3", Head: head, Key: key,
		Manifest: manifest, Outcome: OutcomePassed, Count: 12, Ended: time.Now().UTC().Format(time.RFC3339Nano)}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	attestArgs := []string{"receipts", "attest-ci", "--trust-root", "ci-v1", "--issuer", "github-actions",
		"--repository", "example/project", "--workflow", "gate.yml", "--run", "run-17", "--signing-key", privatePath, receiptPath}
	var output, diagnostic bytes.Buffer
	if status := cli.Dispatch(attestArgs, nil, &output, &diagnostic); status != 0 {
		t.Fatalf("attest exit %d: %s", status, &diagnostic)
	}
	originalAttestation := bytes.Clone(output.Bytes())
	if err := os.WriteFile(attestationPath, originalAttestation, 0o600); err != nil {
		t.Fatal(err)
	}
	verification := []string{"receipts", "verify-ci", "--trust-root", "ci-v1", "--public-key", publicPath,
		"--issuer", "github-actions", "--repository", "example/project", "--workflow", "gate.yml", "--run", "run-17",
		"--head", head, "--key", key, "--manifest", manifest, receiptPath, attestationPath}
	verify := func(args []string) int {
		t.Helper()
		output.Reset()
		diagnostic.Reset()
		return cli.Dispatch(args, nil, &output, &diagnostic)
	}
	if status := verify(verification); status != 0 || !strings.Contains(output.String(), "verified CI run run-17") {
		t.Fatalf("verify exit %d: %s", status, &diagnostic)
	}
	for _, test := range []struct {
		name  string
		flag  string
		value string
	}{
		{"root rotation", "--trust-root", "ci-v2"},
		{"issuer", "--issuer", "other-ci"},
		{"repository", "--repository", "example/other"},
		{"workflow", "--workflow", "other.yml"},
		{"run replay", "--run", "run-18"},
		{"source revision", "--head", strings.Repeat("d", 40)},
		{"policy key", "--key", strings.Repeat("e", 64)},
		{"manifest", "--manifest", strings.Repeat("f", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string(nil), verification...)
			args[slices.Index(args, test.flag)+1] = test.value
			if status := verify(args); status != 1 {
				t.Fatalf("accepted changed trust context with exit %d: %s", status, &output)
			}
		})
	}
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherDER, err := x509.MarshalPKIXPublicKey(otherPublic)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: otherDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := verify(verification); status != 1 {
		t.Fatalf("accepted a different trusted key: %d", status)
	}
	if err := os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	alteredReceipt := bytes.Replace(encoded, []byte(`"evidence_count":12`), []byte(`"evidence_count":13`), 1)
	if err := os.WriteFile(receiptPath, append(alteredReceipt, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := verify(verification); status != 1 {
		t.Fatalf("accepted changed report: %d", status)
	}
	if err := os.WriteFile(receiptPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	var altered ciAttestation
	if err := json.Unmarshal(originalAttestation, &altered); err != nil {
		t.Fatal(err)
	}
	altered.Claim.Run = "run-18"
	changed, err := json.Marshal(altered)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attestationPath, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	args := append([]string(nil), verification...)
	args[slices.Index(args, "--run")+1] = "run-18"
	if status := verify(args); status != 1 {
		t.Fatalf("accepted a modified signed claim: %d", status)
	}
}
