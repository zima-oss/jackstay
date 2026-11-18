package flock

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/zima-oss/jackstay/internal/telemetry"
)

// waitSpan is the span of a wait for a lock (docs/design/observability.md, section 2), made only
// once the lock turns out not to be free, and dated from when the wait began.
type waitSpan struct {
	path  string
	began time.Time
	span  *telemetry.Span
}

// waiting starts the span, once.
func (wait *waitSpan) waiting() {
	if wait.span != nil {
		return
	}
	_, wait.span = telemetry.Start(context.Background(), "wait "+filepath.Base(wait.path), telemetry.Options{StartAt: wait.began,
		Attributes: []attribute.KeyValue{attribute.String(telemetry.AttrLockName, filepath.Base(wait.path)),
			attribute.String(telemetry.AttrLockPath, wait.path)}})
}

var holderPID = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\(the lock's (\d+)\)`) })

// end ends the span by how the wait ended; the holder before is named by its pid only (its note
// holds its argv, which is never recorded: section 7).
func (wait *waitSpan) end(err error, holderNote string) {
	if wait.span == nil {
		return
	}
	switch {
	case err == nil:
		var attributes []attribute.KeyValue
		if found := holderPID().FindStringSubmatch(holderNote); found != nil {
			if pid, parseErr := strconv.Atoi(found[1]); parseErr == nil {
				attributes = append(attributes, attribute.Int(telemetry.AttrLockHolderPID, pid))
			}
		}
		wait.span.Event(telemetry.EventLockAcquired, attributes...)
		wait.span.End("acquired")
	case errors.As(err, new(*GaveUp)):
		wait.span.Set(attribute.Bool(telemetry.AttrWaitGaveUp, true))
		wait.span.Fail("gave_up", "")
		wait.span.End("gave-up")
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		wait.span.Fail("cancelled", "")
		wait.span.End("cancelled")
	default:
		wait.span.Fail("unusable", "")
		wait.span.End("failed")
	}
}
