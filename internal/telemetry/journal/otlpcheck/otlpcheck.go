// Package otlpcheck holds the journal's lines to the OTLP v1 protocol it is written against
// (docs/design/observability.md, section 4): it reads a line into the pinned
// go.opentelemetry.io/proto/otlp's TracesData with protojson and writes the message back in OTLP's
// JSON encoding. Only tests import it, so no protobuf runtime is in jackstay's binary (section 8).
package otlpcheck

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// idKeys are the OTLP JSON fields that hold ids: hex strings in OTLP, base64 in protobuf's JSON.
var idKeys = map[string]bool{"traceId": true, "spanId": true, "parentSpanId": true}

// Decode reads an OTLP-JSON line into the protocol's message; unknown fields are an error.
func Decode(line []byte) (*tracepb.TracesData, error) {
	tree, err := parse(line)
	if err != nil {
		return nil, err
	}
	if err := rewrite(tree, func(text string) (string, error) {
		raw, err := hex.DecodeString(text)
		return base64.StdEncoding.EncodeToString(raw), err
	}); err != nil {
		return nil, err
	}
	data, err := json.Marshal(tree)
	if err != nil {
		return nil, err
	}
	message := &tracepb.TracesData{}
	return message, protojson.Unmarshal(data, message)
}

// Encode writes a message in OTLP's JSON encoding as a tree (keys sorted when marshalled).
func Encode(message *tracepb.TracesData) (any, error) {
	data, err := protojson.MarshalOptions{UseEnumNumbers: true}.Marshal(message)
	if err != nil {
		return nil, err
	}
	tree, err := parse(data)
	if err != nil {
		return nil, err
	}
	return tree, rewrite(tree, func(text string) (string, error) {
		raw, err := base64.StdEncoding.DecodeString(text)
		return hex.EncodeToString(raw), err
	})
}

// Tree parses a line as a generic JSON tree, numbers kept as written.
func Tree(line []byte) (any, error) { return parse(line) }

func parse(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var tree any
	return tree, decoder.Decode(&tree)
}

func rewrite(tree any, convert func(string) (string, error)) error {
	switch node := tree.(type) {
	case map[string]any:
		for key, value := range node {
			if text, ok := value.(string); ok && idKeys[key] {
				converted, err := convert(text)
				if err != nil {
					return err
				}
				node[key] = converted
				continue
			}
			if err := rewrite(value, convert); err != nil {
				return err
			}
		}
	case []any:
		for _, element := range node {
			if err := rewrite(element, convert); err != nil {
				return err
			}
		}
	}
	return nil
}
