package access

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTokenLikeNames(t *testing.T) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "AWS_ACCESS_KEY_ID", "NPM_AUTH", "DB_PASSWORD",
		"STRIPE_API_KEY", "OPENAI_APIKEY", "CLOUDSDK_PROXY_PASSWORD", "SESSION_KEY", "SSH_PRIVATE_KEY", "AUTH_HEADER", "MY_CREDENTIALS"} {
		if !TokenLike(name) {
			t.Errorf("%s is not taken for a token", name)
		}
	}
	for _, name := range []string{"PATH", "HOME", "HTTPS_PROXY", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "LANG", "GOPATH", "AUTHOR", "KEYBOARD"} {
		if TokenLike(name) {
			t.Errorf("%s is taken for a token", name)
		}
	}
}

func TestEnvironmentKeepsWhatAReviewerNeedsAndDropsTheRest(t *testing.T) {
	caller := []string{"PATH=/opt/tools:/usr/bin", "HOME=/home/r", "LANG=C.UTF-8", "HTTPS_PROXY=http://user:secret@proxy:3128",
		"NO_PROXY=localhost", "SSL_CERT_FILE=/ca.pem", "NODE_EXTRA_CA_CERTS=/ca.pem", "GH_TOKEN=t", "AWS_SECRET_ACCESS_KEY=s",
		"OPENAI_API_KEY=o", "ANTHROPIC_API_KEY=a", "JACKSTAY_CHAT_SESSION_TOKEN=j", "GIT_DIR=/g", "CLAUDE_CODE_MESSAGING_SOCKET=/m",
		"SSH_AUTH_SOCK=/agent", "TMPDIR=/caller", "OMC_STATE=x", "SANDBOX_RUNTIME=1"}
	codex := Environment(caller, Codex, "/review/tmp")
	for _, kept := range []string{"PATH=/opt/tools:/usr/bin", "HOME=/home/r", "LANG=C.UTF-8", "HTTPS_PROXY=http://user:secret@proxy:3128",
		"NO_PROXY=localhost", "SSL_CERT_FILE=/ca.pem", "NODE_EXTRA_CA_CERTS=/ca.pem", "OPENAI_API_KEY=o", "TMPDIR=/review/tmp"} {
		if !slices.Contains(codex, kept) {
			t.Errorf("%s did not reach Codex: %v", kept, codex)
		}
	}
	for _, gone := range []string{"GH_TOKEN", "AWS_SECRET", "ANTHROPIC_API_KEY", "JACKSTAY_", "GIT_DIR", "CLAUDE_CODE", "SSH_AUTH_SOCK",
		"TMPDIR=/caller", "OMC_", "SANDBOX_"} {
		if slices.ContainsFunc(codex, func(pair string) bool { return strings.HasPrefix(pair, gone) }) {
			t.Errorf("%s reached Codex: %v", gone, codex)
		}
	}
	claude := Environment(caller, Claude, "")
	if !slices.Contains(claude, "ANTHROPIC_API_KEY=a") || slices.Contains(claude, "OPENAI_API_KEY=o") {
		t.Errorf("Claude Code gets its own provider's key and not another's: %v", claude)
	}
	bedrock := Environment([]string{"CLAUDE_CODE_USE_BEDROCK=1", "AWS_SECRET_ACCESS_KEY=s", "AWS_REGION=eu"}, Claude, "")
	if !slices.Contains(bedrock, "AWS_SECRET_ACCESS_KEY=s") || !slices.Contains(bedrock, "CLAUDE_CODE_USE_BEDROCK=1") {
		t.Errorf("Bedrock is Claude Code's provider here, so its credentials are its own: %v", bedrock)
	}
}

func TestSecretsAreTheAccountsKeysAndTheEnvFiles(t *testing.T) {
	paths := SecretPaths("/home/r", Codex, []string{"CLAUDE_CONFIG_DIR=/claude/config"})
	for _, want := range []string{"/home/r/.ssh", "/home/r/.aws", "/home/r/.config/gh", "/home/r/.vault-token",
		"/home/r/.claude", "/home/r/.claude.json", "/claude/config"} {
		if !slices.Contains(paths, want) {
			t.Errorf("%s is not a secret to Codex: %v", want, paths)
		}
	}
	if claude := SecretPaths("/home/r", Claude, nil); !slices.Contains(claude, "/home/r/.codex") || slices.Contains(claude, "/home/r/.claude") {
		t.Errorf("Claude Code's secrets: %v", claude)
	}
	root := t.TempDir()
	for _, file := range []string{".env", "service/.env.production", "deep/a/b/c/d/.env", ".git/.env", "node_modules/x/.env", "environment.go", ".envrc"} {
		path := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	found := SecretFiles([]string{root, ""}, 3, 100)
	want := []string{filepath.Join(root, ".env"), filepath.Join(root, "service/.env.production")}
	if !slices.Equal(found, want) {
		t.Fatalf("found %v, want %v (not past the depth, not in .git, not .envrc)", found, want)
	}
	if limited := SecretFiles([]string{root}, 3, 1); len(limited) != 1 {
		t.Fatalf("the limit: %v", limited)
	}
}
