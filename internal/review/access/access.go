// Package access is the reviewer's access profile, the same for every reviewer (W-090): it reads
// everything, read-only, with the network open and the user's own PATH, proxy, CA and provider
// settings; and it is denied only what a reviewer clearly does not need — writing outside its own
// temporary folder, the account's secrets and tokens other than its provider's own login, and
// the user's MCP servers, hooks and rules (a project can name MCP servers to give back).
package access

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

// Reviewer names, as review.use spells them.
const (
	Codex  = "codex"
	Claude = "claude-code"
)

// secretFolders are the account's paths that hold keys, tokens and logins: relative to its home.
var secretFolders = []string{
	".ssh", ".aws", ".azure", ".gnupg", ".kube", ".docker/config.json", ".netrc", ".npmrc", ".pypirc",
	".config/gh", ".config/gcloud", ".config/hub", ".git-credentials", ".password-store",
	".vault-token", ".terraform.d/credentials.tfrc.json", ".cargo/credentials.toml", ".config/op",
}

// SecretPaths are the absolute paths reviewer is never allowed to read: the account's secrets, the
// system's keychain or keyring, and the other reviewer's own login and state (its OAuth and refresh
// tokens are not this reviewer's), found from environ (CODEX_HOME, CLAUDE_CONFIG_DIR) and home.
func SecretPaths(home, reviewer string, environ []string) []string {
	folders := slices.Clone(secretFolders)
	if runtime.GOOS == "darwin" {
		folders = append(folders, "Library/Keychains")
	} else {
		folders = append(folders, ".local/share/keyrings")
	}
	paths := make([]string, 0, len(folders)+3)
	for _, folder := range folders {
		paths = append(paths, filepath.Join(home, folder))
	}
	value := func(name string) string {
		for _, pair := range environ {
			if found, ok := strings.CutPrefix(pair, name+"="); ok {
				return found
			}
		}
		return ""
	}
	switch reviewer {
	case Codex:
		paths = append(paths, filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json"))
		if folder := value("CLAUDE_CONFIG_DIR"); folder != "" {
			paths = append(paths, folder)
		}
	case Claude:
		codexHome := value("CODEX_HOME")
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		paths = append(paths, codexHome)
	}
	return paths
}

// SecretFileGlobs are files a reviewer is never allowed to read wherever they are: `.env` files,
// anchored at the filesystem's root (Claude Code's `//` form), not at the reviewer's working folder.
var SecretFileGlobs = []string{"//**/.env", "//**/.env.*"}

// tokenLike are names of variables that carry a credential.
var tokenLike = regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|PASSPHRASE|API_?KEY|ACCESS_?KEY|PRIVATE_?KEY|CREDENTIAL|COOKIE|SESSION_?KEY|(^|_)AUTH($|_))`)

// TokenLike says whether a variable's name says it holds a credential.
func TokenLike(name string) bool { return tokenLike.MatchString(name) }

// internal are variables of the calling session and of jackstay that a reviewer must not inherit:
// they point it at the caller's session, sockets or git state.
func internal(name string) bool {
	for _, prefix := range []string{"JACKSTAY_", "OMC_", "GIT_", "CLAUDE", "CODEX_SANDBOX", "SANDBOX_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return slices.Contains([]string{"SSH_AUTH_SOCK", "SSH_AGENT_PID", "GPG_AGENT_INFO", "TMPDIR", "PWD", "OLDPWD"}, name)
}

// providerOwn are the variables of a reviewer's own model provider: its login and settings, which
// it is given even when they look like tokens.
func providerOwn(reviewer, name string, environ map[string]string) bool {
	switch reviewer {
	case Codex:
		return strings.HasPrefix(name, "OPENAI_") || strings.HasPrefix(name, "AZURE_OPENAI_") ||
			name == "CODEX_HOME" || name == "CODEX_API_KEY"
	case Claude:
		if strings.HasPrefix(name, "ANTHROPIC_") || slices.Contains([]string{"CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN",
			"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_SKIP_BEDROCK_AUTH",
			"CLAUDE_CODE_SKIP_VERTEX_AUTH", "CLAUDE_CODE_API_KEY_HELPER_TTL_MS"}, name) {
			return true
		}
		if environ["CLAUDE_CODE_USE_BEDROCK"] != "" && strings.HasPrefix(name, "AWS_") {
			return true
		}
		if environ["CLAUDE_CODE_USE_VERTEX"] != "" && (strings.HasPrefix(name, "VERTEX_") || name == "CLOUD_ML_REGION" ||
			name == "GOOGLE_APPLICATION_CREDENTIALS") {
			return true
		}
	}
	return false
}

// Environment is what a reviewer runs with: the caller's whole environment — PATH, locale, proxy
// (credentials in its URL included: the reviewer reaches the network through it), CA bundles and
// the rest — less the variables of the calling session and jackstay, and less every
// token-like variable that is not the reviewer's own provider's; TMPDIR is its own folder.
func Environment(environ []string, reviewer, temporary string) []string {
	values := make(map[string]string, len(environ))
	for _, pair := range environ {
		name, value, _ := strings.Cut(pair, "=")
		values[name] = value
	}
	kept := make([]string, 0, len(environ)+1)
	for _, pair := range environ {
		name, _, found := strings.Cut(pair, "=")
		if !found || name == "" {
			continue
		}
		if providerOwn(reviewer, name, values) {
			kept = append(kept, pair)
			continue
		}
		if internal(name) || TokenLike(name) {
			continue
		}
		kept = append(kept, pair)
	}
	if temporary != "" {
		kept = append(kept, "TMPDIR="+temporary)
	}
	return kept
}

// MCPServer is an MCP server a project gives back to its reviewer: `review.reviewers.<name>.mcp_servers`.
type MCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// skippedFolders hold dependencies, caches or Git's own data: no project's `.env` lives there.
var skippedFolders = []string{".git", "node_modules", "vendor", ".venv", "target", ".cache", "Library", ".build",
	".npm", ".cargo", ".rustup", "go", ".gradle", ".m2", "Pods", "DerivedData"}

// SecretFiles are the `.env` files under roots, found by walking each at most depth folders deep,
// at most limit files and visiting at most visits entries in all, past folders that hold
// dependencies or caches; each path once. A reviewer whose rules take only paths is denied each
// one. It is a snapshot: a file elsewhere, deeper, past the bounds, or made later is not in it.
func SecretFiles(roots []string, depth, limit, visits int) []string {
	var found []string
	seen := map[string]bool{}
	visited := 0
	for _, root := range roots {
		if root == "" {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			visited++
			if visited > visits || len(found) >= limit {
				return filepath.SkipAll
			}
			if err != nil {
				if entry != nil && entry.IsDir() && path != root {
					return filepath.SkipDir
				}
				return nil
			}
			relative, _ := filepath.Rel(root, path)
			if entry.IsDir() {
				if slices.Contains(skippedFolders, entry.Name()) || relative != "." && strings.Count(relative, string(filepath.Separator)) >= depth {
					return filepath.SkipDir
				}
				return nil
			}
			if name := entry.Name(); (name == ".env" || strings.HasPrefix(name, ".env.")) && !seen[path] {
				seen[path] = true
				found = append(found, path)
			}
			return nil
		})
	}
	return found
}
