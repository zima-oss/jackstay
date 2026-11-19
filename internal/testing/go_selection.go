package scopes

import (
	"go/token"
	"regexp"
	"strings"
	"unicode"

	"github.com/zima-oss/jackstay/internal/judge"
)

const goSelector = "go-test"

var goPackagePath = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]*(/[A-Za-z0-9][A-Za-z0-9._+~-]*)*$`)

func parseGoSelection(args []string) (judge.GoSelection, error) {
	if len(args) != 1 {
		return judge.GoSelection{}, refused("the go-test selector needs exactly one Go test ID: PACKAGE::TestName[/subtest]")
	}
	pkg, name, separated := strings.Cut(args[0], "::")
	if !separated || len(pkg) > 512 || strings.Contains(pkg, "...") || !goPackagePath.MatchString(pkg) ||
		pkg == "all" || pkg == "std" || pkg == "cmd" || pkg == "tool" || pkg == "work" {
		return judge.GoSelection{}, refused("the go-test selector needs a literal Go package import path before ::")
	}
	root, _, _ := strings.Cut(name, "/")
	if len(name) > 512 || !token.IsIdentifier(root) ||
		(!strings.HasPrefix(root, "Test") && !strings.HasPrefix(root, "Example") && !strings.HasPrefix(root, "Fuzz")) {
		return judge.GoSelection{}, refused("the go-test selector needs a Go test, example or fuzz seed name after ::")
	}
	if strings.HasSuffix(name, "/") || strings.Contains(name, "//") ||
		strings.IndexFunc(name, func(char rune) bool { return unicode.IsControl(char) || unicode.IsSpace(char) }) >= 0 {
		return judge.GoSelection{}, refused("the go-test selector needs nonempty subtest names as printed by go test -json")
	}
	return judge.GoSelection{Package: pkg, Name: name}, nil
}

func goSelectionArgs(selected judge.GoSelection) []string {
	parts := strings.Split(selected.Name, "/")
	for index, name := range parts {
		parts[index] = "^" + regexp.QuoteMeta(name) + "$"
	}
	return []string{"-run=" + strings.Join(parts, "/"), selected.Package}
}
