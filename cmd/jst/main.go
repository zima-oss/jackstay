// Command jackstay is jackstay's one binary (docs/design/implementation.md). Called as `jst-guard`, it is `jst guard` (the
// Claude Code hook the projects' settings name).
package main

import (
	"os"

	"github.com/zima-oss/jackstay/internal/cli"
	// Each Go command registers itself in cli's table.
	_ "github.com/zima-oss/jackstay/internal/adopt"
	_ "github.com/zima-oss/jackstay/internal/baseline"
	_ "github.com/zima-oss/jackstay/internal/branch"
	_ "github.com/zima-oss/jackstay/internal/changes"
	_ "github.com/zima-oss/jackstay/internal/check"
	_ "github.com/zima-oss/jackstay/internal/contextbrief"
	_ "github.com/zima-oss/jackstay/internal/discard"
	_ "github.com/zima-oss/jackstay/internal/doctor"
	_ "github.com/zima-oss/jackstay/internal/gh"
	_ "github.com/zima-oss/jackstay/internal/guard"
	_ "github.com/zima-oss/jackstay/internal/ids"
	_ "github.com/zima-oss/jackstay/internal/install"
	_ "github.com/zima-oss/jackstay/internal/land"
	_ "github.com/zima-oss/jackstay/internal/landed"
	_ "github.com/zima-oss/jackstay/internal/lint"
	_ "github.com/zima-oss/jackstay/internal/lock"
	_ "github.com/zima-oss/jackstay/internal/logs"
	_ "github.com/zima-oss/jackstay/internal/owner"
	_ "github.com/zima-oss/jackstay/internal/patterns"
	_ "github.com/zima-oss/jackstay/internal/procs/background"
	_ "github.com/zima-oss/jackstay/internal/records"
	_ "github.com/zima-oss/jackstay/internal/release"
	_ "github.com/zima-oss/jackstay/internal/review"
	_ "github.com/zima-oss/jackstay/internal/sessionchat"
	_ "github.com/zima-oss/jackstay/internal/stats"
	_ "github.com/zima-oss/jackstay/internal/status"
	_ "github.com/zima-oss/jackstay/internal/testing"
	_ "github.com/zima-oss/jackstay/internal/tidy"
	_ "github.com/zima-oss/jackstay/internal/trash"
	_ "github.com/zima-oss/jackstay/internal/wait"
)

func main() {
	os.Exit(cli.Main(os.Args[0], os.Args[1:]))
}
