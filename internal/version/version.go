// Package version names the jackstay version this binary belongs to: this constant is its one
// home. An install of changed code moves it, and the installed commit is tagged vX.Y.Z (W-020: one
// version names one commit).
package version

import (
	"regexp"
	"runtime/debug"
)

// Version is jackstay's version.
const Version = "0.5.11"

// Commit is the commit the binary was built from, set at build time with
// -ldflags "-X github.com/zima-oss/jackstay/internal/version.Commit=…" (install, goreleaser).
var Commit = ""

// BuildCommit is Commit, else what Go recorded in the build (CommitFromBuild), else "unknown".
func BuildCommit() string {
	if Commit != "" {
		return Commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return CommitFromBuild(info)
}

// CommitFromBuild names the commit a build recorded:
//   - `go build` in a checkout: the revision Go stamped (vcs.revision, short), "+changes" when the
//     tree had uncommitted changes (vcs.modified);
//   - `go install …/cmd/jst@vX.Y.Z`: no revision is recorded, but the module version is the tag, which
//     names one commit (W-020; tags are never moved, and Go's checksum database ties the module's
//     content to it), so the tag is the commit's name — only when it is this source's own Version;
//   - `go install …@<branch or commit>`: the pseudo-version's commit (its last 12 hex digits, short);
//
// else "unknown".
func CommitFromBuild(info *debug.BuildInfo) string {
	revision, dirty := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision != "" {
		if len(revision) > 7 {
			revision = revision[:7]
		}
		if dirty {
			revision += "+changes"
		}
		return revision
	}
	module := info.Main.Version
	if module == "v"+Version {
		return module
	}
	if hash, ok := pseudoCommit(module); ok {
		return hash[:7]
	}
	return "unknown"
}

// pseudoVersion is Go's pseudo-version form (golang.org/x/mod/module's pseudoVersionRE, without the
// +incompatible suffix): vX.0.0-yyyymmddhhmmss-abcdefabcdef, vX.Y.Z-pre.0.yyyymmddhhmmss-…, or
// vX.Y.Z-0.yyyymmddhhmmss-…; the last group is the commit.
var pseudoVersion = regexp.MustCompile(`^v[0-9]+\.(0\.0-|[0-9]+\.[0-9]+-([^+]*\.)?0\.)[0-9]{14}-([0-9a-f]{12})$`)

// pseudoCommit is the commit a pseudo-version names; a tag that only looks like one is not.
func pseudoCommit(module string) (string, bool) {
	found := pseudoVersion.FindStringSubmatch(module)
	if found == nil {
		return "", false
	}
	return found[3], true
}
