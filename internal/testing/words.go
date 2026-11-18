package scopes

import (
	"slices"
	"strings"
)

// nothing is a `--skip` pattern that skips nothing: no test is named by the empty string.
const nothing = "^$"

// escapedCharacters are those Python's re.escape escapes (3.7 and later): the words a project's
// test runner reads are the same whichever jackstay wrote them.
const escapedCharacters = "()[]{}?*+-|^$\\.&~# \t\n\r\v\f"

// escapePattern is a name as a regular expression that matches exactly it.
func escapePattern(name string) string {
	var built strings.Builder
	for _, char := range name {
		if strings.ContainsRune(escapedCharacters, char) {
			built.WriteByte('\\')
		}
		built.WriteRune(char)
	}
	return built.String()
}

// testsPattern is `{tests}`: the names given to `focused` as one alternation `swift test --filter`
// reads (`Target.Class/test` exactly that test; `Target.Class` that class; `Class` that class in
// any target).
func testsPattern(names []string) string {
	parts := make([]string, 0, len(names))
	for _, name := range names {
		switch {
		case strings.Contains(name, "/"):
			parts = append(parts, "^"+escapePattern(name)+"$")
		case strings.Contains(name, "."):
			parts = append(parts, "^"+escapePattern(name)+"(/|$)")
		default:
			parts = append(parts, `(^|\.)`+escapePattern(name)+"(/|$)")
		}
	}
	if len(parts) == 1 {
		return parts[0]
	}
	grouped := make([]string, 0, len(parts))
	for _, part := range parts {
		grouped = append(grouped, "("+part+")")
	}
	return strings.Join(grouped, "|")
}

// expand is the words that run: the placeholders replaced, as whole words or inside one; a
// refusal when the call cannot fill them (or gives names nothing takes). slow is `{slow}`'s value
// ("" leaves it skipping nothing).
func expand(chosen leaf, args []string, base, slow string) ([]string, error) {
	used := placeholdersOf(chosen.run)
	takesTests := slices.Contains(used, "tests")
	switch {
	case len(args) > 0 && !takesTests:
		return nil, refused("`" + chosen.name + "` takes no names (its words hold no {tests}); given: " + joinSpace(args))
	case takesTests && len(args) == 0:
		return nil, refused("`" + chosen.name + "` needs the tests to run: jst test " + chosen.name + " Target.Class[/test] …")
	case slices.Contains(used, "base") && base == "":
		return nil, refused("`" + chosen.name + "` needs a base: `\"base\"` in its definition, or --base REF")
	}
	if slow == "" {
		slow = nothing
	}
	values := map[string]string{"tests": "", "base": base, "slow": slow}
	if len(args) > 0 {
		values["tests"] = testsPattern(args)
	}
	words := make([]string, 0, len(chosen.run))
	for _, word := range chosen.run {
		words = append(words, replacePlaceholders(word, values))
	}
	return words, nil
}

// plainWord is a word a shell reads back as it is.
func plainWord(word string) bool {
	if word == "" {
		return false
	}
	for _, char := range word {
		plain := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
			strings.ContainsRune("@%+=:,./_-", char)
		if !plain {
			return false
		}
	}
	return true
}

// shellQuote is a word as a shell reads it back.
func shellQuote(word string) string {
	if plainWord(word) {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'"'"'`) + "'"
}

// shellWords are words as one shell line.
func shellWords(words []string) string {
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		quoted = append(quoted, shellQuote(word))
	}
	return joinSpace(quoted)
}
