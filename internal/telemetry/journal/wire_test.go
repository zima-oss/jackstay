package journal

import (
	"encoding/json"
	"testing"
)

func TestWireNumberReadsAStringOrANumber(t *testing.T) {
	for _, text := range []string{`"1763467200000000000"`, `1763467200000000000`, `"-5"`} {
		var number wireNumber
		if err := json.Unmarshal([]byte(text), &number); err != nil {
			t.Errorf("%s: %v", text, err)
		}
	}
}

// A time beyond the int64 range is refused, not wrapped round to a negative time.
func TestWireNumberRefusesANumberBeyondInt64(t *testing.T) {
	for _, text := range []string{`"9223372036854775808"`, `"18446744073709551615"`, `18446744073709551615`} {
		var number wireNumber
		if err := json.Unmarshal([]byte(text), &number); err == nil {
			t.Errorf("%s: read as %d, want an error", text, number)
		}
	}
}
