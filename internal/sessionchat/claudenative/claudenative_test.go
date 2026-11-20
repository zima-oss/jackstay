package claudenative

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
)

func keys(value map[string]any, prefix string) []string {
	var found []string
	for key, inner := range value {
		found = append(found, prefix+key)
		if nested, ok := inner.(map[string]any); ok {
			found = append(found, keys(nested, prefix+key+".")...)
		}
	}
	slices.Sort(found)
	return found
}

// The doorbell's line has the shape Claude Code's own sender writes (testdata/), less the sender's
// own address, which jackstay has none of, and less any auth line.
func TestTheLineHasTheCapturedShape(t *testing.T) {
	for _, version := range VerifiedVersions {
		data, err := os.ReadFile(filepath.Join("testdata", version, "sender-lines.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		var captured map[string]any
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &captured); err != nil {
			t.Fatal(err)
		}
		delete(captured, "from")
		ours, err := Line("00000000-0000-0000-0000-000000000000", DoorbellText())
		if err != nil {
			t.Fatal(err)
		}
		var written map[string]any
		if err := json.Unmarshal(ours, &written); err != nil {
			t.Fatal(err)
		}
		if got, want := keys(written, ""), keys(captured, ""); !slices.Equal(got, want) {
			t.Fatalf("%s: keys %v, captured %v", version, got, want)
		}
		if written["type"] != captured["type"] || written["msgV"] != captured["msgV"] || written["priority"] != captured["priority"] {
			t.Fatalf("%s: %v against %v", version, written, captured)
		}
	}
}

func DoorbellText() string { return chatstore.DoorbellLine(2, 1, 0) }

// shortFolder is a folder short enough for a Unix socket's path on every system.
func shortFolder(t *testing.T) string {
	t.Helper()
	folder, err := os.MkdirTemp("/tmp", "cn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(folder) })
	return folder
}

// listen serves one connection: it reads the line, hands it to got, and then closes the connection
// at once or keeps it open.
func listen(t *testing.T, closeAfterLine bool) (string, chan string) {
	t.Helper()
	path := filepath.Join(shortFolder(t), "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	done := make(chan struct{})
	t.Cleanup(func() { close(done); _ = listener.Close() })
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		received, _ := bufio.NewReader(connection).ReadString('\n')
		got <- received
		if !closeAfterLine {
			<-done // stays open, silent, until the test is over
		}
		_ = connection.Close()
	}()
	return path, got
}

func TestASilentSocketTookTheRing(t *testing.T) {
	path, got := listen(t, false)
	writer := Writer{Wait: 50 * time.Millisecond} // the server never closes before the test ends
	if outcome := writer.Ring(context.Background(), path, DoorbellText()); outcome != chatstore.RingTaken {
		t.Fatalf("got %s", outcome)
	}
	if line := <-got; !strings.Contains(line, "new messages") || strings.Contains(line, "auth") {
		t.Fatalf("the line carries the doorbell and no auth: %q", line)
	}
}

func TestAClosedConnectionRefusedTheRing(t *testing.T) {
	path, _ := listen(t, true)
	writer := Writer{Wait: time.Hour} // the close ends the wait, never the clock
	if outcome := writer.Ring(context.Background(), path, DoorbellText()); outcome != chatstore.RingClosed {
		t.Fatalf("got %s", outcome)
	}
}

func TestNoSocketIsAConfirmedFailure(t *testing.T) {
	folder := shortFolder(t)
	if outcome := (Writer{}).Ring(context.Background(), filepath.Join(folder, "none"), "x"); outcome != chatstore.RingFailed {
		t.Fatalf("a missing socket: %s", outcome)
	}
	file := filepath.Join(folder, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if outcome := (Writer{}).Ring(context.Background(), file, "x"); outcome != chatstore.RingFailed {
		t.Fatalf("a regular file: %s", outcome)
	}
}

func TestOnlyCapturedVersionsAreVerified(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	var captured []string
	for _, entry := range entries {
		if entry.IsDir() {
			captured = append(captured, entry.Name())
		}
	}
	if !slices.Equal(captured, VerifiedVersions) || Verified("2.1.327") || !Verified("2.1.328") {
		t.Fatalf("verified %v, captured %v", VerifiedVersions, captured)
	}
}
