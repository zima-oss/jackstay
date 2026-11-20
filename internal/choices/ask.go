package choices

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Asker puts a question to a person; offered is the answer taken on a plain Enter.
type Asker interface {
	Ask(question Question, offered string) (string, error)
}

// ErrNoAnswer says the input ended before an answer.
var ErrNoAnswer = errors.New("no answer: the input ended")

// TerminalAsker asks on a terminal: the options numbered, the offered one marked, Enter takes it.
type TerminalAsker struct {
	In  *bufio.Reader
	Out io.Writer
}

// Ask shows the question until it gets a number, an option's value or Enter.
func (asker TerminalAsker) Ask(question Question, offered string) (string, error) {
	if !question.Has(offered) {
		offered = question.Recommended()
	}
	for {
		fmt.Fprintln(asker.Out, question.Text)
		for index, option := range question.Options {
			marker := " "
			if option.Value == offered {
				marker = "*"
			}
			fmt.Fprintf(asker.Out, "  %s %d. %s\n", marker, index+1, option.Label)
		}
		fmt.Fprintf(asker.Out, "Answer [Enter: %s]: ", offered)
		line, err := asker.In.ReadString('\n')
		answer := strings.TrimSpace(line)
		if err != nil && answer == "" {
			return "", ErrNoAnswer
		}
		if answer == "" {
			return offered, nil
		}
		if number, err := strconv.Atoi(answer); err == nil && number >= 1 && number <= len(question.Options) {
			return question.Options[number-1].Value, nil
		}
		if question.Has(answer) {
			return answer, nil
		}
		fmt.Fprintf(asker.Out, "%q is none of the options.\n", answer)
	}
}

// Offered is the answer a question starts from: the project's value, else the machine default,
// else the recommended one; from says which.
func Offered(question Question, project string, inProject bool, defaults Defaults) (value, from string) {
	switch {
	case inProject && question.Has(project):
		return project, "this project's jackstay.json"
	case defaults[question.ID] != "":
		return defaults[question.ID], "this machine's default (jst setup)"
	default:
		return question.Recommended(), "recommended"
	}
}
