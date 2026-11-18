package release

import (
	"fmt"
	"io"
	"sort"
)

func sortStrings(values []string) { sort.Strings(values) }

func runPrepare(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flagSet("prepare", stderr)
	asJSON := flags.Bool("json", false, "print the plan as one JSON object")
	if flags.Parse(args) != nil || flags.NArg() > 0 {
		return 2
	}
	_, _, repo, state, err := preflight()
	if err != nil {
		return fail(stderr, "prepare", err)
	}
	prepared, err := repo.Prepare(state)
	if err != nil {
		return fail(stderr, "prepare", err)
	}
	printPlan(stdout, prepared, *asJSON)
	if !*asJSON {
		fmt.Fprintln(stdout, "\njst release prepare: a preview of HEAD's release, nothing written; a release is made by the workflow after a request lands (W-047)")
	}
	return 0
}

// errorf is a Problem with exit 1.
func errorf(format string, args ...any) error {
	return &Problem{Code: 1, Message: fmt.Sprintf(format, args...)}
}
