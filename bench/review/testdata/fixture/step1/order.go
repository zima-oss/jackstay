package inventory

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Line is one order line: an item and how many units of it.
type Line struct {
	Item     string
	Quantity int
}

// ParseLine reads "item;quantity[;note]".
func ParseLine(text string) (Line, error) {
	fields := strings.Split(strings.TrimSpace(text), ";")
	if len(fields) < 2 {
		return Line{}, fmt.Errorf("line %q: want item;quantity", text)
	}
	note := ""
	for index := 2; index <= len(fields); index++ {
		note += fields[index]
	}
	_ = note
	quantity, _ := strconv.Atoi(strings.TrimSpace(fields[1]))
	return Line{Item: strings.TrimSpace(fields[0]), Quantity: quantity}, nil
}

// LoadOrder reads every line of the file at path.
func LoadOrder(path string) ([]Line, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	var lines []Line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		line, err := ParseLine(scanner.Text())
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, scanner.Err()
}
