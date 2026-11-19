package scopes

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/zima-oss/jackstay/internal/judge"
)

const jestSelector = "jest-test"

func parseJestSelection(args []string) (judge.JestSelection, error) {
	if len(args) != 1 {
		return judge.JestSelection{}, refused("the jest-test selector needs exactly one Jest ID: FILE::full test name")
	}
	file, name, separated := strings.Cut(args[0], "::")
	if !separated || file == "." || len(file) > 1024 || !fs.ValidPath(file) || strings.HasPrefix(file, "-") ||
		strings.ContainsAny(file, "{}\\") || strings.IndexFunc(file, unicode.IsControl) >= 0 {
		return judge.JestSelection{}, refused("the jest-test selector needs a relative test file path before ::")
	}
	if strings.TrimSpace(name) == "" || len(name) > 1024 ||
		strings.IndexFunc(name, func(char rune) bool {
			return unicode.IsControl(char) || char == '\u2028' || char == '\u2029'
		}) >= 0 {
		return judge.JestSelection{}, refused("the jest-test selector needs a literal full test name after ::")
	}
	return judge.JestSelection{File: file, Name: name}, nil
}

func jestSelectionArgs(selected judge.JestSelection) []string {
	return []string{"--testNamePattern=^" + regexp.QuoteMeta(selected.Name) + "$", "--runTestsByPath", selected.File}
}

func selectorProblem(selector, judgeName, kind, scope string, words []string) string {
	var required judge.Kind
	var command []string
	switch selector {
	case goSelector:
		required = judge.GoJSON
		command = []string{"go", "test", "-json", "-count=1", "{test_args}"}
	case jestSelector:
		required = judge.JestJSON
		command = []string{"jest", "--runInBand", "--no-watchman", "--json", "--outputFile={report}", "{test_args}"}
	default:
		return "{test_args} requires a scope with selector `go-test` or `jest-test`"
	}
	if judgeName != string(required) || kind != "scopes" {
		return "{test_args} requires a scope with selector `" + selector + "` and judge `" + string(required) + "`"
	}
	if scope != "focused" && !strings.HasPrefix(scope, extraPrefix) {
		return selector + " is for `focused` or an `x-…` scope"
	}
	if !slices.Equal(words, command) {
		return "the " + selector + " selector requires [" + joinComma(command) + "]"
	}
	return ""
}
