package journal

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Span is one span as the reader gives it: flattened, its values plain Go values (string, int64,
// float64, bool, []any).
type Span struct {
	TraceID       string         `json:"trace_id"`
	SpanID        string         `json:"span_id"`
	ParentID      string         `json:"parent_id,omitempty"`
	Name          string         `json:"name"`
	Kind          int            `json:"kind"`
	Start         time.Time      `json:"start"`
	End           time.Time      `json:"end"`
	StatusCode    int            `json:"status_code"`
	StatusMessage string         `json:"status_message,omitempty"`
	Attributes    map[string]any `json:"attributes"`
	Events        []Event        `json:"events,omitempty"`
	Resource      map[string]any `json:"resource"`
	Scope         string         `json:"scope"`
	SchemaURL     string         `json:"schema_url,omitempty"`
	// Folder is the telemetry folder the span was read from.
	Folder string `json:"folder"`
}

// Event is a span event.
type Event struct {
	Name       string         `json:"name"`
	Time       time.Time      `json:"time"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Status codes of OTLP.
const (
	StatusUnset = 0
	StatusOK    = 1
	StatusError = 2
)

// Duration is how long the span lasted.
func (span Span) Duration() time.Duration { return span.End.Sub(span.Start) }

// Text is a string attribute, or "".
func (span Span) Text(key string) string { return textOf(span.Attributes[key]) }

// Number is an integer attribute, or 0.
func (span Span) Number(key string) int64 { return numberOf(span.Attributes[key]) }

// Texts is a list attribute of strings.
func (span Span) Texts(key string) []string { return textsOf(span.Attributes[key]) }

// ResourceText is a resource's string attribute.
func (span Span) ResourceText(key string) string { return textOf(span.Resource[key]) }

// ResourceNumber is a resource's integer attribute.
func (span Span) ResourceNumber(key string) int64 { return numberOf(span.Resource[key]) }

// Text is an event's string attribute.
func (event Event) Text(key string) string { return textOf(event.Attributes[key]) }

// Number is an event's integer attribute.
func (event Event) Number(key string) int64 { return numberOf(event.Attributes[key]) }

// Texts is an event's list attribute.
func (event Event) Texts(key string) []string { return textsOf(event.Attributes[key]) }

func textOf(value any) string {
	text, _ := value.(string)
	return text
}

func numberOf(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case float64:
		return int64(typed)
	}
	return 0
}

func textsOf(value any) []string {
	elements, _ := value.([]any)
	texts := make([]string, 0, len(elements))
	for _, element := range elements {
		if text, ok := element.(string); ok {
			texts = append(texts, text)
		}
	}
	return texts
}

// Result is what a read found besides its spans.
type Result struct {
	// Unreadable counts the lines that did not decode.
	Unreadable int
	// Files counts the files read; Folders the folders that could be read (one that does not exist
	// is no journal yet, not a problem).
	Files   int
	Folders int
	// Problems are what could not be read: a folder, a file, a broken .gz.
	Problems []string
}

// Read gives every span of the folders that overlaps [from, to): started before to and ended at
// or after from. Files are named by the end date, so every day from from's date through today is
// read; of one date, the plain file when it exists, else the .gz — never both (section 4).
func Read(folders []string, from, until, today time.Time, visit func(Span)) Result {
	return read(folders, from, until, today, 0, visit)
}

// ReadBounded gives the same window as Read with a byte limit on each line. An oversized
// line counts as unreadable, and does not prevent reading subsequent lines from the same file.
func ReadBounded(folders []string, from, until, today time.Time, maximumLineBytes int, visit func(Span)) Result {
	return read(folders, from, until, today, max(1, maximumLineBytes), visit)
}

func read(folders []string, from, until, today time.Time, maximumLineBytes int, visit func(Span)) Result {
	var result Result
	// Day files are named by the local date: the range's ends are read as local dates (an RFC 3339
	// bound in UTC names another calendar day near midnight).
	first := dayStart(from.In(time.Local))
	last := dayStart(today.In(time.Local))
	for _, folder := range folders {
		if _, err := os.ReadDir(folder); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				result.Problems = append(result.Problems, err.Error())
			}
			continue
		}
		result.Folders++
		for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
			place := filepath.Join(folder, FileName(day.Format(time.DateOnly)))
			reader, closer, err := openDay(place)
			if err != nil {
				result.Problems = append(result.Problems, err.Error())
			}
			if reader == nil {
				continue
			}
			result.Files++
			if err := readLines(reader, folder, from, until, maximumLineBytes, visit, &result); err != nil {
				result.Problems = append(result.Problems, place+": "+err.Error())
			}
			closer()
		}
	}
	return result
}

// openDay opens a date's file: the plain one, else the .gz; nil and no error when neither exists.
func openDay(place string) (io.Reader, func(), error) {
	file, err := os.Open(place) // #nosec G304 -- jackstay's own journal
	if err == nil {
		return file, func() { _ = file.Close() }, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	file, err = os.Open(place + ".gz") // #nosec G304 -- jackstay's own journal
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	reader, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s.gz: %w", place, err)
	}
	return reader, func() { _ = reader.Close(); _ = file.Close() }, nil
}

func readLines(reader io.Reader, folder string, from, until time.Time, maximumLineBytes int, visit func(Span), result *Result) error {
	lines := bufio.NewReader(reader)
	var line []byte
	oversized := false
	for {
		chunk, err := lines.ReadSlice('\n')
		if !oversized {
			if maximumLineBytes > 0 && len(chunk) > maximumLineBytes-len(line) {
				oversized = true
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		switch {
		case oversized:
			result.Unreadable++
		case len(bytes.TrimSpace(line)) > 0:
			spans, problem := DecodeLine(line)
			if problem != nil {
				result.Unreadable++
			}
			for _, span := range spans {
				if span.Start.Before(until) && !span.End.Before(from) {
					span.Folder = folder
					visit(span)
				}
			}
		}
		line, oversized = line[:0], false
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// The OTLP JSON shape, as far as jackstay reads it.
type wireData struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []wireKeyValue `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Scope struct {
				Name string `json:"name"`
			} `json:"scope"`
			SchemaURL string     `json:"schemaUrl"`
			Spans     []wireSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type wireSpan struct {
	TraceID      string         `json:"traceId"`
	SpanID       string         `json:"spanId"`
	ParentSpanID string         `json:"parentSpanId"`
	Name         string         `json:"name"`
	Kind         int            `json:"kind"`
	Start        wireNumber     `json:"startTimeUnixNano"`
	End          wireNumber     `json:"endTimeUnixNano"`
	Attributes   []wireKeyValue `json:"attributes"`
	Events       []struct {
		Name       string         `json:"name"`
		Time       wireNumber     `json:"timeUnixNano"`
		Attributes []wireKeyValue `json:"attributes"`
	} `json:"events"`
	Status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
}

type wireKeyValue struct {
	Key   string    `json:"key"`
	Value wireValue `json:"value"`
}

type wireValue struct {
	StringValue *string     `json:"stringValue"`
	BoolValue   *bool       `json:"boolValue"`
	IntValue    *wireNumber `json:"intValue"`
	DoubleValue *float64    `json:"doubleValue"`
	ArrayValue  *struct {
		Values []wireValue `json:"values"`
	} `json:"arrayValue"`
}

// wireNumber is an int64 the JSON encoding writes as a string or a number. The encoding allows
// unsigned 64-bit times too; one beyond the int64 range (after the year 2262) is refused, not
// wrapped round to a negative time.
type wireNumber int64

func (number *wireNumber) UnmarshalJSON(data []byte) error {
	parsed, err := strconv.ParseInt(string(bytes.Trim(data, `"`)), 10, 64)
	if err != nil {
		return err
	}
	*number = wireNumber(parsed)
	return nil
}

func (value wireValue) plain() any {
	switch {
	case value.StringValue != nil:
		return *value.StringValue
	case value.BoolValue != nil:
		return *value.BoolValue
	case value.IntValue != nil:
		return int64(*value.IntValue)
	case value.DoubleValue != nil:
		return *value.DoubleValue
	case value.ArrayValue != nil:
		elements := make([]any, 0, len(value.ArrayValue.Values))
		for _, element := range value.ArrayValue.Values {
			elements = append(elements, element.plain())
		}
		return elements
	}
	return nil
}

func plainMap(pairs []wireKeyValue) map[string]any {
	found := make(map[string]any, len(pairs))
	for _, pair := range pairs {
		found[pair.Key] = pair.Value.plain()
	}
	return found
}

func nanosTime(number wireNumber) time.Time {
	if number == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(number))
}

// DecodeLine reads one OTLP-JSON line into spans.
func DecodeLine(line []byte) ([]Span, error) {
	var data wireData
	if err := json.Unmarshal(line, &data); err != nil {
		return nil, err
	}
	var spans []Span
	for _, resourceSpans := range data.ResourceSpans {
		resource := plainMap(resourceSpans.Resource.Attributes)
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			for _, wire := range scopeSpans.Spans {
				span := Span{TraceID: wire.TraceID, SpanID: wire.SpanID, ParentID: wire.ParentSpanID, Name: wire.Name,
					Kind: wire.Kind, Start: nanosTime(wire.Start), End: nanosTime(wire.End),
					StatusCode: wire.Status.Code, StatusMessage: wire.Status.Message,
					Attributes: plainMap(wire.Attributes), Resource: resource, Scope: scopeSpans.Scope.Name,
					SchemaURL: scopeSpans.SchemaURL}
				for _, event := range wire.Events {
					span.Events = append(span.Events, Event{Name: event.Name, Time: nanosTime(event.Time),
						Attributes: plainMap(event.Attributes)})
				}
				spans = append(spans, span)
			}
		}
	}
	return spans, nil
}

// SortByStart orders spans by start, then by end.
func SortByStart(spans []Span) {
	sort.SliceStable(spans, func(left, right int) bool {
		if !spans[left].Start.Equal(spans[right].Start) {
			return spans[left].Start.Before(spans[right].Start)
		}
		return spans[left].End.Before(spans[right].End)
	})
}
