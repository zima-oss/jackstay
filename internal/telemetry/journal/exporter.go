// Package journal is jackstay's journal of spans (docs/design/observability.md, section 4): an SDK
// span exporter that writes each ended span as one line of OTLP-JSON — a `TracesData` message of
// the OTLP v1 protocol in its JSON encoding (ids as hex, enums as numbers) — into a file per day
// under a project's `telemetry` folder, appended under a lock; the folder's upkeep (gzip, delete);
// and the reader `jst log` and `jst stats` use.
package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"golang.org/x/sys/unix"
)

// Limits of what one line holds.
const (
	// ValueLimit is the most of one string value written; a longer one is cut and the span marked.
	ValueLimit = 4096
	// LineLimit is the most of one line: a span over it is written without its events.
	LineLimit = 1 << 20
	// LockWait is how long a writer tries for the day file's lock before it drops the line.
	LockWait = 200 * time.Millisecond
	lockPoll = 5 * time.Millisecond
)

// TruncatedKey marks a span some value of which was cut.
const TruncatedKey = "jackstay.truncated"

// Reducer gives what is written of an attribute (the mask of section 7); nil writes it as it is.
type Reducer func(attribute.KeyValue) attribute.KeyValue

// Exporter writes ended spans into a telemetry folder. Every failure is swallowed: a span is
// worth no command's failure (section 4).
type Exporter struct {
	// Folder is `<jackstay home>/<project>/telemetry`.
	Folder string
	// Reduce is applied to every attribute of a span, its events and its resource.
	Reduce Reducer
	// ReduceText is applied to a status message.
	ReduceText func(string) string
	// Upkeep runs when a write created the day's file (nil: none).
	Upkeep func(folder string)
	// Now is the clock that names the day file (nil: time.Now).
	Now func() time.Time
	// Dropped counts lines not written (the lock not had, the folder not writable).
	Dropped int
}

// ExportSpans writes each span as a line. It never returns an error.
func (exporter *Exporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	for _, span := range spans {
		line, err := exporter.Line(span)
		if err != nil {
			exporter.Dropped++
			continue
		}
		created, err := AppendLine(exporter.Folder, exporter.day(), line)
		if err != nil {
			exporter.Dropped++
			continue
		}
		if created && exporter.Upkeep != nil {
			exporter.Upkeep(exporter.Folder)
		}
	}
	return nil
}

// Shutdown does nothing: every line is written when its span ends.
func (exporter *Exporter) Shutdown(context.Context) error { return nil }

func (exporter *Exporter) day() string {
	now := time.Now
	if exporter.Now != nil {
		now = exporter.Now
	}
	return now().Format(time.DateOnly)
}

// Line is the OTLP-JSON line of one span.
func (exporter *Exporter) Line(span sdktrace.ReadOnlySpan) ([]byte, error) {
	line, err := encode(exporter.message(span, true))
	if err == nil && len(line) > LineLimit {
		line, err = encode(exporter.message(span, false))
	}
	return line, err
}

func encode(message outData) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(message); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (exporter *Exporter) message(span sdktrace.ReadOnlySpan, withEvents bool) outData {
	truncated := !withEvents
	attributes := func(given []attribute.KeyValue) []outKeyValue {
		converted := make([]outKeyValue, 0, len(given))
		for _, pair := range given {
			if exporter.Reduce != nil {
				pair = exporter.Reduce(pair)
			}
			value, cut := anyValue(pair.Value)
			truncated = truncated || cut
			converted = append(converted, outKeyValue{Key: string(pair.Key), Value: value})
		}
		return converted
	}
	context := span.SpanContext()
	converted := outSpan{
		TraceID:           context.TraceID().String(),
		SpanID:            context.SpanID().String(),
		TraceState:        context.TraceState().String(),
		Name:              span.Name(),
		Kind:              int(span.SpanKind()),
		StartTimeUnixNano: unixNanos(span.StartTime()),
		EndTimeUnixNano:   unixNanos(span.EndTime()),
		Attributes:        attributes(span.Attributes()),
		Flags:             uint32(context.TraceFlags()) | 0x100, // the parent's remoteness is known
	}
	if parent := span.Parent(); parent.IsValid() {
		converted.ParentSpanID = parent.SpanID().String()
		if parent.IsRemote() {
			converted.Flags |= 0x200
		}
	}
	converted.DroppedAttributesCount = uint32(max(span.DroppedAttributes(), 0)) // #nosec G115 -- a count
	if withEvents {
		for _, event := range span.Events() {
			converted.Events = append(converted.Events, outEvent{Name: event.Name,
				TimeUnixNano: unixNanos(event.Time), Attributes: attributes(event.Attributes)})
		}
	}
	status := span.Status()
	converted.Status.Message = status.Description
	if exporter.ReduceText != nil {
		converted.Status.Message = exporter.ReduceText(status.Description)
	}
	// The SDK's codes are Unset 0, Error 1, Ok 2; OTLP's are Unset 0, Ok 1, Error 2.
	switch status.Code {
	case 1:
		converted.Status.Code = StatusError
	case 2:
		converted.Status.Code = StatusOK
	}
	if truncated {
		yes := true
		converted.Attributes = append(converted.Attributes, outKeyValue{Key: TruncatedKey, Value: outValue{BoolValue: &yes}})
	}
	scope := span.InstrumentationScope()
	var resource []outKeyValue
	if span.Resource() != nil {
		resource = attributes(span.Resource().Attributes())
	}
	return outData{ResourceSpans: []outResourceSpans{{
		Resource: outResource{Attributes: resource},
		ScopeSpans: []outScopeSpans{{
			Scope:     outScope{Name: scope.Name, Version: scope.Version},
			SchemaURL: scope.SchemaURL,
			Spans:     []outSpan{converted},
		}},
	}}}
}

func unixNanos(moment time.Time) string {
	if moment.IsZero() {
		return "0"
	}
	return strconv.FormatInt(moment.UnixNano(), 10)
}

// anyValue converts a value; true when a string was cut to ValueLimit.
func anyValue(value attribute.Value) (outValue, bool) {
	cut := false
	text := func(given string) *string {
		if len(given) > ValueLimit {
			cut = true
			given = given[:ValueLimit]
		}
		return &given
	}
	whole := func(number int64) *string {
		formatted := strconv.FormatInt(number, 10)
		return &formatted
	}
	array := func(values []outValue) outValue { return outValue{ArrayValue: &outArray{Values: values}} }
	switch value.Type() {
	case attribute.BOOL:
		flag := value.AsBool()
		return outValue{BoolValue: &flag}, false
	case attribute.INT64:
		return outValue{IntValue: whole(value.AsInt64())}, false
	case attribute.FLOAT64:
		number := value.AsFloat64()
		return outValue{DoubleValue: &number}, false
	case attribute.STRINGSLICE:
		values := []outValue{}
		for _, element := range value.AsStringSlice() {
			values = append(values, outValue{StringValue: text(element)})
		}
		return array(values), cut
	case attribute.INT64SLICE:
		values := []outValue{}
		for _, element := range value.AsInt64Slice() {
			values = append(values, outValue{IntValue: whole(element)})
		}
		return array(values), false
	case attribute.BOOLSLICE:
		values := []outValue{}
		for _, element := range value.AsBoolSlice() {
			flag := element
			values = append(values, outValue{BoolValue: &flag})
		}
		return array(values), false
	case attribute.FLOAT64SLICE:
		values := []outValue{}
		for _, element := range value.AsFloat64Slice() {
			number := element
			values = append(values, outValue{DoubleValue: &number})
		}
		return array(values), false
	}
	return outValue{StringValue: text(value.String())}, cut
}

// The OTLP v1 message `TracesData` in its JSON encoding (OTLP specification, "JSON Protobuf
// Encoding": lowerCamelCase names, ids as lowercase hex, enums as numbers, 64-bit integers as
// strings), written by encoding/json so no protobuf runtime is in the binary (section 8: the
// guard's budget). The pinned go.opentelemetry.io/proto/otlp is the reference: a test decodes
// every line with protojson into its tracepb.TracesData (package otlpcheck).
type outData struct {
	ResourceSpans []outResourceSpans `json:"resourceSpans"`
}

type outResourceSpans struct {
	Resource   outResource     `json:"resource"`
	ScopeSpans []outScopeSpans `json:"scopeSpans"`
}

type outResource struct {
	Attributes []outKeyValue `json:"attributes,omitempty"`
}

type outScopeSpans struct {
	Scope     outScope  `json:"scope"`
	Spans     []outSpan `json:"spans"`
	SchemaURL string    `json:"schemaUrl,omitempty"`
}

type outScope struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type outSpan struct {
	TraceID                string        `json:"traceId"`
	SpanID                 string        `json:"spanId"`
	TraceState             string        `json:"traceState,omitempty"`
	ParentSpanID           string        `json:"parentSpanId,omitempty"`
	Flags                  uint32        `json:"flags,omitempty"`
	Name                   string        `json:"name"`
	Kind                   int           `json:"kind,omitempty"`
	StartTimeUnixNano      string        `json:"startTimeUnixNano"`
	EndTimeUnixNano        string        `json:"endTimeUnixNano"`
	Attributes             []outKeyValue `json:"attributes,omitempty"`
	DroppedAttributesCount uint32        `json:"droppedAttributesCount,omitempty"`
	Events                 []outEvent    `json:"events,omitempty"`
	Status                 outStatus     `json:"status"`
}

type outEvent struct {
	TimeUnixNano string        `json:"timeUnixNano"`
	Name         string        `json:"name"`
	Attributes   []outKeyValue `json:"attributes,omitempty"`
}

type outStatus struct {
	Message string `json:"message,omitempty"`
	Code    int    `json:"code,omitempty"`
}

type outKeyValue struct {
	Key   string   `json:"key"`
	Value outValue `json:"value"`
}

type outValue struct {
	StringValue *string   `json:"stringValue,omitempty"`
	BoolValue   *bool     `json:"boolValue,omitempty"`
	IntValue    *string   `json:"intValue,omitempty"`
	DoubleValue *float64  `json:"doubleValue,omitempty"`
	ArrayValue  *outArray `json:"arrayValue,omitempty"`
}

type outArray struct {
	Values []outValue `json:"values"`
}

// FileName is a day's file in a telemetry folder.
func FileName(day string) string { return day + Suffix }

// Suffix ends every plain day file; a compressed one adds ".gz".
const Suffix = ".otlp.jsonl"

// ErrLocked says the day file's lock was not had within LockWait.
var ErrLocked = errors.New("the journal's day file is locked")

// AppendLine appends a line to a folder's day file under the file's lock, in one write; true when
// this call created the file.
func AppendLine(folder, day string, line []byte) (bool, error) {
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return false, err
	}
	place := filepath.Join(folder, FileName(day))
	created := true
	file, err := os.OpenFile(place, os.O_WRONLY|os.O_CREATE|os.O_APPEND|os.O_EXCL, 0o600) // #nosec G304 -- jackstay's own journal
	if errors.Is(err, os.ErrExist) {
		created = false
		file, err = os.OpenFile(place, os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- jackstay's own journal
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	if err := lockWithin(file, LockWait); err != nil {
		return created, err
	}
	defer func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }() // #nosec G115 -- a descriptor
	written, err := file.Write(line)
	if err == nil && written < len(line) {
		err = errors.New("a short write")
	}
	return created, err
}

func lockWithin(file *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) // #nosec G115 -- a descriptor
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return err
		}
		if time.Now().After(deadline) {
			return ErrLocked
		}
		time.Sleep(lockPoll)
	}
}
