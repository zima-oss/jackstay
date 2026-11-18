package release

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/zima-oss/jackstay/internal/changes"
)

// Version is MAJOR.MINOR.PATCH, three decimal integers without leading zeros.
type Version struct {
	Major, Minor, Patch int
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)

// ParseVersion reads a version; anything but MAJOR.MINOR.PATCH is an error.
func ParseVersion(text string) (Version, error) {
	parts := versionPattern.FindStringSubmatch(text)
	if parts == nil {
		return Version{}, fmt.Errorf("%q is not a version (MAJOR.MINOR.PATCH, no leading zeros, no suffix)", text)
	}
	major, _ := strconv.Atoi(parts[1])
	minor, _ := strconv.Atoi(parts[2])
	patch, _ := strconv.Atoi(parts[3])
	return Version{Major: major, Minor: minor, Patch: patch}, nil
}

func (version Version) String() string {
	return fmt.Sprintf("%d.%d.%d", version.Major, version.Minor, version.Patch)
}

// Less says whether version comes before other.
func (version Version) Less(other Version) bool {
	if version.Major != other.Major {
		return version.Major < other.Major
	}
	if version.Minor != other.Minor {
		return version.Minor < other.Minor
	}
	return version.Patch < other.Patch
}

// bumpRank orders bumps: none < patch < minor < major.
var bumpRank = map[string]int{"": 0, "patch": 1, "minor": 2, "major": 3}

// HighestBump is the highest bump among entries ("" when none bumps).
func HighestBump(entries []changes.Entry) string {
	highest := ""
	for _, entry := range entries {
		if bumpRank[entry.Bump()] > bumpRank[highest] {
			highest = entry.Bump()
		}
	}
	return highest
}

// Next is the version after last for a bump. Before 1.0 a major bump is a minor one, so nothing
// computed reaches 1.0.0 from 0.x (docs/design/release.md, section 3).
func (version Version) Next(bump string) Version {
	if bump == "major" && version.Major == 0 {
		bump = "minor"
	}
	switch bump {
	case "major":
		return Version{Major: version.Major + 1}
	case "minor":
		return Version{Major: version.Major, Minor: version.Minor + 1}
	case "patch":
		return Version{Major: version.Major, Minor: version.Minor, Patch: version.Patch + 1}
	}
	return version
}
