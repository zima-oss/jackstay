package doctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/zima-oss/jackstay/internal/strictjson"
)

// prepareSandbox adds one allowWrite entry while retaining all unrelated JSON values. A deny
// entry is an owner decision, never something setup removes or overrides.
func prepareSandbox(settingsPath, stateHome string) ([]byte, []byte, os.FileMode, error) {
	original, mode, err := readProfile(settingsPath)
	if err != nil {
		return nil, nil, 0, err
	}
	if len(original) == 0 {
		return nil, nil, 0, fmt.Errorf("settings file is absent or empty")
	}
	var settings map[string]json.RawMessage
	if err := strictjson.UnmarshalOpen(original, &settings); err != nil || settings == nil {
		return nil, nil, 0, fmt.Errorf("settings is not a single JSON object with unique keys")
	}
	var sandbox map[string]json.RawMessage
	if err := json.Unmarshal(settings["sandbox"], &sandbox); err != nil || sandbox == nil {
		return nil, nil, 0, fmt.Errorf("sandbox is not a JSON object")
	}
	if !bytes.Equal(bytes.TrimSpace(sandbox["enabled"]), []byte("true")) {
		return nil, nil, 0, fmt.Errorf("sandbox is not explicitly enabled")
	}
	filesystem := map[string]json.RawMessage{}
	if raw, exists := sandbox["filesystem"]; exists {
		if err := json.Unmarshal(raw, &filesystem); err != nil || filesystem == nil {
			return nil, nil, 0, fmt.Errorf("sandbox.filesystem is not a JSON object")
		}
	}
	if raw, exists := filesystem["denyWrite"]; exists {
		var denied []any
		if err := json.Unmarshal(raw, &denied); err != nil || denied == nil {
			return nil, nil, 0, fmt.Errorf("sandbox.filesystem.denyWrite is not a list")
		}
		for _, entry := range denied {
			if _, isText := entry.(string); !isText {
				return nil, nil, 0, fmt.Errorf("sandbox.filesystem.denyWrite has a non-string entry")
			}
		}
		real := realPath(stateHome)
		if covering(denied, real) != "" || inside(denied, real) != "" {
			return nil, nil, 0, fmt.Errorf("denyWrite covers jackstay state; review that entry yourself")
		}
	}
	allowed := []string{}
	if raw, exists := filesystem["allowWrite"]; exists {
		if err := json.Unmarshal(raw, &allowed); err != nil || allowed == nil {
			return nil, nil, 0, fmt.Errorf("sandbox.filesystem.allowWrite is not a list of strings")
		}
	}
	if covering(stringsToAny(allowed), realPath(stateHome)) != "" {
		return nil, nil, 0, fmt.Errorf("allowWrite already covers jackstay state; inspect the doctor finding")
	}
	allowed = append(allowed, stateHome)
	filesystem["allowWrite"], err = json.Marshal(allowed)
	if err != nil {
		return nil, nil, 0, err
	}
	sandbox["filesystem"], err = json.Marshal(filesystem)
	if err != nil {
		return nil, nil, 0, err
	}
	settings["sandbox"], err = json.Marshal(sandbox)
	if err != nil {
		return nil, nil, 0, err
	}
	replacement, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, nil, 0, err
	}
	return original, append(replacement, '\n'), mode, nil
}

func stringsToAny(entries []string) []any {
	values := make([]any, len(entries))
	for index, entry := range entries {
		values[index] = entry
	}
	return values
}

// Only the changed JSON path is printed, so unrelated settings and possible private values do
// not get copied into terminal logs or the JSON plan.
func settingsDiff(path string, _ []byte, replacement []byte) string {
	var settings struct {
		Sandbox struct {
			Filesystem struct {
				AllowWrite []string `json:"allowWrite"`
			} `json:"filesystem"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal(replacement, &settings); err != nil || len(settings.Sandbox.Filesystem.AllowWrite) == 0 {
		return ""
	}
	added := settings.Sandbox.Filesystem.AllowWrite[len(settings.Sandbox.Filesystem.AllowWrite)-1]
	encoded, _ := json.Marshal(added)
	return fmt.Sprintf("  --- %s\n  +++ %s\n  @@ sandbox.filesystem.allowWrite @@\n  +%s\n", path, path, strings.TrimSpace(string(encoded)))
}
