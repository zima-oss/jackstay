package scopes

import (
	"strings"

	"github.com/zima-oss/jackstay/internal/cleancopy"
	"github.com/zima-oss/jackstay/internal/config"
)

// makeCopy makes the clean copy of the commit chosen.commit names (internal/cleancopy), and the
// working folder inside it that matches the one jackstay was called in.
func (base *tester) makeCopy(chosen options) (*cleancopy.Copy, string, error) {
	if strings.HasPrefix(chosen.commit, "-") {
		return nil, "", refused("--commit takes a commit: '" + chosen.commit + "'")
	}
	name := chosen.project
	if name == "" {
		// Only the live identity matters here. The scope and the rest of the settings
		// are validated after the requested commit has been checked out.
		if root, loaded, err := config.Load(base.here, []string{}); err == nil && root != "" {
			name = config.ProjectName(root, loaded, base.here)
		}
	}
	made, inside, err := cleancopy.Make(cleancopy.Request{Here: base.here, Commit: chosen.commit, Project: name,
		Now: base.clock.Now(), Git: base.git})
	if err != nil {
		return nil, "", refused("--commit " + chosen.commit + ": " + err.Error())
	}
	return made, inside, nil
}
