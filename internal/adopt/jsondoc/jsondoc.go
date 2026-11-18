// Package jsondoc is an ordered JSON model for the files `jst adopt` edits (jackstay.json,
// .claude/settings.json; docs/design/adopt.md sections 3.1 and 3.3): objects keep their keys in the
// order written, a key added goes after them, and Format writes two spaces of indent and one
// newline at the end. A file is only rewritten when Equal says its model changed, so a file
// formatted another way is left byte for byte.
package jsondoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Object is a JSON object with its keys in order.
type Object struct {
	keys    []string
	members map[string]any
}

// NewObject is an empty object.
func NewObject() *Object { return &Object{members: map[string]any{}} }

// Keys are the object's keys, in order.
func (object *Object) Keys() []string { return append([]string(nil), object.keys...) }

// Get is the value of key.
func (object *Object) Get(key string) (any, bool) {
	value, ok := object.members[key]
	return value, ok
}

// Object is the member key when it is an object, else nil.
func (object *Object) Object(key string) *Object {
	value, _ := object.members[key].(*Object)
	return value
}

// Set gives key a value; a new key goes after the existing ones.
func (object *Object) Set(key string, value any) {
	if _, ok := object.members[key]; !ok {
		object.keys = append(object.keys, key)
	}
	object.members[key] = value
}

// Parse reads one JSON value strictly: no key twice in an object, nothing after the value.
// Objects become *Object, arrays []any, numbers json.Number.
func Parse(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("more than one JSON value")
	}
	return value, nil
}

func readValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := NewObject()
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, _ := keyToken.(string)
			if _, twice := object.members[key]; twice {
				return nil, fmt.Errorf("the key %q is written twice in one object", key)
			}
			value, err := readValue(decoder)
			if err != nil {
				return nil, err
			}
			object.Set(key, value)
		}
		_, err := decoder.Token()
		return object, err
	case '[':
		list := []any{}
		for decoder.More() {
			value, err := readValue(decoder)
			if err != nil {
				return nil, err
			}
			list = append(list, value)
		}
		_, err := decoder.Token()
		return list, err
	}
	return nil, fmt.Errorf("unexpected %v", delimiter)
}

// FromPlain turns a value of encoding/json's model (maps, slices, numbers) into this one; an
// object's keys are taken in lexical order (a Go map has none of its own).
func FromPlain(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		object := NewObject()
		for _, key := range keys {
			object.Set(key, FromPlain(typed[key]))
		}
		return object
	case []any:
		list := make([]any, len(typed))
		for index, item := range typed {
			list[index] = FromPlain(item)
		}
		return list
	case float64:
		return json.Number(fmt.Sprint(typed))
	case int:
		return json.Number(fmt.Sprint(typed))
	}
	return value
}

// ToPlain is the value in encoding/json's model (maps, slices, json.Number), for the checks that
// read that model.
func ToPlain(value any) any {
	switch typed := value.(type) {
	case *Object:
		plain := make(map[string]any, len(typed.keys))
		for _, key := range typed.keys {
			plain[key] = ToPlain(typed.members[key])
		}
		return plain
	case []any:
		list := make([]any, len(typed))
		for index, item := range typed {
			list[index] = ToPlain(item)
		}
		return list
	}
	return value
}

// Clone is a deep copy.
func Clone(value any) any {
	switch typed := value.(type) {
	case *Object:
		copied := NewObject()
		for _, key := range typed.keys {
			copied.Set(key, Clone(typed.members[key]))
		}
		return copied
	case []any:
		list := make([]any, len(typed))
		for index, item := range typed {
			list[index] = Clone(item)
		}
		return list
	}
	return value
}

// Equal says two values are the same JSON, keys in the same order.
func Equal(left, right any) bool {
	switch typed := left.(type) {
	case *Object:
		other, ok := right.(*Object)
		if !ok || len(typed.keys) != len(other.keys) {
			return false
		}
		for index, key := range typed.keys {
			if other.keys[index] != key || !Equal(typed.members[key], other.members[key]) {
				return false
			}
		}
		return true
	case []any:
		other, ok := right.([]any)
		if !ok || len(typed) != len(other) {
			return false
		}
		for index := range typed {
			if !Equal(typed[index], other[index]) {
				return false
			}
		}
		return true
	case json.Number:
		other, ok := right.(json.Number)
		return ok && typed == other
	}
	return left == right
}

// Format writes the value with two spaces of indent and one newline at the end.
func Format(value any) []byte {
	var out strings.Builder
	write(&out, value, "")
	out.WriteString("\n")
	return []byte(out.String())
}

func write(out *strings.Builder, value any, indent string) {
	inner := indent + "  "
	switch typed := value.(type) {
	case *Object:
		if len(typed.keys) == 0 {
			out.WriteString("{}")
			return
		}
		out.WriteString("{\n")
		for index, key := range typed.keys {
			out.WriteString(inner)
			out.WriteString(quote(key))
			out.WriteString(": ")
			write(out, typed.members[key], inner)
			if index < len(typed.keys)-1 {
				out.WriteString(",")
			}
			out.WriteString("\n")
		}
		out.WriteString(indent + "}")
	case []any:
		if len(typed) == 0 {
			out.WriteString("[]")
			return
		}
		out.WriteString("[\n")
		for index, item := range typed {
			out.WriteString(inner)
			write(out, item, inner)
			if index < len(typed)-1 {
				out.WriteString(",")
			}
			out.WriteString("\n")
		}
		out.WriteString(indent + "]")
	case string:
		out.WriteString(quote(typed))
	case json.Number:
		out.WriteString(typed.String())
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		out.WriteString(quote(fmt.Sprint(typed)))
	}
}

func quote(text string) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(text)
	return strings.TrimSuffix(buffer.String(), "\n")
}
