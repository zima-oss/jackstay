package chatstore

import (
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The checks a send makes beyond its tool's schema (sections 4.4, 4.6, 4.7 and 5.2). The store
// makes every check itself, so no caller can store what the schema would refuse.

// Defaults and ranges of a send.
const (
	DefaultExpirySeconds = 259200
	MinExpirySeconds     = 60
	MaxExpirySeconds     = 604800
	PriorityNow          = "now"
	PriorityNext         = "next"
	PriorityLater        = "later"
)

// RequestInput is a send's `request` (section 4.4).
type RequestInput struct {
	Expects string
	Options []string
}

var (
	metaKey        = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	revisionText   = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	linesText      = regexp.MustCompile(`^[1-9][0-9]{0,6}(-[1-9][0-9]{0,6})?$`)
	artifactURI    = regexp.MustCompile(`^jst:(review/[^/\s]+/[^/\s]+|test/[^/\s]+|brief/\S+)$`)
	idempotencyKey = regexp.MustCompile(`^[\x21-\x7e]{8,128}$`)
)

func oneLine(text string, most int) bool {
	return utf8.ValidString(text) && utf8.RuneCountInString(text) <= most && !strings.ContainsAny(text, "\r\n")
}

func checkBody(message string) error {
	if len(message) > MaxBodyBytes {
		return refuse(ReasonTooLarge, "a message is at most %d bytes; send the rest as an attachment", MaxBodyBytes)
	}
	if !utf8.ValidString(message) {
		return refuse(ReasonInvalid, "the message is not valid UTF-8")
	}
	return nil
}

func checkSummary(summary string) error {
	if !oneLine(summary, 200) {
		return refuse(ReasonInvalid, "a summary is at most 200 characters on one line")
	}
	return nil
}

// summaryOf is the summary given, or the first line of the message cut to 200 characters.
func summaryOf(summary, message string) string {
	if summary != "" {
		return summary
	}
	line, _, _ := strings.Cut(message, "\n")
	line = strings.TrimSuffix(line, "\r")
	if runes := []rune(line); len(runes) > 200 {
		line = string(runes[:200])
	}
	return line
}

func checkPriority(priority string) (string, error) {
	switch priority {
	case "":
		return PriorityNext, nil
	case PriorityNow, PriorityNext, PriorityLater:
		return priority, nil
	}
	return "", refuse(ReasonInvalid, "priority is now, next or later")
}

func checkExpiry(seconds int) (int, error) {
	if seconds == 0 {
		return DefaultExpirySeconds, nil
	}
	if seconds < MinExpirySeconds || seconds > MaxExpirySeconds {
		return 0, refuse(ReasonInvalid, "expires_in_seconds is %d to %d", MinExpirySeconds, MaxExpirySeconds)
	}
	return seconds, nil
}

func checkIdempotencyKey(key string) error {
	if key != "" && !idempotencyKey.MatchString(key) {
		return refuse(ReasonInvalid, "an idempotency_key is 8 to 128 printable ASCII characters without spaces")
	}
	return nil
}

func checkMeta(meta map[string]string) error {
	if len(meta) > 16 {
		return refuse(ReasonInvalid, "meta holds at most 16 keys")
	}
	for key, value := range meta {
		if !metaKey.MatchString(key) {
			return refuse(ReasonInvalid, "meta key %q is not 1 to 32 letters, digits or _", key)
		}
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 256 {
			return refuse(ReasonInvalid, "meta %q is at most 256 characters", key)
		}
	}
	return nil
}

// checkAttachments applies the kind rules of section 4.7; a breach names the attachment's index.
func checkAttachments(attachments []Attachment) error {
	if len(attachments) > 8 {
		return refuse(ReasonInvalid, "a message carries at most 8 attachments")
	}
	for index, attachment := range attachments {
		if problem := attachmentProblem(attachment); problem != "" {
			return refuse(ReasonInvalid, "attachment %d: %s", index, problem)
		}
	}
	return nil
}

func attachmentProblem(attachment Attachment) string {
	for _, field := range []string{attachment.Repository, attachment.Path, attachment.Revision, attachment.Lines, attachment.URI} {
		if !utf8.ValidString(field) || len(field) > 512 {
			return "a field is past 512 characters"
		}
	}
	if !oneLine(attachment.Title, 200) {
		return "title is at most 200 characters on one line"
	}
	if attachment.Repository != "" && !channelAddress.MatchString(attachment.Repository) {
		return "repository is a channel address"
	}
	if attachment.Revision != "" && !revisionText.MatchString(attachment.Revision) {
		return "revision is 7 to 64 lower-case hex characters"
	}
	if attachment.Lines != "" && !linesText.MatchString(attachment.Lines) {
		return "lines is N or N-M"
	}
	switch attachment.Kind {
	case "file":
		if attachment.Path == "" || attachment.URI != "" {
			return "a file has a path and no uri"
		}
		if strings.HasPrefix(attachment.Path, "/") || strings.ContainsAny(attachment.Path, "\x00\r\n") {
			return "path is relative to the repository"
		}
		for _, part := range strings.Split(path.Clean(attachment.Path), "/") {
			if part == ".." {
				return "path may not leave the repository"
			}
		}
		for _, part := range strings.Split(attachment.Path, "/") {
			if part == ".." {
				return "path may not leave the repository"
			}
		}
	case "commit":
		if attachment.Revision == "" || attachment.Path != "" || attachment.Lines != "" || attachment.URI != "" {
			return "a commit has a revision, and no path, lines or uri"
		}
	case "artifact":
		if !artifactURI.MatchString(attachment.URI) || attachment.Path != "" || attachment.Revision != "" ||
			attachment.Lines != "" || attachment.Repository != "" {
			return "an artifact is a jst:review/<task>/<round>, jst:test/<run> or jst:brief/<worktree> uri"
		}
	case "url":
		if !strings.HasPrefix(attachment.URI, "https://") || strings.ContainsAny(attachment.URI, " \r\n") ||
			attachment.Path != "" || attachment.Revision != "" || attachment.Lines != "" || attachment.Repository != "" {
			return "a url is an https: uri"
		}
	default:
		return "kind is file, commit, artifact or url"
	}
	return ""
}

func checkRequest(request *RequestInput) error {
	if request == nil {
		return nil
	}
	switch request.Expects {
	case "decision":
		if len(request.Options) > 0 {
			return refuse(ReasonInvalid, "options go only with expects: answer")
		}
	case "answer":
		if len(request.Options) == 1 || len(request.Options) > 8 {
			return refuse(ReasonInvalid, "a request has 2 to 8 options, or none")
		}
		seen := map[string]bool{}
		for _, option := range request.Options {
			if option == "" || !oneLine(option, 64) || seen[option] {
				return refuse(ReasonInvalid, "options are distinct, 1 to 64 characters on one line")
			}
			seen[option] = true
		}
	default:
		return refuse(ReasonInvalid, "expects is decision or answer")
	}
	return nil
}
