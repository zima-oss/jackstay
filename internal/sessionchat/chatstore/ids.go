package chatstore

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"time"
)

// alphabet is Crockford's base 32 in lower case, the alphabet of message IDs and handles
// (docs/design/chat.md section 5.1).
const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

var (
	messageIDPattern = regexp.MustCompile(`^msg_[0-9a-hjkmnp-tv-z]{26}$`)
	handlePattern    = regexp.MustCompile(`^msg_([0-9a-hjkmnp-tv-z]{26})\.([0-9a-hjkmnp-tv-z]{13})$`)
	requestIDPattern = regexp.MustCompile(`^req_([0-9a-hjkmnp-tv-z]{26})$`)
)

// newMessageID is a ULID-shaped ID: 48 bits of milliseconds then 80 random bits, so IDs sort by
// their creation and the outbox can count a sender's recent sends from the names alone.
func newMessageID(now time.Time, random io.Reader) (string, error) {
	var bits [16]byte
	binary.BigEndian.PutUint64(bits[:8], uint64(now.UnixMilli())<<16) // #nosec G115 -- a time after 1970
	if _, err := io.ReadFull(random, bits[6:]); err != nil {
		return "", fmt.Errorf("random message ID: %w", err)
	}
	return "msg_" + encode(bits[:], 26), nil
}

// newHandle is the 64 random bits of a native delivery's handle (section 4.10), 13 characters.
func newHandle(random io.Reader) (string, error) {
	var bits [8]byte
	if _, err := io.ReadFull(random, bits[:]); err != nil {
		return "", fmt.Errorf("random handle: %w", err)
	}
	return encode(bits[:], 13), nil
}

// encode writes the bits of data, most significant first, as count base-32 characters; the
// first character takes the bits left over at the top.
func encode(data []byte, count int) string {
	out := make([]byte, count)
	var accumulator uint64
	var held uint
	position := count - 1
	for index := len(data) - 1; index >= 0 && position >= 0; index-- {
		accumulator |= uint64(data[index]) << held
		held += 8
		for held >= 5 && position >= 0 {
			out[position] = alphabet[accumulator&31]
			accumulator >>= 5
			held -= 5
			position--
		}
	}
	for ; position >= 0; position-- {
		out[position] = alphabet[accumulator&31]
		accumulator >>= 5
	}
	return string(out)
}

// messageTime is the creation time an ID carries.
func messageTime(id string) (time.Time, bool) {
	if !messageIDPattern.MatchString(id) {
		return time.Time{}, false
	}
	var milliseconds uint64
	for _, character := range id[4:14] { // ten characters: two bits of padding, then the 48 of time
		milliseconds = milliseconds<<5 | uint64(indexOf(byte(character))) // #nosec G115 -- below 32
	}
	return time.UnixMilli(int64(milliseconds)), true // #nosec G115 -- 50 bits
}

func indexOf(character byte) int {
	for index := 0; index < len(alphabet); index++ {
		if alphabet[index] == character {
			return index
		}
	}
	return 0
}

// requestIDOf is the request ID of a request message: the message's own ID under `req_`.
func requestIDOf(messageID string) string { return "req_" + messageID[4:] }

// messageOfRequest is the message ID a request ID names, and whether it is one.
func messageOfRequest(requestID string) (string, bool) {
	match := requestIDPattern.FindStringSubmatch(requestID)
	if match == nil {
		return "", false
	}
	return "msg_" + match[1], true
}

// splitReference splits a message reference (section 5.1) into the message ID and, for a native
// delivery's handle, its 13 random characters.
func splitReference(reference string) (id, handle string, ok bool) {
	if messageIDPattern.MatchString(reference) {
		return reference, "", true
	}
	if match := handlePattern.FindStringSubmatch(reference); match != nil {
		return "msg_" + match[1], match[2], true
	}
	return "", "", false
}

// hashKey is the hex SHA-256 of a session key: its file name and the start of its ref.
func hashKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

// hashOf is the hex SHA-256 of joined parts.
func hashOf(parts ...string) string {
	hash := sha256.New()
	for index, part := range parts {
		if index > 0 {
			hash.Write([]byte{0})
		}
		hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// shortRef is the shortest of 6, 8 or 10 leading characters of hash that no other hash shares.
func shortRef(hash string, others []string) string {
	for _, length := range []int{6, 8, 10} {
		clash := false
		for _, other := range others {
			if other != hash && len(other) >= length && other[:length] == hash[:length] {
				clash = true
				break
			}
		}
		if !clash {
			return hash[:length]
		}
	}
	return hash[:10]
}
