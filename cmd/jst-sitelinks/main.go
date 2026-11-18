// Command jst-sitelinks checks the links of a built documentation site (internal/sitelinks): `just
// docs` runs it with `go run ./cmd/jst-sitelinks FOLDER BASE` on the site it built. It is not
// installed.
package main

import (
	"os"

	"github.com/zima-oss/jackstay/internal/sitelinks"
)

func main() {
	os.Exit(sitelinks.Main(os.Args[1:], os.Stdout, os.Stderr))
}
