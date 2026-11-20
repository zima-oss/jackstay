# Community signals and possible next capabilities

Research snapshot, 19 November 2026. This is a product proposal for jackstay maintainers. The
signals below are first-person issue reports in adjacent coding-agent projects and current upstream
product documentation. They show failure modes and requests. They do not establish demand among
jackstay users or measure the incidence of a failure across installations.

## Evidence and limits

| Signal | Source and observation | Implication for jackstay | Limit |
|---|---|---|---|
| Concurrent sessions affect Git state | A Claude Code report opened 13 August 2026 describes three wrong-attribution commits in a shared clone and an advisory claims experiment. Another report opened 31 August 2026 reproduces stale-index data loss when HEAD moves without index refresh. [Shared-clone field report](https://github.com/anthropics/claude-code/issues/88862) · [Stale-index report](https://github.com/anthropics/claude-code/issues/90943) | Add evidence about worktree occupancy and staged paths before jackstay commits or changes refs. | The field report covers one team. The stale-index report includes Windows-specific reproduction paths. Neither is a jackstay incident. |
| Worktree allocation can race live sessions | A Claude Code issue opened 1 August 2026 reports a pooled worktree being reassigned while another session used it. A Codex issue opened 29 March 2026 discusses how shared branches make concurrent edits expected. [Worktree reuse report](https://github.com/anthropics/claude-code/issues/83180) · [Codex shared-branch report](https://github.com/openai/codex/issues/16195) | Observe ownership and revision changes at action time, and distinguish a compatible independent edit from a collision. | Product-specific reports. The proposed jackstay behavior needs its own reproducible fixture. |
| Hook coverage is sometimes unknown | A Claude Code report opened 21 August 2026 describes absent user hook execution in subagents. A separate Windows report later corrected its diagnosis to an enterprise policy allowing only managed hooks. A Codex issue documents fail-open behavior after a hook execution error. [Subagent report](https://github.com/anthropics/claude-code/issues/88441) · [Managed-policy correction](https://github.com/anthropics/claude-code/issues/88896) · [Codex hook diagnosis](https://github.com/openai/codex/issues/24453) | Report demonstrated coverage per installed runner, hook event and invocation route. Avoid inferring protection from the presence of a settings file. | The upstream issues have evolving diagnoses and statuses. A jackstay probe establishes only the paths it actually exercises. |
| Command environments can differ by execution route | A Codex discussion opened 7 June 2026 groups discrepancies between shell startup, app server, setup scripts and worktree execution. [Codex environment discussion](https://github.com/openai/codex/discussions/26901) | Give test evidence an inspectable provenance for effective directory, executable and selected environment inputs. | Discussion is a request for a clearer contract. It does not prove a failure in jackstay's current runner. |
| Usage attribution is requested for autonomous work | A Claude Code issue opened 4 November 2025 asks for token and cost fields in hook payloads. Another asks for session totals and child lineage because transcript reads can race the final write. [Usage-in-hooks request](https://github.com/anthropics/claude-code/issues/11008) · [Stop-hook request](https://github.com/anthropics/claude-code/issues/80446) | Show the origin, completeness and unit of a token observation before using it for budgets. | Token observations are not provider invoices. Hook schemas and availability can change. |

The public jackstay issue and discussion pages could not be inspected reliably in this research
session. The proposals therefore begin with user validation and local fixtures. No row above is
presented as a verified request from a jackstay adopter.

## Priorities

These priorities combine potential data loss, fit with jackstay's existing local command model,
and the amount of upstream evidence. They are a sequencing proposal, subject to maintainer and
adopter feedback.

| Priority | Initiative | Existing foundation | Earliest useful outcome |
|---|---|---|---|
| 1 | Live worktree occupancy and commit attribution | `jst status`, `jst start`, `jst tidy`, local delivery records | A user can identify another active session touching a planned Git operation before it runs. |
| 1 | Runner coverage diagnostics | `jst doctor`, `jst guard`, generated adoption settings | A user can see which tested hook route refused a harmless probe, and which routes remain unknown. |
| 2 | Test environment provenance | Test receipts, clean-copy runs and the multi-runner evidence plan | A test record explains which executable and effective inputs produced its verdict. |
| 2 | Usage provenance and budget reports | Review token counters, `jst status` events, local journal | A local report separates measured tokens from unavailable cost data and identifies coverage gaps. |

### Live worktree occupancy and commit attribution

A new activity view in `jst status` would join worktree identity, Git index state and observed
local sessions. A later preflight shared by `jst gh pr-land`, `jst release` and other Git-writing
commands would snapshot HEAD, the index tree and intended path set, then revalidate those facts
immediately before the write. The report would use stable identity and timestamps, with explicit
`unknown` when a runner did not publish a liveness signal. It would show a path as contested only
when two observed live sessions claim overlapping work, leaving the operator a clear explanation
and preserving the current command's project ownership checks.

The first slice is read-only activity and a deterministic warning when the intended index contains
changes outside the operation's declared path set. The warning cannot infer authorship from Git
alone. A later opt-in per-session claim file can provide ownership hints, with one file per session
and expiry tied to a witnessed process identity. The claim remains advisory until all supported
runners can report coverage reliably. Integration should cover two worktrees, two independent
clones with the same project name, concurrent staging, HEAD movement during preflight, an expired
claim and a runner with no observed session. Success means the operation refuses a changed HEAD or
index under its existing error policy and a user can inspect a report that names the conflicting
paths without exposing file contents.

Dependencies are the A1 repository identity, A2 local operation preconditions, and a decision on
whether runner session observations can prove liveness. This initiative is high priority because
it targets concrete integrity failures, but its cross-runner effect remains a hypothesis until a
jackstay adopter reproduces a collision. [Shared-clone field report](https://github.com/anthropics/claude-code/issues/88862) · [Stale-index report](https://github.com/anthropics/claude-code/issues/90943)

### Runner coverage diagnostics

Extend `jst doctor` with an opt-in diagnostic matrix for each installed runner and hook route. A
probe executes a benign command through the actual runner entry point and checks for jackstay's
expected refusal or observation, recording runner version, hook source, policy visibility and the
specific route exercised. Each cell has `observed`, `failed` or `unknown` and a timestamp. The
first slice can test Claude Code's normal interactive Bash hook and Codex's supported shell hook
in disposable projects. Further routes, such as subagents and nested code-mode calls, need
version-specific fixtures and a way to run without assuming provider credentials.

No automated probe should create external work or require a real review model. Tests should stub
the runner boundary and exercise policy suppression, missing hook executables, stale settings,
subagent payload differences and a denied benign command. A manual compatibility matrix with
precise versions complements local probes. A health report can claim coverage only for a route
actually exercised. Dependencies are adapter version detection and `jst doctor`'s existing
platform checks. An adopter study should test whether coverage reports reduce surprise after
runner upgrades. [Subagent report](https://github.com/anthropics/claude-code/issues/88441) · [Managed-policy correction](https://github.com/anthropics/claude-code/issues/88896) · [Codex hook diagnosis](https://github.com/openai/codex/issues/24453)

### Test environment provenance

Add a versioned, privacy-filtered execution fingerprint to the proposed common test evidence
record. Capture the resolved executable, version probe result, effective working directory as a
repository-relative path, selected environment variable names and hashes of their values, and the
source revision. The data records whether each field was measured, declared or unavailable.
Receipt reuse compares semantic inputs and still requires a counted pass. A local `jst test
explain RUN` view would show which input changed between a prior pass and the next execution.

Start after TST-2 and the receipt migration design in TST-6. Verify two PATHs that resolve the
same command name to different binaries, two effective directories, a changed selected variable,
a redacted secret-shaped value, and an unavailable version probe. The record must reveal no raw
environment values or absolute home paths. Capture should preserve the existing timeout and
process supervision contract. This is a likely need inferred from Codex's environment discussion
and jackstay's own test receipt model. [Codex environment discussion](https://github.com/openai/codex/discussions/26901)

### Usage provenance and budget reports

Extend the local timeline with a measured-usage schema keyed by provider, session and attempt.
Record token category, unit, capture time, source and completeness. A provider's usage fields are
accepted only through its documented result envelope. A local report can show observed token
sums and missing sessions, with optional user-supplied price tables as explicitly estimated
amounts. An alert can compare a configured token ceiling with measured values at a supported
checkpoint. It should never convert an absent observation into zero usage.

Map accepted input, output and cache categories to the current OpenTelemetry GenAI usage
conventions when exporting a report. Leave message content out of that export. The conventions
document both token categories and the sensitivity of prompt and message attributes.
[OpenTelemetry GenAI attributes](https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/)

Start with Claude Code and Codex review attempts because jackstay already controls their
subprocess boundaries. Prove partial, retried and interrupted attempts, child sessions without
parent identity, duplicate result envelopes, a changed price table and redaction in exported
reports. Later agent-session budgets depend on reliable usage payloads or transcript APIs and
should be gated on an upstream compatibility fixture. This initiative extends A4 and has medium
priority because the upstream demand is concrete while jackstay can currently measure only a
small subset of overall agent activity. [Usage-in-hooks request](https://github.com/anthropics/claude-code/issues/11008) · [Stop-hook request](https://github.com/anthropics/claude-code/issues/80446)

## Validation before product commitment

Recruit adopters beyond the author's projects. Ask them to rank their actual interruptions,
platforms, runners, number of simultaneous sessions and time spent diagnosing a failed gate.
Record reproductions with sanitized fixtures and compare them with the issues above. Validate
one initiative at a time in an opt-in local slice, publish its acceptance results and reconsider
priority when usage disagrees with these external signals. Keep the roadmap's evidence date and
upstream issue status current when a design decision is proposed.
