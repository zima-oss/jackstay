package chatstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// Bindings tie a Claude Code process to its session (sections 3.1 and 6.2): its SessionStart hook
// writes one, its MCP server and the native pass read it. The messaging token is never stored.

const (
	bindingBound = 8 << 10
	maxBindings  = 256
)

// Binding is a binding record.
type Binding struct {
	V             int    `json:"v"`
	ProcessID     int    `json:"process_id"`
	ProcessStart  string `json:"process_start"`
	Key           string `json:"key"`
	Cwd           string `json:"cwd"`
	Socket        string `json:"socket,omitempty"`
	ClaudeVersion string `json:"claude_version,omitempty"`
	Written       string `json:"written"`
}

func (place layout) bindings() string { return filepath.Join(place.root, "bindings") }

func (place layout) binding(processID int, start string) string {
	return filepath.Join(place.root, "bindings", strconv.Itoa(processID)+"-"+hashOf(start)[:16]+".json")
}

// Bind writes the binding of a Claude Code process, replacing its earlier one (a /clear or a resume
// moves the process to a new key).
func (store *Store) Bind(ctx context.Context, binding Binding) error {
	if binding.ProcessID < 1 || binding.ProcessStart == "" || len(binding.Key) > 140 || len(binding.Cwd) > 4096 ||
		len(binding.Socket) > 1024 || len(binding.ClaudeVersion) > 32 {
		return refuse(ReasonInvalid, "a binding names a process, its start and a session key within their bounds")
	}
	return store.update(ctx, func(transaction *transaction) error {
		path := transaction.place.binding(binding.ProcessID, binding.ProcessStart)
		if !exists(path) {
			// Bindings are only the ring's socket addresses, bounded like the sessions: a new one past
			// the bound evicts the one written longest ago (a process that ended without its
			// SessionEnd leaves its binding until then; a ring to its socket is harmless).
			bindings, err := transaction.listBindings()
			if err != nil {
				return err
			}
			for len(bindings) >= maxBindings {
				oldest := 0
				for index := range bindings {
					if bindings[index].Written < bindings[oldest].Written {
						oldest = index
					}
				}
				evicted := bindings[oldest]
				if err := os.Remove(transaction.place.binding(evicted.ProcessID, evicted.ProcessStart)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				bindings = slices.Delete(bindings, oldest, oldest+1)
			}
			fits, err := transaction.admit(deliverystate.BindingCharge, transaction.store.writeLimit)
			if err != nil {
				return err
			}
			if !fits {
				return refuse(ReasonCapacity, "the chat store holds no room for another binding")
			}
		}
		binding.V, binding.Written = 1, stamp(transaction.now)
		return writeRecord(path, binding, bindingBound)
	})
}

// Bindings lists every binding.
func (store *Store) Bindings(ctx context.Context) ([]Binding, error) {
	var result []Binding
	err := store.update(ctx, func(transaction *transaction) error {
		bindings, err := transaction.listBindings()
		result = bindings
		return err
	})
	return result, err
}

func (transaction *transaction) listBindings() ([]Binding, error) {
	names, err := listNames(transaction.place.bindings())
	if err != nil {
		return nil, err
	}
	var bindings []Binding
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		var binding Binding
		if err := readRecord(filepath.Join(transaction.place.bindings(), name), bindingBound, &binding); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

// Unbind removes the binding of a process that is gone (section 7.4: at once).
func (store *Store) Unbind(ctx context.Context, processID int, start string) error {
	return store.update(ctx, func(transaction *transaction) error {
		if err := os.Remove(transaction.place.binding(processID, start)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}
