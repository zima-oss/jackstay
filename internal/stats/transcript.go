package stats

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/zima-oss/jackstay/internal/homedir"
)

// ClaudeUsage is what a session's transcript says it used.
type ClaudeUsage struct {
	Input, Output int64
	Cost          float64
	Read          bool
}

// transcriptLine is what is read of a line of Claude Code's transcript — an undocumented format
// that changes with versions (research, section 2), so every field is optional and a line that
// does not decode is skipped. Nothing of it is copied anywhere.
type transcriptLine struct {
	Message *struct {
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			Output        int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	CostUSD      float64 `json:"costUSD"`
	TotalCostUSD float64 `json:"totalCostUSD"`
}

// ReadTranscript sums a session's usage from its transcript: the path the guard saw, else the
// session's file under ~/.claude/projects. Read on demand, never copied (W-056).
func ReadTranscript(path, session string) ClaudeUsage {
	if path == "" && session != "" {
		found, _ := filepath.Glob(filepath.Join(homedir.AccountHome(), ".claude", "projects", "*", session+".jsonl"))
		if len(found) > 0 {
			path = found[0]
		}
	}
	usage := ClaudeUsage{}
	if path == "" || strings.Contains(session, "/") {
		return usage
	}
	file, err := os.Open(path) // #nosec G304 -- the session's own transcript, named by Claude's hook input
	if err != nil {
		return usage
	}
	defer func() { _ = file.Close() }()
	usage.Read = true
	lines := bufio.NewScanner(file)
	lines.Buffer(make([]byte, 1<<20), 64<<20)
	for lines.Scan() {
		var line transcriptLine
		if json.Unmarshal(lines.Bytes(), &line) != nil {
			continue
		}
		if line.Message != nil && line.Message.Usage != nil {
			found := line.Message.Usage
			usage.Input += found.Input + found.CacheCreation + found.CacheRead
			usage.Output += found.Output
		}
		usage.Cost += line.CostUSD
		if line.TotalCostUSD > usage.Cost {
			usage.Cost = line.TotalCostUSD
		}
	}
	return usage
}
