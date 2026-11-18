// Command jst-licences collects the licence files of the third-party modules compiled into jst
// (internal/licences): goreleaser's before hook, `go run ./cmd/jst-licences licenses`, so a release
// archive carries them beside jackstay's LICENSE. It is not installed.
package main

import (
	"os"

	"github.com/zima-oss/jackstay/internal/licences"
)

func main() {
	os.Exit(licences.Main(os.Args[1:], os.Stdout, os.Stderr))
}
