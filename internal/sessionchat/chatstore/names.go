package chatstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"
)

// CommonChannel is the channel of the sessions outside a Git repository (section 2).
const CommonChannel = "common"

// Channel is a repository's channel, or #common.
type Channel struct {
	ID   string // hex SHA-256 of the Git common directory's path, device and inode; or "common"
	Name string // normalised, without '#'
}

// RepositoryChannel is the channel of the repository whose Git common directory is given. Project is
// the `project` key of its jackstay.json, "" when unset; the name then comes from the clone's folder.
func RepositoryChannel(commonDirectory, project string) (Channel, error) {
	absolute, err := filepath.Abs(commonDirectory)
	if err != nil {
		return Channel{}, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Channel{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return Channel{}, err
	}
	identity, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return Channel{}, errors.New("cannot identify the Git common directory")
	}
	id := hashOf(resolved, fmt.Sprintf("%d:%d", identity.Dev, identity.Ino))
	source := project
	if source == "" {
		source = filepath.Base(resolved)
		if source == ".git" {
			source = filepath.Base(filepath.Dir(resolved))
		}
	}
	return Channel{ID: id, Name: ChannelName(source)}, nil
}

// Common is the #common channel.
func Common() Channel { return Channel{ID: CommonChannel, Name: CommonChannel} }

var outsideChannelName = regexp.MustCompile(`[^a-z0-9._-]+`)

// ChannelName normalises a repository's name into a channel name (section 2).
func ChannelName(source string) string {
	name := outsideChannelName.ReplaceAllString(strings.ToLower(source), "-")
	name = strings.Trim(name, "-._")
	if len(name) > 48 {
		name = strings.Trim(name[:48], "-._")
	}
	switch name {
	case "":
		return "repo"
	case CommonChannel:
		return "common-repo"
	}
	return name
}

// ValidName reports whether a session name is 1 to 64 code points on one line, without '#', '['
// or ']' and without leading or trailing space (section 3.2).
func ValidName(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 64 {
		return false
	}
	if strings.ContainsAny(name, "#[]\r\n") || strings.TrimSpace(name) != name {
		return false
	}
	return true
}

// DefaultName is a session's name before it renames itself: `<repository>@<branch>`, or the base
// name of the working directory outside a repository; cut and cleaned to a valid name.
func DefaultName(channel Channel, branch, worktree string) string {
	name := filepath.Base(worktree)
	if channel.ID != CommonChannel {
		name = channel.Name
		if branch != "" {
			name += "@" + branch
		}
	}
	name = strings.Map(func(character rune) rune {
		if strings.ContainsRune("#[]\r\n", character) {
			return '-'
		}
		return character
	}, name)
	if runes := []rune(name); len(runes) > 64 {
		name = string(runes[:64])
	}
	name = strings.TrimSpace(name)
	if !ValidName(name) {
		return "session"
	}
	return name
}

// address is a parsed `to` (section 3.2).
type address struct {
	channel bool   // `#name` or `#name [ref]`
	parent  bool   // `main`
	name    string // the name before any ref; empty for `[ref]`
	ref     string // hex, 6 to 10 characters; empty when none was given
}

var (
	refSuffix      = regexp.MustCompile(`^(.*?) ?\[([0-9a-f]{6,10})\]$`)
	channelAddress = regexp.MustCompile(`^#([a-z0-9]([a-z0-9._-]{0,46}[a-z0-9])?)( \[([0-9a-f]{6,10})\])?$`)
)

// parseAddress reads an address, or says why it is not one.
func parseAddress(text string) (address, error) {
	if text == "" || utf8.RuneCountInString(text) > 300 || strings.ContainsAny(text, "\r\n") || !utf8.ValidString(text) {
		return address{}, errors.New("an address is 1 to 300 characters on one line")
	}
	if strings.HasPrefix(text, "#") {
		match := channelAddress.FindStringSubmatch(text)
		if match == nil {
			return address{}, fmt.Errorf("%q is not a channel address", text)
		}
		return address{channel: true, name: match[1], ref: match[4]}, nil
	}
	if text == "main" {
		return address{parent: true}, nil
	}
	if match := refSuffix.FindStringSubmatch(text); match != nil {
		return address{name: match[1], ref: match[2]}, nil
	}
	if !ValidName(text) {
		return address{}, fmt.Errorf("%q is not a session name", text)
	}
	return address{name: text}, nil
}

// ChannelAddress is a channel's address, with its ref when another channel shares its name.
func channelAddressOf(channel Channel, known []Channel) string {
	shared := false
	var ids []string
	for _, other := range known {
		ids = append(ids, other.ID)
		if other.ID != channel.ID && other.Name == channel.Name {
			shared = true
		}
	}
	if !shared || channel.ID == CommonChannel {
		return "#" + channel.Name
	}
	return "#" + channel.Name + " [" + shortRef(channel.ID, ids) + "]"
}
