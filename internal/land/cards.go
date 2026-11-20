package land

import (
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/hosting"
)

// jst land writes a Card line; it does not judge one. The Card line and the tree's rules are
// judged by `jst lint pr` in CI (the workflow adopt writes), on the merge with the base, as a
// required check: CI is the gate (W-092), and a description edited later runs that check again,
// which the recheck reads.

// cardLine is the `Card:` line --card asks for ("" without one).
func cardLine(ids []string, reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if len(ids) == 0 {
		if reason != "" {
			return "", failf(exitInvalid, "--reason goes with --card none")
		}
		return "", nil
	}
	if slices.Contains(ids, "none") {
		if len(ids) > 1 || reason == "" {
			return "", failf(exitInvalid, "--card none stands alone and needs --reason TEXT")
		}
		return "Card: none (" + reason + ")", nil
	}
	if reason != "" {
		return "", failf(exitInvalid, "--reason goes with --card none")
	}
	var unique []string
	for _, id := range ids {
		if !slices.Contains(unique, id) {
			unique = append(unique, id)
		}
	}
	return "Card: " + strings.Join(unique, ", "), nil
}

// withLine is body with the line added as its last paragraph.
func withLine(body, line string) string {
	if line == "" {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return line + "\n"
	}
	return strings.TrimRight(body, "\n") + "\n\n" + line + "\n"
}

// cardsInDescription says whether a jackstay.json (as bytes) puts the Card line in the change
// request's description (`cards.unit` `pr`, the default).
func cardsInDescription(data []byte) bool {
	loaded, err := config.Parse(data)
	if err != nil {
		return false
	}
	section := loaded.Section("cards")
	if section == nil {
		return false
	}
	unit, _ := section["unit"].(string)
	return unit == "" || unit == "pr"
}

// cardsNeedAMergeCommit refuses `land.method: rebase` where the base's file puts the Card line in
// the description: a rebase writes no merge commit, so the line would not land.
func cardsNeedAMergeCommit(method hosting.Method, inDescription bool) error {
	if method == hosting.Rebase && inDescription {
		return failf(exitInvalid, "`land.method` is rebase, and the base's Card line is in the change request's "+
			"description, which a rebase does not land: use squash or merge")
	}
	return nil
}
