// Package golden holds the GenAI adapter's five scenarios (docs/design/observability.md, section
// 6): a model call, a tool call, a retry, an error, a nested agent — each produced by jackstay's real
// code paths (the guard hook, `jst review` with a fake Codex printing a recorded `--json` stream,
// cli.Main) into a journal of the test's own, then checked twice: the telemetry's content against
// a golden file (ids, times and machine values normalised), and the answers of the queries over
// it. They run in the gate; moving the conventions' revision (genai.Revision) or otel changes
// the golden files only where a name changed — `go test ./internal/telemetry/golden -update`
// rewrites them, and that diff is the migration's review. The answers must not change.
package golden
