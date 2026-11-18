package supervise

import "testing"

// A capture over its cap keeps the first bytes and says it dropped the rest (Captured.Incomplete).
func TestCappedBufferSaysWhatItDropped(t *testing.T) {
	capped := &cappedBuffer{limit: 4}
	if _, err := capped.Write([]byte("abcd")); err != nil || capped.dropped() {
		t.Fatalf("4 bytes at a cap of 4: dropped %v, %v", capped.dropped(), err)
	}
	if _, err := capped.Write([]byte("e")); err != nil || !capped.dropped() || string(capped.bytes()) != "abcd" {
		t.Fatalf("a fifth byte: dropped %v, kept %q, %v", capped.dropped(), capped.bytes(), err)
	}
}
