// Package claudenative rings a Claude Code session's doorbell on its per-session messaging socket
// (docs/design/chat.md section 8.3, W-095): one line that says how many chat messages wait, never a
// message's text. The line's shape is the one captured from Claude Code (testdata/), and only the
// captured versions are verified for the native route.
package claudenative

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
)

// VerifiedVersions are the Claude Code versions whose socket was captured (testdata/): a session on
// one of them is on the native route; any other is on the hooks route.
var VerifiedVersions = []string{"2.1.328"}

// Verified reports whether a Claude Code version is in the verified range.
func Verified(version string) bool { return slices.Contains(VerifiedVersions, version) }

// answerWait is how long a ring waits to see whether the session closes the connection.
const answerWait = 2 * time.Second

// line is the doorbell's line, in the shape Claude Code's socket takes (testdata/).
type line struct {
	MessageVersion int     `json:"msgV"`
	MessageID      string  `json:"msg_id"`
	Type           string  `json:"type"`
	Message        content `json:"message"`
	Priority       string  `json:"priority"`
}

type content struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Line encodes a doorbell's text as one socket line, ending in a newline.
func Line(id, text string) ([]byte, error) {
	data, err := json.Marshal(line{MessageVersion: 1, MessageID: id, Type: "user",
		Message: content{Role: "user", Content: text}, Priority: "next"})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Writer rings doorbells. Wait and Random replace the 2 s wait and the source of the line's ID; a
// test sets them.
type Writer struct {
	Wait   time.Duration
	Random io.Reader
}

// Ring writes the doorbell to the socket and says what became of it: taken (the connection stayed
// open, or the session answered), closed (the session refused it), or failed (nothing reached the
// socket: none there, not the account's own, or the connection refused). It never sends Claude
// Code's messaging token (section 13).
func (writer Writer) Ring(ctx context.Context, socket, text string) chatstore.RingOutcome {
	if !ownSocket(socket) {
		return chatstore.RingFailed
	}
	id, err := newUUID(writer.Random)
	if err != nil {
		return chatstore.RingFailed
	}
	data, err := Line(id, text)
	if err != nil {
		return chatstore.RingFailed
	}
	dialer := net.Dialer{Timeout: answerWait}
	connection, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return chatstore.RingFailed
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.Write(data); err != nil {
		return chatstore.RingClosed
	}
	wait := writer.Wait
	if wait <= 0 {
		wait = answerWait
	}
	if err := connection.SetReadDeadline(time.Now().Add(wait)); err != nil {
		return chatstore.RingTaken
	}
	buffer := make([]byte, 512)
	_, readErr := connection.Read(buffer)
	var timeout net.Error
	switch {
	case readErr == nil:
		return chatstore.RingTaken // an answer: Claude Code wrote something back
	case errors.As(readErr, &timeout) && timeout.Timeout():
		return chatstore.RingTaken // silence, which means nothing (W-095)
	}
	return chatstore.RingClosed
}

// ownSocket reports whether the path is a socket the account owns, not followed through a link.
func ownSocket(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(owner.Uid) == os.Getuid() // #nosec G115 -- a user ID
}

func newUUID(random io.Reader) (string, error) {
	if random == nil {
		random = rand.Reader
	}
	var bits [16]byte
	if _, err := io.ReadFull(random, bits[:]); err != nil {
		return "", err
	}
	bits[6] = bits[6]&0x0f | 0x40
	bits[8] = bits[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bits[0:4], bits[4:6], bits[6:8], bits[8:10], bits[10:16]), nil
}
