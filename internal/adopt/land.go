package adopt

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/adopt/jsondoc"
	"github.com/zima-oss/jackstay/internal/adopt/profile"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/install"
)

// The two answers to adopt's one question (docs/design/land.md section 5), recommended first.
const (
	AnswerMergeWhenGreen = "The agent merges when the required checks pass; `jackstay.json` and the listed paths need a person (recommended)"
	AnswerPrepare        = "The agent prepares; a person merges."
)

// upgradeSteps are the three pull requests that take a project from jackstay 0.5.x to a schema-3
// file with a `land` section, each merged by a person.
var upgradeSteps = []string{
	"the pin: `jst adopt --apply --pin --settings-only` moves adopt.pin to this jackstay in jackstay.json alone (schema 2)",
	"the workflows: on the merged base, `jst adopt --apply` writes the workflows that download the new pin",
	"schema 3: on the merged base, `jst adopt --apply` writes jackstay.json with `jackstay_schema` 3 and the `land` section",
}

// landSection plans the `land` section (docs/design/land.md section 5) for a jackstay that reads
// schema 3: an existing section gets the keys it lacks, its autonomy kept; a file that is or
// becomes schema 3 gets the section whole. A schema-2 file of a project upgrading from 0.5.x gets
// it only as the third of three pull requests: after the pin and after the workflows that download
// it, which the second step writes as the coming policy needs them (Make decides when the second
// step is done). The section is decided from
// the project's own file: a profile's defaults fill a new section's other keys, never autonomy.
// changed says the pin and the schema must be settled again.
func (planner *planner) landSection(original *jsondoc.Object, chosen *profile.Profile) (changed bool, err error) {
	if install.Compare(planner.options.Version, config.Schema3Release) < 0 {
		return false, nil
	}
	document := planner.document
	if existing := original.Object("land"); existing != nil {
		current, _ := getString(existing, "autonomy")
		if given := planner.options.Autonomy; given != "" && given != current {
			return false, &UsageError{fmt.Sprintf("--autonomy %s: jackstay.json's land.autonomy is %q, and jst adopt never changes it; "+
				"change it there yourself, in a pull request a person merges", given, current)}
		}
		fillLand(document.Object("land"))
		return false, nil
	}
	if planner.existed {
		pin, _ := getString(original.Object("adopt"), "pin")
		if pin != "" && install.Compare(strings.TrimPrefix(pin, "v"), config.Schema3Release) < 0 {
			if err := planner.pinStepKeepsSchema(original); err != nil {
				return false, err
			}
			planner.upgradeNotes(1)
			return false, nil
		}
		// The upgrade is due by the project's own file, never by a schema this plan would raise: a
		// schema-2 file whose workflows download a pinned jackstay. Without a pin no workflow of
		// jackstay's downloads one yet, so the file moves to schema 3 with this apply's own pin.
		if schema, _ := original.Get("jackstay_schema"); pin != "" && fmt.Sprint(schema) != fmt.Sprint(config.Schema) {
			if !planner.options.landNow {
				// Steps 2 and 3: the workflows are those the coming policy needs; Make judges from
				// this whole plan whether step 2 is done, and plans again with landNow if it is.
				// A schema-3 key this apply adds waits for step 3.
				planner.upgrading, planner.landComing = true, true
				planner.deferred = planner.schema3Added(original)
				return true, nil
			}
			planner.upgradeNotes(3)
		}
	}
	autonomy := planner.options.Autonomy
	if autonomy == "" {
		planner.plan.asksAutonomy = true
		autonomy = planner.options.Offered
		if autonomy == "" {
			autonomy = config.AutonomyMergeWhenGreen
		}
		planner.note(fmt.Sprintf("land.autonomy is a person's choice: --apply asks it at a terminal, or takes --autonomy "+
			"%s|%s (the plan shows %s)", config.AutonomyMergeWhenGreen, config.AutonomyPrepare, autonomy))
	}
	section := jsondoc.NewObject()
	section.Set("host", "github")
	section.Set("autonomy", autonomy)
	if planner.profileLand != nil {
		for _, key := range planner.profileLand.Keys() {
			if key != "autonomy" {
				value, _ := planner.profileLand.Get(key)
				section.Set(key, value)
			}
		}
	}
	fillLand(section)
	document.Set("land", section)
	return true, nil
}

// fillLand adds the keys a `land` section lacks, with their defaults, so the file says it all.
func fillLand(section *jsondoc.Object) {
	for _, key := range []string{"host", "method", "checks", "human_paths"} {
		if _, present := section.Get(key); present {
			continue
		}
		switch key {
		case "host":
			section.Set(key, "github")
		case "method":
			section.Set(key, "squash")
		default:
			section.Set(key, []any{})
		}
	}
}

// upgradeNotes say the three steps of the upgrade in order and which one is due.
func (planner *planner) upgradeNotes(due int) {
	for index, step := range upgradeSteps {
		state := "to come"
		switch {
		case index+1 < due:
			state = "done"
		case index+1 == due:
			state = "due"
		}
		planner.note(fmt.Sprintf("upgrade to the land section, step %d of 3 (%s): %s", index+1, state, step))
	}
}

// schema3Added are the keys needing schema 3 that this plan adds to the project's file.
func (planner *planner) schema3Added(original *jsondoc.Object) []string {
	before := config.Schema3Keys(config.Config(jsondoc.ToPlain(original).(map[string]any)))
	var added []string
	for _, key := range config.Schema3Keys(planner.plain) {
		if !slices.Contains(before, key) {
			added = append(added, key)
		}
	}
	return added
}

// schemaWaits refuses a key needing schema 3 before the upgrade's third step: the 0.5.x the
// workflows still download would refuse the file (adopt.profile_sha256 from --profile-sha256, say).
func schemaWaits(step int, added []string) error {
	return &UsageError{fmt.Sprintf("the upgrade's step %d of 3 keeps jackstay.json at schema 2, and this apply would add `%s`, "+
		"which needs schema 3: leave it out now (record the profile digest in step 3, or after the upgrade)",
		step, strings.Join(added, "`, `"))}
}

// pinStepKeepsSchema refuses a first step that would raise jackstay.json to schema 3.
func (planner *planner) pinStepKeepsSchema(original *jsondoc.Object) error {
	if added := planner.schema3Added(original); len(added) > 0 {
		return schemaWaits(1, added)
	}
	return nil
}

// ConfirmWorkflows is the exception by which a person confirms that no workflow the project keeps
// (its own, or kept by an exception) still downloads a jackstay older than 0.6.0, directly or
// through a script or an action: jackstay does not read those files to find out.
const ConfirmWorkflows = "upgrade:workflows"

// stepTwoLeft is what keeps the upgrade's second step from being done, read from a whole plan with
// its settlements and exceptions: a managed workflow still to write or settle (pending), and any
// workflow the project keeps while nobody has confirmed them with the exception ConfirmWorkflows
// (a blocker naming them).
func (planner *planner) stepTwoLeft(confirmed bool) (pending bool, blockers []string) {
	for _, change := range planner.plan.Changes {
		pending = pending || strings.HasPrefix(change.ID, "workflow:")
	}
	for _, conflict := range planner.plan.Conflicts {
		pending = pending || strings.HasPrefix(conflict.ID, "workflow:")
	}
	if len(planner.retained) > 0 && !confirmed {
		kept := slices.Clone(planner.retained)
		slices.Sort(kept)
		blockers = append(blockers, fmt.Sprintf("step 3 is not offered: the project keeps %s, which jst adopt does not manage; "+
			"once you have checked that none still downloads a jackstay older than %s (directly, or through a script or an action), "+
			"confirm it in jackstay.json: \"adopt\": {\"exceptions\": {\"%s\": \"<what you checked>\"}}",
			strings.Join(kept, ", "), config.Schema3Release, ConfirmWorkflows))
	}
	return pending, blockers
}

// AsksAutonomy says the plan writes a new `land` section whose autonomy nobody chose: --apply asks a
// person at a terminal, or needs --autonomy.
func (plan *Plan) AsksAutonomy() bool { return plan.asksAutonomy }

// AskAutonomy asks the person at the terminal adopt's one question and returns the value chosen.
// first is offered first and taken on an empty answer ("": merge-when-green, the recommended one).
func AskAutonomy(input *bufio.Reader, output io.Writer, first string) (string, error) {
	options := []string{config.AutonomyMergeWhenGreen, config.AutonomyPrepare}
	if first == config.AutonomyPrepare {
		options = []string{config.AutonomyPrepare, config.AutonomyMergeWhenGreen}
	}
	answers := map[string]string{config.AutonomyMergeWhenGreen: AnswerMergeWhenGreen, config.AutonomyPrepare: AnswerPrepare}
	fmt.Fprintln(output, "Who merges this project's changes (land.autonomy)?")
	for index, option := range options {
		fmt.Fprintf(output, "  %d) %s\n", index+1, answers[option])
	}
	for {
		fmt.Fprint(output, "Answer [1]: ")
		line, err := input.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		switch answer {
		case "", "1":
			if answer == "" && err != nil {
				return "", errors.New("no answer: standard input ended")
			}
			return options[0], nil
		case "2":
			return options[1], nil
		case config.AutonomyMergeWhenGreen, config.AutonomyPrepare:
			return answer, nil
		}
		if err != nil {
			return "", errors.New("no answer: standard input ended")
		}
		fmt.Fprintln(output, "Answer 1 or 2.")
	}
}

// machineFile is where `jst setup` keeps this machine's answers, under `<jackstay home>/.all`.
const machineFile = "setup.json"

// machineSettings are this machine's answers to `jst setup`'s questions.
type machineSettings struct {
	LandAutonomy string `json:"land_autonomy,omitempty"`
}

func machinePath() (string, error) { return homedir.SharedPath(machineFile) }

// MachineAutonomy is the answer `jst setup` kept for this machine ("": none yet), which adopt offers
// first; the project's file decides.
func MachineAutonomy() (string, error) {
	path, err := machinePath()
	if err != nil {
		return "", err
	}
	data, err := durable.ReadLimited(path, 1<<16)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var settings machineSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	switch settings.LandAutonomy {
	case "", config.AutonomyMergeWhenGreen, config.AutonomyPrepare:
		return settings.LandAutonomy, nil
	}
	return "", fmt.Errorf("%s: land_autonomy must be %q or %q: %q", path, config.AutonomyMergeWhenGreen, config.AutonomyPrepare, settings.LandAutonomy)
}

// SaveMachineAutonomy keeps value as this machine's answer.
func SaveMachineAutonomy(value string) error {
	if value != config.AutonomyMergeWhenGreen && value != config.AutonomyPrepare {
		return fmt.Errorf("land autonomy must be %q or %q: %q", config.AutonomyMergeWhenGreen, config.AutonomyPrepare, value)
	}
	path, err := machinePath()
	if err != nil {
		return err
	}
	return durable.Update(context.Background(), path, 0o600, 1<<16, func(current []byte) ([]byte, error) {
		members := map[string]any{}
		if len(current) > 0 {
			if err := json.Unmarshal(current, &members); err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
			}
		}
		members["land_autonomy"] = value
		data, err := json.MarshalIndent(members, "", "  ")
		return append(data, '\n'), err
	})
}
