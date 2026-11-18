package release

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

// Request is what a release request asks for: the version, the entries by file name with their
// blob ids, the override's blob id, and the commit it was written on, its identity
// (docs/design/release.md, section 3).
type Request struct {
	Version  string            `json:"version"`
	Entries  map[string]string `json:"entries"`
	Override *string           `json:"override"`
	MadeOn   string            `json:"made_on"`
}

// Encode is the request file's bytes: one line, keys in a fixed order, entries sorted by name,
// ending with a line end — the same bytes whichever jackstay writes it.
func (request Request) Encode() []byte {
	var out bytes.Buffer
	out.WriteString(`{"version": `)
	out.WriteString(jsonString(request.Version))
	out.WriteString(`, "entries": {`)
	names := make([]string, 0, len(request.Entries))
	for name := range request.Entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for index, name := range names {
		if index > 0 {
			out.WriteString(", ")
		}
		out.WriteString(jsonString(name) + ": " + jsonString(request.Entries[name]))
	}
	out.WriteString(`}, "override": `)
	if request.Override == nil {
		out.WriteString("null")
	} else {
		out.WriteString(jsonString(*request.Override))
	}
	out.WriteString(`, "made_on": `)
	out.WriteString(jsonString(request.MadeOn))
	out.WriteString("}\n")
	return out.Bytes()
}

func jsonString(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err) // a Go string always encodes
	}
	return string(encoded)
}

// objectID matches a git object id of the repository's format.
func objectID(format, value string) bool {
	length := 40
	if format == "sha256" {
		length = 64
	}
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// DecodeRequest reads a request file strictly: every member present (override may be null), the
// version a version, every id an object id of the repository's format ("sha1" or "sha256").
func DecodeRequest(data []byte, format string) (Request, error) {
	var members map[string]any
	if err := strictjson.Unmarshal(data, &members); err != nil {
		return Request{}, fmt.Errorf("not a release request: %w", err)
	}
	for _, name := range []string{"version", "entries", "override", "made_on"} {
		if _, present := members[name]; !present {
			return Request{}, fmt.Errorf("not a release request: %q is missing", name)
		}
	}
	var request Request
	if err := strictjson.Unmarshal(data, &request); err != nil {
		return Request{}, fmt.Errorf("not a release request: %w", err)
	}
	if !objectID(format, request.MadeOn) {
		return Request{}, fmt.Errorf("not a release request: made_on %q is not a commit id", request.MadeOn)
	}
	if request.Override != nil && !objectID(format, *request.Override) {
		return Request{}, fmt.Errorf("not a release request: override %q is not a blob id", *request.Override)
	}
	for name, blob := range request.Entries {
		if !objectID(format, blob) {
			return Request{}, fmt.Errorf("not a release request: %s's %q is not a blob id", name, blob)
		}
	}
	if _, err := ParseVersion(request.Version); err != nil {
		return Request{}, fmt.Errorf("not a release request: %w", err)
	}
	if request.Entries == nil || request.MadeOn == "" {
		return Request{}, fmt.Errorf("not a release request: entries and made_on are required")
	}
	for name := range request.Entries {
		if !strings.HasSuffix(name, ".md") || !changes.IsSlug(strings.TrimSuffix(name, ".md")) {
			return Request{}, fmt.Errorf("not a release request: %q is no entry's name", name)
		}
	}
	return request, nil
}

// Override is the override file: a version and the reason it is given.
type Override struct {
	Version string `json:"version"`
	Reason  string `json:"reason"`
}

// DecodeOverride reads an override file strictly.
func DecodeOverride(data []byte) (Override, error) {
	var override Override
	if err := strictjson.Unmarshal(data, &override); err != nil {
		return Override{}, fmt.Errorf("the override is not {\"version\", \"reason\"}: %w", err)
	}
	if _, err := ParseVersion(override.Version); err != nil {
		return Override{}, fmt.Errorf("the override: %w", err)
	}
	if strings.TrimSpace(override.Reason) == "" {
		return Override{}, fmt.Errorf("the override has no reason")
	}
	return override, nil
}

// Matches says whether a request lists exactly a folder's entries and override, blob for blob.
// No rule is recomputed: this is what proves old releases.
func (request Request) Matches(listed changes.Listed) bool {
	if len(request.Entries) != len(listed.Entries) {
		return false
	}
	for _, listing := range listed.Entries {
		if request.Entries[path.Base(listing.File.Path)] != listing.File.Blob {
			return false
		}
	}
	switch {
	case request.Override == nil && listed.Override == nil:
		return true
	case request.Override == nil || listed.Override == nil:
		return false
	}
	return *request.Override == listed.Override.Blob
}

// RequestOf is the request a folder's entries make for a version, written on commit madeOn.
func RequestOf(listed changes.Listed, version Version, madeOn string) Request {
	request := Request{Version: version.String(), Entries: map[string]string{}, MadeOn: madeOn}
	for _, listing := range listed.Entries {
		request.Entries[path.Base(listing.File.Path)] = listing.File.Blob
	}
	if listed.Override != nil {
		blob := listed.Override.Blob
		request.Override = &blob
	}
	return request
}

// Agrees says whether a request matches the entries and asks for the version computed for them.
func (request Request) Agrees(listed changes.Listed, next Version) bool {
	return request.Matches(listed) && request.Version == next.String()
}
