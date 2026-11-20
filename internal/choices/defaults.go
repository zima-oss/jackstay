package choices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/homedir"
)

// defaultsLimit bounds the machine defaults file.
const defaultsLimit = 64 << 10

// Defaults are the machine's answers setup keeps, by question ID.
type Defaults map[string]string

// Store keeps the machine defaults.
type Store interface {
	Load() (Defaults, error)
	Save(Defaults) error
}

// FileStore keeps them in a JSON object at a path; MachineStore is the account's.
type FileStore struct{ Path string }

// MachineStore is `<jackstay home>/.all/choices.json`. Setup writes it from outside the agent
// sandbox, which may only read it.
func MachineStore() (FileStore, error) {
	path, err := homedir.SharedPath("choices.json")
	return FileStore{Path: path}, err
}

// Load is the defaults; none when the file does not exist. A value that is no option of its
// question is dropped, so an old file never offers what this jackstay cannot write.
func (store FileStore) Load() (Defaults, error) {
	data, err := durable.ReadLimited(store.Path, defaultsLimit)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", store.Path, err)
	}
	kept := Defaults{}
	for id, value := range raw {
		if question, known := Find(id); known && question.Machine && question.Has(value) {
			kept[id] = value
		}
	}
	return kept, nil
}

// Save replaces the file whole, under its lock.
func (store FileStore) Save(defaults Defaults) error {
	ids := make([]string, 0, len(defaults))
	for id := range defaults {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	ordered := make(map[string]string, len(ids))
	for _, id := range ids {
		ordered[id] = defaults[id]
	}
	data, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return err
	}
	if err := durable.MakeFolder(filepath.Dir(store.Path), 0o700); err != nil {
		return err
	}
	return durable.Update(context.Background(), store.Path, 0o600, defaultsLimit, func([]byte) ([]byte, error) {
		return append(data, '\n'), nil
	})
}
