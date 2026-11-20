// Package delivery stores a bounded checkpoint for a local multi-step operation.
// Callers serialize access and reconcile checkpoints with observed effects before restarting.
package delivery

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

const schema = 1

// Limit bounds each operation record, independent of the number of attempts.
const Limit = 1 << 20

// Outcome is the last recorded state of an attempt.
type Outcome string

const (
	// Running is written before an effect, so a sudden stop leaves a readable checkpoint.
	Running Outcome = "running"
	// Interrupted is written when an attempt returns before completion.
	Interrupted Outcome = "interrupted"
	// Complete is written when the caller has confirmed all effects.
	Complete Outcome = "complete"
)

// ErrUnversioned permits a caller to identify and migrate a legacy plan.
var ErrUnversioned = errors.New("the delivery record has no schema")

// Record carries an immutable operation plan and the last attempt's checkpoint. A resumed
// operation keeps its ID and increments Attempt. Plan should hold facts or digests, never secrets.
type Record[Plan any] struct {
	Schema  int       `json:"schema"`
	ID      string    `json:"id"`
	Attempt int       `json:"attempt"`
	Phase   string    `json:"phase"`
	Outcome Outcome   `json:"outcome"`
	Updated time.Time `json:"updated"`
	Plan    Plan      `json:"plan"`
}

// New starts a new operation, with its first attempt pending Begin.
func New[Plan any](plan Plan) *Record[Plan] {
	return &Record[Plan]{Schema: schema, ID: rand.Text(), Plan: plan}
}

// Digest identifies bytes without retaining their content.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Read loads a record. A caller may decode returned legacy bytes if there is no schema.
func Read[Plan any](place string) (*Record[Plan], []byte, error) {
	data, err := durable.ReadLimited(place, Limit)
	if errors.Is(err, durable.ErrTooLarge) {
		return nil, nil, fmt.Errorf("delivery record exceeds %d bytes", Limit)
	}
	if err != nil {
		return nil, nil, err
	}
	var header struct {
		Schema *int `json:"schema"`
	}
	if err := strictjson.UnmarshalOpen(data, &header); err != nil {
		return nil, nil, err
	}
	if header.Schema == nil {
		return nil, data, ErrUnversioned
	}
	if *header.Schema != schema {
		return nil, nil, fmt.Errorf("unsupported delivery record schema %d", *header.Schema)
	}
	var record Record[Plan]
	if err := strictjson.Unmarshal(data, &record); err != nil {
		return nil, nil, err
	}
	if record.ID == "" || record.Attempt <= 0 || record.Phase == "" || record.Updated.IsZero() {
		return nil, nil, errors.New("incomplete delivery record")
	}
	switch record.Outcome {
	case Running, Interrupted, Complete:
		return &record, nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown delivery outcome %q", record.Outcome)
	}
}

// Begin persists an attempt before any effect. It also starts an additional attempt on replay.
func (record *Record[Plan]) Begin(place string, phase string) error {
	record.Attempt++
	return record.Checkpoint(place, phase, Running)
}

// Checkpoint atomically saves the selected phase and outcome.
func (record *Record[Plan]) Checkpoint(place string, phase string, outcome Outcome) error {
	record.Phase, record.Outcome, record.Updated = phase, outcome, time.Now().UTC()
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) >= Limit {
		return fmt.Errorf("delivery record exceeds %d bytes", Limit)
	}
	return durable.WriteAtomic(place, append(data, '\n'), 0o600, 0)
}
