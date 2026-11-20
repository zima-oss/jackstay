package receipts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

const exportUsage = `usage: jst receipts export KEY

Writes the record of a stored counted pass (with its HEAD and input manifest) to standard output,
and the SHA-256 of its exact bytes to standard error.

exit: 0 exported; 1 no usable export or a read/write failure; 2 bad request.

example: jst receipts export "$receipt_key" > evidence.json`

const verifyUsage = `usage: jst receipts verify --sha256 DIGEST --head SHA --key KEY FILE

Take DIGEST, SHA and KEY from a source other than FILE. Needs no receipt store or Git repository.

exit: 0 verified; 1 unverified or unreadable evidence; 2 bad request.

example: jst receipts verify --sha256 "$expected_digest" --head "$expected_head" --key "$expected_key" evidence.json`

const evidenceSchema = 1

// portableEvidence is deliberately separate from Receipt: command text, paths, environment,
// and toolchain output must not leave the producer's store through this command.
type portableEvidence struct {
	Schema   int    `json:"schema"`
	Jackstay string `json:"jackstay"`
	Head     string `json:"head"`
	Key      string `json:"key"`
	Manifest string `json:"manifest"`
	Outcome  string `json:"outcome"`
	Count    int    `json:"evidence_count"`
	Ended    string `json:"ended"`
}

func init() {
	cli.Register(
		cli.Command{Name: "receipts export", Summary: "write a portable record of a stored test pass", Usage: exportUsage, Run: exportEvidence},
		cli.Command{Name: "receipts verify", Summary: "verify a portable test record against expected digests and revision", Usage: verifyUsage, Run: verifyEvidence},
	)
}

func sha256Text(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fullHex(value string, sizes ...int) bool {
	for _, size := range sizes {
		if len(value) == size && strings.ToLower(value) == value {
			_, err := hex.DecodeString(value)
			return err == nil
		}
	}
	return false
}

func (evidence portableEvidence) valid() bool {
	_, err := time.Parse(time.RFC3339Nano, evidence.Ended)
	return evidence.Schema == evidenceSchema && evidence.Outcome == OutcomePassed && evidence.Count > 0 &&
		evidence.Jackstay != "" && fullHex(evidence.Head, 40, 64) &&
		fullHex(evidence.Key, 64) && fullHex(evidence.Manifest, 64) && err == nil
}

func exportEvidence(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || !fullHex(args[0], 64) {
		fmt.Fprintln(stderr, exportUsage)
		return StatusUnusable
	}
	data, err := readEvidence(args[0], "")
	if err != nil {
		return evidenceError(stderr, err)
	}
	if _, err := stdout.Write(data); err != nil {
		return evidenceError(stderr, err)
	}
	fmt.Fprintln(stderr, "jst receipts: sha256", sha256Text(data))
	return 0
}

// ExportForRun returns the same portable evidence as receipts export when the
// stored receipt came from the given local run. The run ID stays in the store.
func ExportForRun(key, runID string) ([]byte, error) {
	if !fullHex(key, 64) || runID == "" {
		return nil, fmt.Errorf("a receipt key and run ID are required")
	}
	return readEvidence(key, runID)
}

func readEvidence(key, runID string) ([]byte, error) {
	runner, err := newTester(io.Discard, io.Discard)
	if err != nil {
		return nil, err
	}
	root, loaded, err := config.Load(runner.here, Needs)
	if err != nil {
		return nil, err
	}
	if root == "" {
		return nil, fmt.Errorf("no %s at or above this folder", config.Name)
	}
	projectName := config.ProjectName(root, loaded, runner.here)
	store, err := OpenStore(projectName, Section(loaded), runner.clock)
	if err != nil {
		return nil, err
	}
	receipt, why := store.Read(key)
	if receipt == nil {
		return nil, fmt.Errorf("no exportable receipt: %s", why)
	}
	if runID != "" && receipt.RunID != runID {
		return nil, fmt.Errorf("the stored receipt belongs to a different run")
	}
	evidence := portableEvidence{Schema: evidenceSchema, Jackstay: receipt.Jackstay, Head: receipt.Head,
		Key: receipt.Key, Manifest: receipt.Material.Manifest, Outcome: receipt.Outcome,
		Count: receipt.EvidenceCount, Ended: fromEpoch(receipt.EndedEpoch).UTC().Format(time.RFC3339Nano)}
	if !receipt.matchesMaterial() || receipt.Project != projectName || !evidence.valid() {
		return nil, fmt.Errorf("the receipt has inconsistent key material or no counted pass tied to a checkout")
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func verifyEvidence(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("receipts verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	digest := flags.String("sha256", "", "expected file digest")
	head := flags.String("head", "", "expected checkout HEAD")
	key := flags.String("key", "", "expected receipt key")
	if flags.Parse(args) != nil || flags.NArg() != 1 || !fullHex(*digest, 64) ||
		!fullHex(*head, 40, 64) || !fullHex(*key, 64) {
		fmt.Fprintln(stderr, verifyUsage)
		return StatusUnusable
	}
	info, err := os.Stat(flags.Arg(0))
	if err != nil {
		return evidenceError(stderr, err)
	}
	if !info.Mode().IsRegular() {
		return evidenceError(stderr, fmt.Errorf("evidence must be a regular file"))
	}
	file, err := os.OpenFile(flags.Arg(0), os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- the evidence file the user selected
	if err != nil {
		return evidenceError(stderr, err)
	}
	defer func() { _ = file.Close() }()
	if opened, err := file.Stat(); err != nil || !opened.Mode().IsRegular() {
		return evidenceError(stderr, fmt.Errorf("evidence must be a regular file"))
	}
	const maxEvidenceBytes = 4096
	data, err := io.ReadAll(io.LimitReader(file, maxEvidenceBytes+1))
	if err != nil {
		return evidenceError(stderr, err)
	}
	if len(data) > maxEvidenceBytes || sha256Text(data) != *digest {
		return evidenceError(stderr, fmt.Errorf("evidence size or SHA-256 does not match"))
	}
	var evidence portableEvidence
	if err := strictjson.Unmarshal(data, &evidence); err != nil {
		return evidenceError(stderr, err)
	}
	if !evidence.valid() || evidence.Key != *key || evidence.Head != *head {
		return evidenceError(stderr, fmt.Errorf("evidence is invalid or does not match the expected HEAD and receipt key"))
	}
	fmt.Fprintf(stdout, "jst receipts: verified %d tests for HEAD %s\n", evidence.Count, evidence.Head)
	return 0
}

func evidenceError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "jst receipts: %v\n", err)
	return 1
}
