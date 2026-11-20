package choices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/homedir"
)

// MachineFile is where `jst setup` keeps this machine's answers, under `<jackstay home>/.all`: one
// JSON object shared with land_autonomy (internal/adopt), each answer under its question's ID.
const MachineFile = "setup.json"

// machineLimit bounds the file.
const machineLimit = 1 << 16

// Defaults are this machine's answers, by question ID.
type Defaults map[string]string

// MachineAnswers reads this machine's answers to these questions ({} without a file). A value that
// is none of its question's options is dropped, so an old file never offers what cannot be written.
func MachineAnswers() (Defaults, error) {
	path, err := homedir.SharedPath(MachineFile)
	if err != nil {
		return nil, err
	}
	data, err := durable.ReadLimited(path, machineLimit)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults{}, nil
	}
	if err != nil {
		return nil, err
	}
	var members map[string]any
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	kept := Defaults{}
	for _, question := range All() {
		if value, _ := members[question.ID].(string); question.Machine && question.Has(value) {
			kept[question.ID] = value
		}
	}
	return kept, nil
}

// SaveMachineAnswers keeps the answers given, under the file's lock, leaving every other member of
// the file (land_autonomy among them) as it is.
func SaveMachineAnswers(answers Defaults) error {
	path, err := homedir.SharedPath(MachineFile)
	if err != nil {
		return err
	}
	return durable.Update(context.Background(), path, 0o600, machineLimit, func(current []byte) ([]byte, error) {
		members := map[string]any{}
		if len(current) > 0 {
			if err := json.Unmarshal(current, &members); err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
			}
		}
		for id, value := range answers {
			members[id] = value
		}
		data, err := json.MarshalIndent(members, "", "  ")
		return append(data, '\n'), err
	})
}
