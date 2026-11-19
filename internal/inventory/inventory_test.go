package inventory

import (
	"bytes"
	"strings"
	"testing"
)

func TestInventoryRefusesAnUnboundedWindow(t *testing.T) {
	for _, args := range [][]string{{"--since", "-1h"}, {"--since", "721h"}, {"extra"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, nil, &stdout, &stderr); code != 2 || stdout.Len() != 0 ||
			!strings.Contains(stderr.String(), "usage: jst inventory") {
			t.Fatalf("%v: code %d stdout %q stderr %q", args, code, &stdout, &stderr)
		}
	}
}
