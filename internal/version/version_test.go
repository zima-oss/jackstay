package version

import (
	"runtime/debug"
	"testing"
)

func TestCommitFromBuild(t *testing.T) {
	setting := func(pairs ...string) []debug.BuildSetting {
		var settings []debug.BuildSetting
		for index := 0; index+1 < len(pairs); index += 2 {
			settings = append(settings, debug.BuildSetting{Key: pairs[index], Value: pairs[index+1]})
		}
		return settings
	}
	for name, tc := range map[string]struct {
		module   string
		settings []debug.BuildSetting
		want     string
	}{
		"a checkout":                    {"(devel)", setting("vcs.revision", "0123456789abcdef0123456789abcdef01234567", "vcs.modified", "false"), "0123456"},
		"a checkout with changes":       {"v0.5.7+dirty", setting("vcs.revision", "0123456789abcdef", "vcs.modified", "true"), "0123456+changes"},
		"go install at this version":    {"v" + Version, nil, "v" + Version},
		"go install at another tag":     {"v0.4.0", nil, "unknown"},
		"go install at a commit":        {"v0.5.8-0.20261118101500-abcdef012345", nil, "abcdef0"},
		"go install at a pre-release's": {"v0.5.8-rc.1.0.20261118101500-abcdef012345", nil, "abcdef0"},
		"a pseudo-version, modified":    {"v0.5.8-0.20261118101500-abcdef012345+dirty", nil, "unknown"},
		"a pre-release tag":             {"v0.5.8-rc.1", nil, "unknown"},
		"a tag shaped like one":         {"v0.5.8-preview-20261118101500-abcdef012345", nil, "unknown"},
		"no tag before it":              {"v0.0.0-20261118101500-abcdef012345", nil, "abcdef0"},
		"nothing recorded":              {"(devel)", nil, "unknown"},
	} {
		info := &debug.BuildInfo{Main: debug.Module{Path: "github.com/zima-oss/jackstay", Version: tc.module}, Settings: tc.settings}
		if got := CommitFromBuild(info); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
