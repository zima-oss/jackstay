package baseline

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zima-oss/jackstay/internal/guard"
)

// What may stand before the program in a simple command, and is dropped before a kind's pattern
// is tried.
var (
	keywords   = []string{"if", "then", "else", "elif", "do", "while", "until", "!", "{", "(", "time"}
	wrappers   = []string{"caffeinate", "taskpolicy", "nice", "time", "env", "command", "timeout", "gtimeout"}
	assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// baseName is what follows a path's last slash ("" after a trailing one).
func baseName(word string) string {
	return word[strings.LastIndexByte(word, '/')+1:]
}

func allDigits(word string) bool {
	if word == "" {
		return false
	}
	for _, char := range word {
		if !unicode.IsDigit(char) {
			return false
		}
	}
	return true
}

// plainCommand is a simple command's words without what stands before the program (keywords,
// assignments, wrappers and their short flags, counts), the program cut to its base name, joined
// by single spaces; "" when nothing is left or the first word cannot be a program (it holds a
// space: a quoted sentence).
func plainCommand(words []string) string {
	for len(words) > 0 {
		first := words[0]
		if !slices.Contains(keywords, first) && !assignment.MatchString(first) && !slices.Contains(wrappers, baseName(first)) &&
			(!strings.HasPrefix(first, "-") || utf8.RuneCountInString(first) >= 12) && first != "utility" && first != "background" &&
			!allDigits(first) {
			break
		}
		words = words[1:]
	}
	if len(words) == 0 || strings.IndexFunc(words[0], unicode.IsSpace) >= 0 {
		return ""
	}
	return strings.Join(append([]string{baseName(words[0])}, words[1:]...), " ")
}

// kindOfCommands is the earliest kind in the list that one of the simple commands matches, or "".
// A command `X … -- Y …` is also tried as `Y …`.
func kindOfCommands(kinds []Kind, commands [][]string) string {
	best := -1
	for _, words := range commands {
		tried := []string{plainCommand(words)}
		if dash := slices.Index(words, "--"); dash >= 0 {
			tried = append(tried, plainCommand(words[dash+1:]))
		}
		for index, kind := range kinds {
			if best >= 0 && index >= best {
				break
			}
			if slices.ContainsFunc(tried, func(text string) bool { return text != "" && kind.Pattern.MatchString(text) }) {
				best = index
				break
			}
		}
	}
	if best < 0 {
		return ""
	}
	return kinds[best].Name
}

// kindOf is the kind a command line starts, read as a shell reads it: a word in a `grep`, an
// `echo` or a commit message starts nothing.
func kindOf(kinds []Kind, line string) string {
	commands, err := guard.Words(line)
	if err != nil {
		return ""
	}
	return kindOfCommands(kinds, commands)
}
