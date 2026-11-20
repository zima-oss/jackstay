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
// it. added says the section was added.
func (planner *planner) landSection(original *jsondoc.Object, chosen *profile.Profile) (added bool, err error) {
	if install.Compare(planner.options.Version, config.Schema3Release) < 0 {
		return false, nil
	}
	document := planner.document
	if section := document.Object("land"); section != nil {
		current, _ := getString(section, "autonomy")
		if given := planner.options.Autonomy; given != "" && given != current {
			return false, &UsageError{fmt.Sprintf("--autonomy %s: jackstay.json's land.autonomy is %q, and jst adopt never changes it; "+
				"change it there yourself, in a pull request a person merges", given, current)}
		}
		fillLand(section)
		return false, nil
	}
	if planner.existed {
		pin, _ := getString(original.Object("adopt"), "pin")
		if pin != "" && install.Compare(strings.TrimPrefix(pin, "v"), config.Schema3Release) < 0 {
			planner.upgradeNotes(1)
			return false, nil
		}
		if schema, _ := document.Get("jackstay_schema"); fmt.Sprint(schema) != fmt.Sprint(config.Schema) && planner.workflowsPending(chosen) {
			planner.upgradeNotes(2)
			return false, nil
		}
		if schema, _ := original.Get("jackstay_schema"); fmt.Sprint(schema) != fmt.Sprint(config.Schema) {
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

// workflowsPending says whether the managed workflows on disk are not yet what the prospective
// file asks: the second step of the upgrade has not landed.
func (planner *planner) workflowsPending(chosen *profile.Profile) bool {
	probe := probeOf(planner)
	if err := probe.workflows(chosen); err != nil {
		return true
	}
	for _, change := range probe.plan.Changes {
		if strings.HasPrefix(change.ID, "workflow:") {
			return true
		}
	}
	for _, conflict := range probe.plan.Conflicts {
		if strings.HasPrefix(conflict.ID, "workflow:") {
			return true
		}
	}
	return false
}

// probeOf is a planner of the same project and prospective file that plans into a plan of its own.
func probeOf(source *planner) *planner {
	return &planner{options: source.options, root: source.root, base: source.base, plain: source.plain,
		plan: &Plan{inputs: map[string]fileInput{}}}
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
