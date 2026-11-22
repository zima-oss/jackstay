package sessionchat

import (
	"fmt"
	"io"

	"github.com/zima-oss/jackstay/internal/cli"
)

// The Codex bridge's push (docs/design/chat.md section 9.2) waits for jackstay 1. A Codex session
// reaches the chat through `jst chat serve` and reads its inbox there.
func init() {
	cli.Register(cli.Command{Name: "chat codex-bridge", Summary: "not in jackstay 0.6: the Codex bridge returns in jackstay 1",
		Usage:       "usage: jst chat codex-bridge\n\nThe Codex bridge's push waits for jackstay 1. A Codex session reaches the chat through `jst chat serve` and reads its inbox with chat.inbox.\n\nexit: 2 always.\n\nexample: jst chat serve\n",
		PrivateArgs: true, Run: func(_ []string, _ io.Reader, _ io.Writer, diagnostics io.Writer) int {
			fmt.Fprintln(diagnostics, "jst chat codex-bridge: the Codex bridge returns in jackstay 1; a Codex session reads its inbox through jst chat serve")
			return 2
		}})
}
