package githubapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// Token is the token jackstay acts with and where it came from (docs/design/implementation.md, section 8.1).
type Token struct {
	Value   string
	Account string // the account it was asked for; "" inside GitHub Actions (GH_TOKEN)
	// Note is said once on standard error when not empty: a GH_TOKEN ignored outside Actions.
	Note string
}

// String never shows the token itself.
func (token Token) String() string {
	if token.Account == "" {
		return "GH_TOKEN (GitHub Actions)"
	}
	return "the token of " + token.Account + " from gh"
}

// Runner runs a command with an environment: its standard output and error and its exit status.
// err is set only when the command could not be run at all (gh missing).
type Runner func(ctx context.Context, env []string, name string, args ...string) (stdout, stderr []byte, exitCode int, err error)

// Environment is what choosing a token reads: the variables and how `gh` is run.
type Environment struct {
	Getenv  func(name string) string
	Environ func() []string
	Run     Runner
}

// OSEnvironment is the process's own environment and the real `gh`.
func OSEnvironment() Environment {
	return Environment{Getenv: os.Getenv, Environ: os.Environ, Run: runCommand}
}

func runCommand(ctx context.Context, env []string, name string, args ...string) ([]byte, []byte, int, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = env
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}
	return stdout.Bytes(), stderr.Bytes(), 0, err
}

// AccountMissingError is outside GitHub Actions with no account named (exit 2).
type AccountMissingError struct{}

func (*AccountMissingError) Error() string {
	return "set github.account in jackstay.json: the account jackstay acts as (or JACKSTAY_GITHUB_ACCOUNT for one call)"
}

// Code is the exit status the error means.
func (*AccountMissingError) Code() int { return 2 }

// ActionsTokenMissingError is GitHub Actions without GH_TOKEN (exit 2).
type ActionsTokenMissingError struct{}

func (*ActionsTokenMissingError) Error() string {
	return "GH_TOKEN is not set: inside GitHub Actions jackstay acts with the workflow's token only"
}

// Code is the exit status the error means.
func (*ActionsTokenMissingError) Code() int { return 2 }

// NotSignedInError is an account not signed in to `gh` (gh exit 1; exit 2).
type NotSignedInError struct{ Account string }

func (err *NotSignedInError) Error() string {
	return fmt.Sprintf("the GitHub account %s is not signed in to gh: run `gh auth login --hostname github.com` as it", err.Account)
}

// Code is the exit status the error means.
func (*NotSignedInError) Code() int { return 2 }

// TokenCommandError is `gh auth token` failing otherwise: gh missing, the keychain, TLS (exit 2).
type TokenCommandError struct {
	Account string
	Detail  string
}

func (err *TokenCommandError) Error() string {
	return fmt.Sprintf("`gh auth token --hostname github.com --user %s` failed (not a missing sign-in): %s", err.Account, err.Detail)
}

// Code is the exit status the error means.
func (*TokenCommandError) Code() int { return 2 }

// WrongLoginError is a token whose GET /user answers another login (exit 2).
type WrongLoginError struct{ Account, Login string }

func (err *WrongLoginError) Error() string {
	return fmt.Sprintf("the token gh keeps for %s belongs to %s: sign in again (`gh auth login --hostname github.com`) or fix github.account", err.Account, err.Login)
}

// Code is the exit status the error means.
func (*WrongLoginError) Code() int { return 2 }

// tokenVariables are the variables through which `gh` would answer with another token than the
// account's own; they are removed from its environment.
var tokenVariables = []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"}

// EffectiveAccount is JACKSTAY_GITHUB_ACCOUNT when set, else the project's github.account.
func EffectiveAccount(env Environment, configured string) string {
	if account := strings.TrimSpace(env.Getenv("JACKSTAY_GITHUB_ACCOUNT")); account != "" {
		return account
	}
	return configured
}

// InActions says whether jackstay runs inside GitHub Actions.
func InActions(env Environment) bool { return env.Getenv("GITHUB_ACTIONS") == "true" }

// ResolveToken chooses the token, one of two ways never mixed (docs/design/implementation.md section 8.1): inside GitHub Actions
// GH_TOKEN only; elsewhere the effective account's token from `gh auth token --hostname github.com
// --user <account>`, with a GH_TOKEN in the environment ignored (and said in Note). The login is
// checked against GET /user by Connect, not here.
func ResolveToken(ctx context.Context, env Environment, configuredAccount string) (Token, error) {
	if InActions(env) {
		value := env.Getenv("GH_TOKEN")
		if value == "" {
			return Token{}, &ActionsTokenMissingError{}
		}
		return Token{Value: value}, nil
	}
	account := EffectiveAccount(env, configuredAccount)
	if account == "" {
		return Token{}, &AccountMissingError{}
	}
	token := Token{Account: account}
	if env.Getenv("GH_TOKEN") != "" {
		token.Note = "GH_TOKEN is ignored outside GitHub Actions: jackstay acts as " + account + ", with gh's token for it"
	}
	var childEnv []string
	for _, entry := range env.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !contains(tokenVariables, name) {
			childEnv = append(childEnv, entry)
		}
	}
	stdout, stderr, exitCode, err := env.Run(ctx, childEnv, "gh", "auth", "token", "--hostname", "github.com", "--user", account)
	detail := strings.TrimSpace(string(stderr))
	switch {
	case err != nil:
		return Token{}, &TokenCommandError{Account: account, Detail: err.Error()}
	case exitCode == 1:
		return Token{}, &NotSignedInError{Account: account}
	case exitCode != 0:
		return Token{}, &TokenCommandError{Account: account, Detail: fmt.Sprintf("exit %d: %s", exitCode, detail)}
	}
	token.Value = strings.TrimSpace(string(stdout))
	if token.Value == "" {
		return Token{}, &TokenCommandError{Account: account, Detail: "it printed no token"}
	}
	return token, nil
}

// Connect is the client for a repository (owner/name) with the token ResolveToken chooses; outside
// GitHub Actions GET /user is asked once and a login other than the account's is refused.
func Connect(ctx context.Context, env Environment, configuredAccount, repository string, options ...Option) (*Client, Token, error) {
	token, err := ResolveToken(ctx, env, configuredAccount)
	if err != nil {
		return nil, Token{}, err
	}
	if testServer := env.Getenv(TestServerVariable); testServer != "" {
		if !isLoopback(testServer) {
			return nil, Token{}, fmt.Errorf("%s is for a test server on this machine (http://127.0.0.1:PORT/), not %q", TestServerVariable, testServer)
		}
		options = append([]Option{WithBaseURL(testServer)}, options...)
	}
	client, err := New(token.Value, repository, options...)
	if err != nil {
		return nil, Token{}, err
	}
	if token.Account == "" {
		return client, token, nil
	}
	login, err := client.Login(ctx)
	if err != nil {
		return nil, Token{}, fmt.Errorf("GET /user with the token of %s: %w", token.Account, err)
	}
	if !strings.EqualFold(login, token.Account) {
		return nil, Token{}, &WrongLoginError{Account: token.Account, Login: login}
	}
	return client, token, nil
}

func contains(list []string, wanted string) bool {
	for _, item := range list {
		if item == wanted {
			return true
		}
	}
	return false
}

// TestServerVariable names a GitHub API served on this machine (the differential check's recorded
// answers); anything but a loopback http address is refused, so a token never leaves for it.
const TestServerVariable = "JACKSTAY_GITHUB_API_URL"

func isLoopback(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" || !strings.HasSuffix(parsed.Path, "/") {
		return false
	}
	host := parsed.Hostname()
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}
