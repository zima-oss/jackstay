package adopt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/zima-oss/jackstay/internal/adopt/profile"
	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/delivery"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/homedir"
)

// adoptionDelivery records only paths and content digests. A missing file is represented by an
// empty string. Desired file contents are recomputed from the current project and its profile.
type adoptionDelivery struct {
	Inputs  map[string]string `json:"inputs"`
	Targets map[string]string `json:"targets"`
	Profile string            `json:"profile"`
}

func deliveryPath(root string) (string, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	return homedir.SharedPath("delivery", delivery.Digest([]byte(resolved)), "adopt.json")
}

func (plan *Plan) deliveryInputs() (adoptionDelivery, error) {
	inputs := adoptionDelivery{Inputs: make(map[string]string, len(plan.inputs)), Targets: make(map[string]string, len(plan.writes))}
	for relative, input := range plan.inputs {
		if input.exists {
			inputs.Inputs[relative] = delivery.Digest([]byte(input.text))
		} else {
			inputs.Inputs[relative] = ""
		}
	}
	for _, pending := range plan.writes {
		if _, read := inputs.Inputs[pending.path]; !read {
			inputs.Inputs[pending.path] = ""
		}
		if !pending.remove {
			inputs.Targets[pending.path] = delivery.Digest(pending.data)
		} else {
			inputs.Targets[pending.path] = ""
		}
	}
	if plan.profile != nil {
		data, err := json.Marshal(plan.profile)
		if err != nil {
			return adoptionDelivery{}, err
		}
		inputs.Profile = delivery.Digest(data)
	}
	return inputs, nil
}

// canResume permits a missing write only if its recorded effect is already visible. An edited
// input or a different plan starts a fresh operation under the usual adoption conflict rules.
func (snapshot adoptionDelivery) canResume(candidate adoptionDelivery) bool {
	if snapshot.Profile != candidate.Profile {
		return false
	}
	for relative, before := range snapshot.Inputs {
		after, wasTarget := snapshot.Targets[relative]
		next, willWrite := candidate.Targets[relative]
		actual := candidate.Inputs[relative]
		switch {
		case willWrite && (!wasTarget || before != actual || after != next):
			return false
		case !willWrite && wasTarget && after != actual:
			return false
		case !wasTarget && before != actual:
			return false
		}
	}
	for relative := range candidate.Inputs {
		if _, known := snapshot.Inputs[relative]; !known {
			return false
		}
	}
	return true
}

// Apply checks the inputs before the first write and rechecks each target before replacing it.
// A later apply uses the saved attempt only if it observes the same intended effects.
func (plan *Plan) Apply() error {
	return plan.apply(changes.WriteFileAtomic)
}

func (plan *Plan) apply(writeFile func(string, []byte) error) error {
	if len(plan.Conflicts) > 0 {
		return ErrConflicts
	}
	place, err := deliveryPath(plan.Root)
	if err != nil {
		return err
	}
	if len(plan.writes) == 0 {
		if _, err := os.Stat(place); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	held, err := flock.WaitFile(ctx, place+".lock")
	if err != nil {
		return fmt.Errorf("locking adoption delivery: %w", err)
	}
	defer held.Release()
	if err := plan.checkInputs(); err != nil {
		return err
	}
	record, _, err := delivery.Read[adoptionDelivery](place)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading the adoption delivery record: %w", err)
	}
	inputs, err := plan.deliveryInputs()
	if err != nil {
		return err
	}
	resume := record != nil && record.Outcome != delivery.Complete && record.Plan.canResume(inputs)
	if !resume {
		if len(plan.writes) == 0 {
			return nil
		}
		record = delivery.New(inputs)
	}
	if err := record.Begin(place, "apply-files"); err != nil {
		return fmt.Errorf("recording the adoption plan: %w", err)
	}
	if err := plan.applyFiles(place, record, writeFile); err != nil {
		return errors.Join(err, record.Checkpoint(place, record.Phase, delivery.Interrupted))
	}
	return record.Checkpoint(place, "apply-files", delivery.Complete)
}

func (plan *Plan) checkInputs() error {
	if plan.profile != nil {
		var current *profile.Profile
		var err error
		if plan.ProfileSHA256 != "" {
			current, err = profile.LoadPinned(plan.profile.Path, plan.ProfileSHA256)
		} else {
			current, err = profile.Load(plan.profile.Path)
		}
		if err != nil {
			return fmt.Errorf("checking the profile before apply: %w", err)
		}
		if !reflect.DeepEqual(current, plan.profile) {
			return errors.New("the profile changed after planning; rerun jst adopt to review the new state")
		}
	}
	paths := make([]string, 0, len(plan.inputs)+len(plan.writes))
	for relative := range plan.inputs {
		paths = append(paths, relative)
	}
	for _, pending := range plan.writes {
		if _, read := plan.inputs[pending.path]; !read {
			paths = append(paths, pending.path)
		}
	}
	slices.Sort(paths)
	for _, relative := range paths {
		if err := plan.checkInput(relative, plan.inputs[relative]); err != nil {
			return err
		}
	}
	return nil
}

func (plan *Plan) applyFiles(place string, record *delivery.Record[adoptionDelivery], writeFile func(string, []byte) error) error {
	for _, pending := range plan.writes {
		if err := record.Checkpoint(place, pending.path, delivery.Running); err != nil {
			return err
		}
		if err := plan.checkInput(pending.path, plan.inputs[pending.path]); err != nil {
			return err
		}
		target := filepath.Join(plan.Root, filepath.FromSlash(pending.path))
		if pending.remove {
			if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err := writeFile(target, pending.data); err != nil {
			return fmt.Errorf("%s: %w", pending.path, err)
		}
	}
	return nil
}
