package receipts

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

const attestCIUsage = `usage: jst receipts attest-ci --trust-root NAME --issuer ID --repository ID --workflow ID --run ID --signing-key KEY RECEIPT

Sign the exact exported receipt bytes and CI run identity with an Ed25519 private key in PKCS#8
PEM format. Keep the key outside Git. Write the attestation to stdout.

exit: 0 attested; 1 invalid receipt, key or write failure; 2 bad request.`

const verifyCIUsage = `usage: jst receipts verify-ci --trust-root NAME --public-key KEY --issuer ID --repository ID --workflow ID --run ID --head SHA --key KEY --manifest SHA RECEIPT ATTESTATION

Verify the Ed25519 attestation against a trusted public key and independently expected CI
identity, revision, receipt key and manifest. Both files are portable; no repository is needed.

exit: 0 verified; 1 invalid or mismatched evidence; 2 bad request.`

const ciEvidenceSchema = 1
const maxCIFileBytes = 4096

type ciClaim struct {
	Schema        int    `json:"schema"`
	TrustRoot     string `json:"trust_root"`
	Issuer        string `json:"issuer"`
	Repository    string `json:"repository"`
	Workflow      string `json:"workflow"`
	Run           string `json:"run"`
	Head          string `json:"head"`
	Key           string `json:"key"`
	Manifest      string `json:"manifest"`
	ReceiptSHA256 string `json:"receipt_sha256"`
}

type ciAttestation struct {
	Claim     ciClaim `json:"claim"`
	Signature string  `json:"signature"`
}

func init() {
	cli.Register(
		cli.Command{Name: "receipts attest-ci", Summary: "sign a portable receipt with its CI identity", Usage: attestCIUsage, PrivateArgs: true, Run: attestCI},
		cli.Command{Name: "receipts verify-ci", Summary: "verify a CI receipt against a named trust root", Usage: verifyCIUsage, PrivateArgs: true, Run: verifyCI},
	)
}

func ciIdentity(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func ciFile(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- caller-selected evidence or key
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("CI evidence and keys must be regular files")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("CI evidence or key is unreadable or too large")
	}
	return data, nil
}

func ciReceipt(path string) ([]byte, portableEvidence, error) {
	data, err := ciFile(path, maxCIFileBytes)
	if err != nil {
		return nil, portableEvidence{}, err
	}
	var receipt portableEvidence
	if strictjson.Unmarshal(data, &receipt) != nil || !receipt.valid() {
		return nil, portableEvidence{}, errors.New("receipt is not a counted portable pass")
	}
	return data, receipt, nil
}

func ed25519Private(path string) (ed25519.PrivateKey, error) {
	data, err := ciFile(path, 8192)
	if err != nil {
		return nil, err
	}
	block, remainder := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(remainder)) != 0 {
		return nil, errors.New("signing key must be one PKCS#8 PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	key, ok := parsed.(ed25519.PrivateKey)
	if err != nil || !ok {
		return nil, errors.New("signing key must be Ed25519")
	}
	return key, nil
}

func ed25519Public(path string) (ed25519.PublicKey, error) {
	data, err := ciFile(path, 8192)
	if err != nil {
		return nil, err
	}
	block, remainder := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(remainder)) != 0 {
		return nil, errors.New("trusted key must be one PKIX PEM public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	key, ok := parsed.(ed25519.PublicKey)
	if err != nil || !ok {
		return nil, errors.New("trusted key must be Ed25519")
	}
	return key, nil
}

func attestCI(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("receipts attest-ci", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("trust-root", "", "named trusted signing key")
	issuer := flags.String("issuer", "", "CI issuer")
	repository := flags.String("repository", "", "CI repository")
	workflow := flags.String("workflow", "", "CI workflow")
	run := flags.String("run", "", "CI run identity")
	keyPath := flags.String("signing-key", "", "Ed25519 private key")
	if flags.Parse(args) != nil || flags.NArg() != 1 || !ciIdentity(*root) || !ciIdentity(*issuer) ||
		!ciIdentity(*repository) || !ciIdentity(*workflow) || !ciIdentity(*run) || *keyPath == "" {
		fmt.Fprintln(stderr, attestCIUsage)
		return StatusUnusable
	}
	data, receipt, err := ciReceipt(flags.Arg(0))
	if err != nil {
		return evidenceError(stderr, err)
	}
	key, err := ed25519Private(*keyPath)
	if err != nil {
		return evidenceError(stderr, err)
	}
	claim := ciClaim{Schema: ciEvidenceSchema, TrustRoot: *root, Issuer: *issuer,
		Repository: *repository, Workflow: *workflow, Run: *run, Head: receipt.Head, Key: receipt.Key,
		Manifest: receipt.Manifest, ReceiptSHA256: sha256Text(data)}
	canonical, err := json.Marshal(claim)
	if err != nil {
		return evidenceError(stderr, err)
	}
	attestation := ciAttestation{Claim: claim, Signature: hex.EncodeToString(ed25519.Sign(key, canonical))}
	encoded, err := json.Marshal(attestation)
	if err != nil {
		return evidenceError(stderr, err)
	}
	if _, err := stdout.Write(append(encoded, '\n')); err != nil {
		return evidenceError(stderr, err)
	}
	return 0
}

func verifyCI(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("receipts verify-ci", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("trust-root", "", "expected trust root name")
	publicPath := flags.String("public-key", "", "trusted Ed25519 public key")
	issuer := flags.String("issuer", "", "expected CI issuer")
	repository := flags.String("repository", "", "expected CI repository")
	workflow := flags.String("workflow", "", "expected CI workflow")
	run := flags.String("run", "", "expected CI run")
	head := flags.String("head", "", "expected source revision")
	key := flags.String("key", "", "expected receipt policy key")
	manifest := flags.String("manifest", "", "expected input manifest")
	if flags.Parse(args) != nil || flags.NArg() != 2 || !ciIdentity(*root) || !ciIdentity(*issuer) ||
		!ciIdentity(*repository) || !ciIdentity(*workflow) || !ciIdentity(*run) || *publicPath == "" ||
		!fullHex(*head, 40, 64) || !fullHex(*key, 64) || !fullHex(*manifest, 64) {
		fmt.Fprintln(stderr, verifyCIUsage)
		return StatusUnusable
	}
	data, receipt, err := ciReceipt(flags.Arg(0))
	if err != nil {
		return evidenceError(stderr, err)
	}
	attestationData, err := ciFile(flags.Arg(1), maxCIFileBytes)
	if err != nil {
		return evidenceError(stderr, err)
	}
	var attestation ciAttestation
	if err := strictjson.Unmarshal(attestationData, &attestation); err != nil {
		return evidenceError(stderr, err)
	}
	claim := attestation.Claim
	if claim.Schema != ciEvidenceSchema || claim.TrustRoot != *root || claim.Issuer != *issuer ||
		claim.Repository != *repository || claim.Workflow != *workflow || claim.Run != *run || claim.Head != *head || claim.Key != *key ||
		claim.Manifest != *manifest || claim.Head != receipt.Head || claim.Key != receipt.Key ||
		claim.Manifest != receipt.Manifest || claim.ReceiptSHA256 != sha256Text(data) {
		return evidenceError(stderr, errors.New("CI claim does not match the expected run and receipt"))
	}
	public, err := ed25519Public(*publicPath)
	if err != nil {
		return evidenceError(stderr, err)
	}
	signature, err := hex.DecodeString(attestation.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return evidenceError(stderr, errors.New("CI signature is invalid"))
	}
	canonical, err := json.Marshal(claim)
	if err != nil || !ed25519.Verify(public, canonical, signature) {
		return evidenceError(stderr, errors.New("CI signature does not match the trusted key"))
	}
	fmt.Fprintf(stdout, "jst receipts: verified CI run %s for HEAD %s under %s\n", claim.Run, claim.Head, claim.TrustRoot)
	return 0
}
