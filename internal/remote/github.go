package remote

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// githubHost is the one host jackstay talks to; GitHub Enterprise hosts are not supported.
const githubHost = "github.com"

// ownerText is a GitHub user or organisation name; nameText a repository's name.
var (
	ownerText = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	nameText  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// Repository is a GitHub repository.
type Repository struct {
	Owner string
	Name  string
	// User is the user an https URL names (`https://user@github.com/…`), for the advice that git's
	// credentials may not be the project's account; "" otherwise. An SSH URL's `git@` is not a
	// login and is never returned.
	User string
}

// String is `owner/name`.
func (repository Repository) String() string { return repository.Owner + "/" + repository.Name }

// OtherHostError is a remote URL that does not name github.com (another host, or a local path).
type OtherHostError struct {
	Host string
}

func (other *OtherHostError) Error() string {
	if other.Host == "" {
		return "the remote URL names no host (a local path?): jackstay talks to github.com only; set github.repository in jackstay.json"
	}
	return fmt.Sprintf("the remote is on %q: jackstay talks to github.com only", other.Host)
}

// BadURLError is a github.com URL, or a `github.repository`, that does not name one repository.
type BadURLError struct {
	Reason string
}

func (bad *BadURLError) Error() string {
	return "the remote URL or github.repository does not name a GitHub repository: " + bad.Reason
}

// ParseURL is the repository a remote URL names: `https://github.com/o/r(.git)`,
// `https://user[:secret]@github.com/o/r`, `git@github.com:o/r.git`, `ssh://git@github.com[:22]/o/r`.
// *OtherHostError for another host or a local path, *BadURLError for a github.com URL that does not
// parse.
func ParseURL(raw string) (Repository, error) {
	if strings.Contains(raw, "://") {
		return parseWithScheme(raw)
	}
	// git's scp form: `[user@]host:path`, with no slash before the colon.
	before, path, found := strings.Cut(raw, ":")
	if !found || strings.Contains(before, "/") {
		return Repository{}, &OtherHostError{}
	}
	host := before
	if at := strings.LastIndex(before, "@"); at >= 0 {
		host = before[at+1:]
	}
	if !strings.EqualFold(host, githubHost) {
		return Repository{}, &OtherHostError{Host: host}
	}
	return parsePath(strings.TrimPrefix(path, "/"), "")
}

func parseWithScheme(raw string) (Repository, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		if strings.Contains(strings.ToLower(raw), githubHost) {
			return Repository{}, &BadURLError{Reason: "invalid URL syntax"}
		}
		return Repository{}, &OtherHostError{}
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, githubHost) {
		return Repository{}, &OtherHostError{Host: host}
	}
	scheme := strings.ToLower(parsed.Scheme)
	port := parsed.Port()
	user := ""
	switch {
	case scheme == "https" && (port == "" || port == "443"):
		if parsed.User != nil {
			user = parsed.User.Username()
		}
	case scheme == "ssh" && (port == "" || port == "22"):
	default:
		return Repository{}, &BadURLError{Reason: "github.com is reached over https or ssh on their own ports"}
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return Repository{}, &BadURLError{Reason: "a query or a fragment"}
	}
	return parsePath(strings.TrimPrefix(parsed.Path, "/"), user)
}

// parsePath reads `owner/name[.git][/]`.
func parsePath(path, user string) (Repository, error) {
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	owner, name, found := strings.Cut(path, "/")
	switch {
	case !found:
		return Repository{}, &BadURLError{Reason: "the path is not owner/name"}
	case strings.Contains(name, "/"):
		return Repository{}, &BadURLError{Reason: "the path has more than owner/name"}
	}
	repository, err := checked(owner, name)
	if err != nil {
		return Repository{}, &BadURLError{Reason: err.Error()}
	}
	repository.User = user
	return repository, nil
}

// ParseRepository reads `owner/name` (jackstay.json's `github.repository`).
func ParseRepository(text string) (Repository, error) {
	owner, name, found := strings.Cut(text, "/")
	if !found || strings.Contains(name, "/") {
		return Repository{}, &BadURLError{Reason: "github.repository is written owner/name"}
	}
	repository, err := checked(owner, name)
	if err != nil {
		return Repository{}, &BadURLError{Reason: err.Error()}
	}
	return repository, nil
}

func checked(owner, name string) (Repository, error) {
	if !ownerText.MatchString(owner) {
		return Repository{}, fmt.Errorf("invalid GitHub owner (use letters, digits and hyphens, at most 39 characters)")
	}
	if !nameText.MatchString(name) || name == "." || name == ".." {
		return Repository{}, fmt.Errorf("invalid GitHub repository name")
	}
	return Repository{Owner: owner, Name: name}, nil
}

// URL is the fetch URL of a remote, as git uses it (`insteadOf` rewrites applied).
func URL(ctx context.Context, git Runner, remote string) (string, error) {
	// The URL as configured: `git remote get-url` applies `url.*.insteadOf`, which says where git
	// fetches from, not which repository on GitHub this is.
	out, err := git.Run(ctx, "config", "--get", "remote."+remote+".url")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RepositoryOf is the project's GitHub repository: `github.repository` when the section sets it,
// else what the URL of the remote (Name's rule) names. github is the `github` section, nil when
// there is none.
func RepositoryOf(ctx context.Context, git Runner, github map[string]any) (Repository, error) {
	if text, _ := github["repository"].(string); text != "" {
		return ParseRepository(text)
	}
	name, err := Name(ctx, git, ConfiguredRemote(github))
	if err != nil {
		return Repository{}, err
	}
	address, err := URL(ctx, git, name)
	if err != nil {
		return Repository{}, err
	}
	return ParseURL(address)
}
