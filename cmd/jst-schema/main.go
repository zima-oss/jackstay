// Command jst-schema prints the editor-facing schema for jackstay.json.
// Regenerate with: go run ./cmd/jst-schema > schemas/jackstay.schema.json
package main

import (
	"fmt"
	"os"

	"github.com/zima-oss/jackstay/internal/config"
)

func main() {
	content, err := config.JSONSchema()
	if err == nil {
		_, err = os.Stdout.Write(content)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
