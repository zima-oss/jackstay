package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/mask"
)

// FolderName is the journal's folder under a project's (or `.all`'s) folder of the jackstay home.
const FolderName = "telemetry"

// SectionName is jackstay.json's section of these settings (section 10).
const SectionName = "telemetry"

// Settings of a project's journal.
type Settings struct {
	name      string
	folder    string
	retention journal.Retention
	commands  mask.Mode
}

// resolveProject finds the project (given, or the jackstay.json around the working folder), its
// journal folder and its `telemetry` settings; a process with no project writes under `.all`
// with the defaults. Nothing here fails: a jackstay.json that cannot be read gives the defaults.
func resolveProject(name, root string) Settings {
	settings := Settings{retention: journal.DefaultRetention, commands: mask.Masked}
	if root == "" && name == "" {
		if folder, err := os.Getwd(); err == nil {
			root, _ = config.Find(folder)
		}
	}
	var loaded config.Config
	if root != "" {
		if data, err := os.ReadFile(filepath.Join(root, config.Name)); err == nil { // #nosec G304 -- the project's jackstay.json
			loaded, _ = config.Parse(data)
		}
		if name == "" {
			name = config.ProjectName(root, loaded, root)
		}
		settings.retention, settings.commands = SectionOf(loaded.Section(SectionName))
	}
	settings.name = name
	var err error
	if name == "" {
		settings.folder, err = homedir.SharedPath(FolderName)
	} else {
		settings.folder, err = homedir.ProjectPath(name, FolderName)
	}
	if err != nil {
		settings.folder = ""
	}
	return settings
}

// SectionOf reads a `telemetry` section; what is missing or out of bounds takes the default (the
// strict check of the section is internal/config's).
func SectionOf(section map[string]any) (journal.Retention, mask.Mode) {
	retention := journal.DefaultRetention
	mode := mask.Masked
	whole := func(value any) (int, bool) {
		number, ok := value.(json.Number)
		if !ok {
			return 0, false
		}
		parsed, err := number.Int64()
		if err != nil || parsed < 1 || parsed > 3650 {
			return 0, false
		}
		return int(parsed), true
	}
	if days, ok := whole(section["keep_days"]); ok {
		retention.KeepDays = days
	}
	if days, ok := whole(section["compress_after_days"]); ok {
		retention.CompressAfterDays = days
	}
	if retention.KeepDays < retention.CompressAfterDays {
		retention = journal.DefaultRetention
	}
	if section["commands"] == string(mask.Skeleton) {
		mode = mask.Skeleton
	}
	return retention, mode
}

// reducer is the exporter's mask (section 7): each attribute by its declared kind.
func reducer(mode mask.Mode) journal.Reducer {
	return func(pair attribute.KeyValue) attribute.KeyValue {
		kind, declared := KindOf(string(pair.Key))
		if !declared {
			if testing.Testing() {
				panic("telemetry: attribute " + string(pair.Key) + " is not declared (internal/telemetry/names.go)")
			}
			return attribute.String(string(pair.Key), "<undeclared>")
		}
		switch kind {
		case Command:
			if pair.Value.Type() == attribute.STRINGSLICE {
				return attribute.StringSlice(string(pair.Key), mask.Argv(pair.Value.AsStringSlice(), mode))
			}
			return attribute.String(string(pair.Key), mask.Command(pair.Value.String(), mode))
		case FreeText:
			if pair.Value.Type() == attribute.STRINGSLICE {
				texts := pair.Value.AsStringSlice()
				for index, text := range texts {
					texts[index] = mask.Text(text)
				}
				return attribute.StringSlice(string(pair.Key), texts)
			}
			return attribute.String(string(pair.Key), mask.Text(pair.Value.String()))
		}
		return pair
	}
}
