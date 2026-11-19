package profile

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/zima-oss/jackstay/internal/cli"
)

const exportHelp = `usage: jst profile export PROFILE

Write a portable version-1 bundle with the profile and its workflow templates to stdout.
Print the SHA-256 of the exact bundle bytes to stderr.

exit: 0 exported. 1 read, validation or write error. 2 bad arguments.

example: jst profile export profile.json > policy.bundle.json`

const verifyHelp = `usage: jst profile verify --sha256 DIGEST BUNDLE

Check the bundle's exact bytes against a SHA-256 obtained independently of the bundle.
Read the bundled profile and templates to check this jackstay can use them offline.

exit: 0 verified. 1 digest, profile or template error. 2 bad arguments.

example: jst profile verify --sha256 "$expected" policy.bundle.json`

func init() {
	cli.Register(
		cli.Command{Name: "profile export", Summary: "export an offline adoption profile bundle", Usage: exportHelp, Run: exportCommand},
		cli.Command{Name: "profile verify", Summary: "verify a portable adoption profile bundle", Usage: verifyHelp, Run: verifyCommand},
	)
}

func exportCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, exportHelp)
		return 2
	}
	data, err := export(args[0])
	if err == nil {
		_, err = stdout.Write(data)
	}
	if err != nil {
		fmt.Fprintln(stderr, "jst profile export:", err)
		return 1
	}
	fmt.Fprintln(stderr, "jst profile export: sha256", sha256Hex(data))
	return 0
}

func verifyCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("profile verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	expected := flags.String("sha256", "", "expected digest of the bundle")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		fmt.Fprintln(stderr, verifyHelp)
		return 2
	}
	if decoded, err := hex.DecodeString(*expected); err != nil || len(decoded) != 32 {
		fmt.Fprintln(stderr, "jst profile verify: --sha256 needs 64 hex digits")
		return 2
	}
	path := flags.Arg(0)
	data, err := readProfileFile(path, maxBundleBytes)
	if err != nil {
		fmt.Fprintln(stderr, "jst profile verify:", err)
		return 1
	}
	digest := sha256Hex(data)
	if !strings.EqualFold(*expected, digest) {
		fmt.Fprintln(stderr, "jst profile verify: SHA-256 does not match")
		return 1
	}
	profile, err := loadBundle(path, data)
	if err != nil {
		fmt.Fprintln(stderr, "jst profile verify:", err)
		return 1
	}
	fmt.Fprintf(stdout, "jst profile verify: %q, format %d, sha256 %s\n", profile.Name, bundleVersion, digest)
	return 0
}
