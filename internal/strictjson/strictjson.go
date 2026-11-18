// Package strictjson reads the JSON files jackstay defines (a diff case, a release request, an entry's
// front matter) strictly: exactly one value, no member named twice, no member the Go type does not
// declare, nothing after the value, and no more than a size limit. It is encoding/json/v2 with
// those options fixed, so every reader in jackstay refuses the same things.
package strictjson

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DefaultLimit is the size a file jackstay reads may have unless the caller says otherwise.
const DefaultLimit = 1 << 20

// Unmarshal decodes data into v strictly.
func Unmarshal(data []byte, target any) error {
	return json.Unmarshal(data, target, json.RejectUnknownMembers(true))
}

// Read decodes everything r holds into v strictly, refusing more than limit bytes.
func Read(reader io.Reader, limit int64, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("more than %d bytes", limit)
	}
	return Unmarshal(data, target)
}

// UnmarshalOpen decodes data into target strictly but takes members the Go type does not declare
// (a file another version may have written with more): one value, no member named twice, nothing
// after it.
func UnmarshalOpen(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

// Member is one member of an object, its value as written.
type Member struct {
	Name  string
	Value jsontext.Value
}

// Members reads data as exactly one JSON object, strictly (no member named twice, nothing after
// it), and returns its members in the order written with their values as written.
func Members(data []byte) ([]Member, error) {
	decoder := jsontext.NewDecoder(bytes.NewReader(data))
	token, err := decoder.ReadToken()
	if err != nil {
		return nil, err
	}
	if token.Kind() != '{' {
		return nil, errors.New("not a JSON object")
	}
	members := []Member{}
	for decoder.PeekKind() != '}' {
		name, err := decoder.ReadToken()
		if err != nil {
			return nil, err
		}
		memberName := name.String()
		value, err := decoder.ReadValue()
		if err != nil {
			return nil, err
		}
		members = append(members, Member{Name: memberName, Value: value.Clone()})
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, err
	}
	if _, err := decoder.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, errors.New("more than one JSON value")
	}
	return members, nil
}

// Integer is a member value that is a JSON integer (no fraction, no exponent) that fits an int.
func Integer(value jsontext.Value) (int, error) {
	text := string(bytes.TrimSpace(value))
	if value.Kind() != '0' || strings.ContainsAny(text, ".eE") {
		return 0, fmt.Errorf("%s is not an integer", text)
	}
	return strconv.Atoi(text)
}
