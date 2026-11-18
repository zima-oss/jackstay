// Package patterns is `jst patterns [DIR...]`: the regular expressions of each project's jackstay.json
// that RE2, Go's dialect, does not take (W-036). A project rewrites them before Go reads its
// settings. Exit 0 none, 1 some, 2 a jackstay.json that cannot be read as JSON.
package patterns

import (
	"fmt"
	"io"
	"os"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
)

func init() {
	cli.Register(cli.Command{Name: "patterns", Summary: "the patterns of a jackstay.json that are not RE2", Usage: help, Run: run})
}

func run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		workDir, _ := os.Getwd()
		args = []string{workDir}
	}
	code := 0
	for _, dir := range args {
		_, path := config.Find(dir)
		if path == "" {
			fmt.Fprintf(stderr, "jst patterns: no %s at or above %s\n", config.Name, dir)
			code = 2
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "jst patterns: %v\n", err)
			code = 2
			continue
		}
		parsed, err := config.Parse(data)
		if err != nil {
			fmt.Fprintf(stderr, "jst patterns: %s: %v\n", path, err)
			code = 2
			continue
		}
		found := config.NotRE2(parsed)
		if len(found) == 0 {
			fmt.Fprintf(stdout, "%s: every pattern is RE2\n", path)
			continue
		}
		for _, problem := range found {
			fmt.Fprintf(stdout, "%s: not RE2: %s\n", path, problem)
		}
		if code == 0 {
			code = 1
		}
	}
	return code
}
