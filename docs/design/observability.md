# Monitoring and analytics: jackstay's journal of OpenTelemetry traces, `jst log`, `jst stats`

Decision: [W-056](../decisions/W-056-observability-otel-traces-local.md); versions checked at the
source ([W-037](../decisions/W-037-current-go-and-dependencies.md)). Status: built. How to read
the journal is the [observability guide](../guide/observability.md).

## 0. What it is for, in one paragraph

Every jackstay process gets one OpenTelemetry trace context, so any call can be traced to the
process and session that made it. Each process writes what it did as spans into one local journal
in OTLP-JSON, passes the context to every child through `TRACEPARENT`, records the Claude and Codex
ids that join its spans to their sessions, and `jst log` and `jst stats` answer the questions of
section 11. jackstay's own stores stay the source of truth (section 9): a lost span loses no state.

## 1. Versions, checked at the source on 2026-11-17 (W-037)

| what | pinned | source |
|---|---|---|
| Go tracing API and SDK | `go.opentelemetry.io/otel`, `/otel/trace`, `/otel/sdk` **v1.47.0** (stable, 2026-10-02) | `go list -m -versions` against the module proxy: v1.47.0 is the newest |
| OTLP v1 protocol types | `go.opentelemetry.io/proto/otlp` **v1.11.1** (packages `trace/v1`, `common/v1`, `resource/v1` only — never `collector/…`, so no gRPC is compiled in) | same |
| JSON of protobuf | `google.golang.org/protobuf` at the version `proto/otlp` v1.11.1 requires | its go.mod |
| GenAI conventions | repository `open-telemetry/semantic-conventions-genai`, commit **`e6658f14c9d9ea0760825f029cb82e7673f48fab`** (2026-11-06; the repository has no tags), schema `https://opentelemetry.io/schemas/gen-ai-dev/1.42.0-dev`, stability *development*; built on semconv **v1.45.0** | GitHub API, `model/manifest.yaml` at that commit |

Everything *development* (every `gen_ai.*` name, the environment carrier of `TRACEPARENT`) is
confined to one adapter (section 6) or is the W3C header text. No metrics or logs SDK, no exporter
module, no collector, no network; the resource is built by hand, without `resource.Default()`.

## 2. The span model

One **trace** per tree of jackstay processes: a jackstay process with no inherited `TRACEPARENT` starts a new
trace; one that inherits it continues it (section 5). Spans, all written by the process that owns
them:

| span | name | kind | when | parent |
|---|---|---|---|---|
| **command** | `jst <command>` (`jst test`, `jst review`, `jst guard`) | SERVER when inherited from a non-jackstay parent, else INTERNAL | every invocation of the binary, from `cli.Main` before the command runs to its exit status | the inherited `TRACEPARENT`, else none (a root) |
| **phase** | `<command>.<phase>` (`test.run`, `test.gate`, `review.attempt`) | INTERNAL | a named stage a command reports; nests | the command span or an enclosing phase |
| **wait** | `wait <lock file name>` | INTERNAL | every wait for a lock, a slot or a queue that can block | the phase or command waiting |
| **child** | `exec <argv0 base name>` (`exec just`, `exec go`, `exec codex`) | CLIENT | every process started through `supervise.Run` (the one runner) | the phase or command that starts it |
| **agent** | `invoke_agent codex` (GenAI, section 6) | CLIENT | each Codex run of `jst review`: a span `internal/review` owns, opened before the run and ended only after the run's `events.jsonl` is read and its outcome known; the run's `exec codex` child span is under it | the attempt |

Not spanned: `git` calls (summed on the phase as `jackstay.git.calls`/`jackstay.git.seconds`), and
reads of files and the process table.

**Span events** (point facts inside a span, never spans of their own):

| event | on | attributes |
|---|---|---|
| `jackstay.guard.decision` | the guard's command span | `jackstay.guard.outcome` (allowed, refused, warned, failed_open, ignored_tool, config_error), `jackstay.guard.rules` (list), `jackstay.guard.warnings` (list) |
| `jackstay.test.verdict` | a test step or gate step | `jackstay.test.outcome` (the receipts vocabulary: passed, failed, reused, …), `jackstay.test.count`, `jackstay.test.key`, `jackstay.test.head` |
| `jackstay.review.verdict` | a round | `jackstay.review.outcome`, `jackstay.review.findings` by severity (`jackstay.review.findings.blocker` …), `jackstay.review.disposition` |
| `jackstay.codex.turn` | the agent span | the turn's usage (section 6), `jackstay.codex.turn.index`, `jackstay.codex.turn.failed` |
| `jackstay.bg.started` / `jackstay.bg.requested_outside` | `jst bg`'s span (a confirmed start) / the guard's span (a request: on the guard's decision, allowed or refused, recorded with it) | `jackstay.bg.id`, `jackstay.bg.deadline`; for a request, the guard's outcome on it |
| `jackstay.lock.acquired` | a wait span | `jackstay.lock.holder_pid` (the holder before, from its note), `jackstay.lock.queued`; the note's text is never recorded (it holds the holder's raw argv) |
| `exception` (semconv, stable) | any span that ends by a panic or a fault | `exception.type`, `exception.message`, `exception.stacktrace` (`runtime/debug.Stack()` at the recover) |

**Status and errors.** A span is *Error* when its operation did not do what was asked: exit status
not 0, a child ended non-zero or by a signal, a wait gave up, a Codex run not `done`, the guard
failed open. `error.type` names the class (`exit_1`, `signal_SIGTERM`, `timeout`, `gave_up`,
`no_verdict`, `panic`, `failed_open`); `jackstay.outcome` keeps the command's own word, so a red
test differs from a crash. A guard refusal is not an error: status *Unset*, outcome `refused`.

**Every span is ended, a signalled command's included.** `telemetry.Finish(status)` ends the open
spans innermost first (still open: `jackstay.outcome` = `stopped`, `error.type` = `signal_<NAME>`)
and flushes, idempotent and bounded at 200 ms. `cli.Main` calls it after `entry.Run`, and
`supervise.EndBy` before it re-raises a signal. A process killed by SIGKILL writes nothing for its
open spans; `jst log` shows their ended children under "parent not recorded".

**The caller.** Every command span carries `process.pid` and `process.parent_pid`. A guard that
fails open or panics also records `jackstay.caller.argv`: the parent's arguments, masked, from the
kernel (`kern.procargs2`, `/proc/<ppid>/cmdline`), at most 1 KiB.

## 3. Attributes

Resource (one per process, written on every line): `service.name` = `jst`, `service.version` =
`internal/version`, `jackstay.build.commit`, `process.pid`, `process.parent_pid`, `host.name` is **not**
recorded (one machine), `jackstay.project` (the project's name, `config.ProjectName`, or absent).

`jackstay.*` attributes (a rename changes this table and the queries in the same commit):

| attribute | type | on |
|---|---|---|
| `jackstay.command` | string | command span: the command's table name (`test`, `lint pr`) |
| `jackstay.args` | string list | command span: its arguments, masked |
| `jackstay.cwd`, `jackstay.worktree`, `jackstay.branch`, `jackstay.head` | string | command span (`jackstay.head`/`jackstay.branch` only where the command already reads them; no git call is added for telemetry) |
| `jackstay.exit_code` | int | command and child spans |
| `jackstay.outcome` | string | command, phase, child |
| `jackstay.signal` | string | child: the signal it died by; `jackstay.stopped_by`, `jackstay.killed` |
| `jackstay.child.argv` | string list | child span: argv, masked; `jackstay.child.pid` |
| `jackstay.claude.session_id` | string | every command span that knows it (hook input, else `CLAUDE_CODE_SESSION_ID`) — the raw copy, beside the adapter's `gen_ai.conversation.id` |
| `jackstay.claude.tool_use_id`, `jackstay.claude.prompt_id`, `jackstay.claude.agent_id`, `jackstay.claude.agent_type`, `jackstay.claude.transcript` | string | the guard's command span, from its input |
| `jackstay.codex.thread_id` | string | agent span |
| `jackstay.review.task`, `jackstay.review.round`, `jackstay.review.attempt`, `jackstay.review.store` | string/int | review phases; `jackstay.review.store` is the round's folder (the link into jackstay's store, section 9) |
| `jackstay.test.scope`, `jackstay.test.key`, `jackstay.test.run_id`, `jackstay.test.output` | string | test phases; `jackstay.test.output` is the kept output's path |
| `jackstay.bg.id`, `jackstay.bg.name` | string | `jst bg` |
| `jackstay.github.operation`, `jackstay.github.repository`, `jackstay.github.pull_request` | string, string, int | `jst checks` and `jst land status`/`find`: the subcommand; with `jst land`, the repository and number of a resolved pull request |
| `jackstay.lock.name`, `jackstay.lock.path`, `jackstay.wait.gave_up` | string/bool | wait spans |
| `jackstay.guard.command` | string | guard: the masked command, at most 1,000 bytes; `jackstay.guard.command_sha256` of the unmasked text so equal commands stay equal |
| `jackstay.git.calls`, `jackstay.git.seconds` | int/double | phases and command |

The raw Claude and Codex ids are kept under `jackstay.*` beside the adapter's `gen_ai.*` names, so
an older journal still answers after the conventions rename one.

## 4. The journal: OTLP-JSON lines under `~/.jackstay`

**Where.** `<jackstay home>/<project marker>/telemetry/<YYYY-MM-DD>.otlp.jsonl`, so each project's
retention follows its own `jackstay.json`; a process with no project writes under `.all/telemetry/`.
The date is the local date the span ended. Folder 0700, files 0600.

**A line** is one OTLP v1 `TracesData` in its JSON encoding: one `ResourceSpans`, one `ScopeSpans`
(scope `jst`, the jackstay version), and the span(s) ended together. Lines are self-contained, so any
OTLP file reader (a collector's `otlpjsonfile` receiver, otel-desktop-viewer) reads the folder as it
is.

**The exporter** (`journal.Exporter`, registered with `trace.WithSyncer`, so a span is written when
it ends):

1. Converts the SDK's `ReadOnlySpan`s to OTLP `ResourceSpans`.
2. Masks every string attribute, event attribute and status message (section 7).
3. Encodes OTLP JSON (ids lowercase hex, enums as numbers, 64-bit integers as strings) with its own
   structs and `encoding/json`, not `protojson` (section 16). `journal/otlpcheck` (tests only)
   round-trips every test line through the pinned proto's `protojson`, and the two must be equal.
4. Appends in one `write` under `flock(LOCK_EX)`, tried every 5 ms for at most 200 ms (else the line
   is dropped). If the file's last byte is not a newline, the line starts with one, so a torn write
   stays its own damaged line.
5. Cuts a line over 256 KiB by cutting its largest attribute values to 4 KiB, with
   `jackstay.truncated` = true; a span is never split.
6. Runs the folder's upkeep (section 10) after the write that created a day's file.

The exporter never fails a command. `JACKSTAY_TELEMETRY=off` makes the provider a no-op.

**Reading.** `journal.Read(folders, from, to)` yields the spans that **overlap** the range, reading
every day file from `from`'s date through today (files are named by end date); time-summing reports
clip spans to the range. Of one date it reads one file: the plain one when it exists, else the
`.gz` (upkeep removes the plain one only after verifying the `.gz`), so nothing counts twice. Spans
come flattened into one Go record each, decoded with `encoding/json` into a small mirror of the
OTLP shape. A line it cannot decode is skipped and counted (printed on standard error); a torn last
line is tolerated.

## 5. Context: `TRACEPARENT` in and out

- **In.** `cli.Main` reads `TRACEPARENT` (and `TRACESTATE`) from its environment with the SDK's W3C
  `propagation.TraceContext` over an environment carrier. A valid value makes the command span its
  child: jackstay under jackstay, and under Claude Code's beta tracing a child of its
  `claude_code.tool` span. An invalid value is ignored and recorded as
  `jackstay.traceparent.invalid`. The sampled flag is ignored: jackstay always records.
- **Out.** `cli.Main` sets `TRACEPARENT` in its own environment to the command span, so every child
  inherits it; `supervise.Run` gives each child its own `exec` span's context instead.
- **Claude's ids** come from the hook input in the guard (`session_id`, `tool_use_id`,
  `prompt_id`, `agent_id`, `agent_type`, `transcript_path`) and from `CLAUDE_CODE_SESSION_ID` in
  every other command (undocumented, so best effort).
- **Codex's id** is the `thread_id` of the `thread.started` event in the round's `events.jsonl`,
  the join key into `~/.codex/sessions`.

**What joins what, exactly:**

| from | to | by |
|---|---|---|
| a jackstay process | its jackstay children | the trace (parent span ids) |
| a Claude session | every jackstay span of it | `gen_ai.conversation.id` / `jackstay.claude.session_id` |
| a Claude tool call | the guard's decision on it | `gen_ai.tool.call.id` / `jackstay.claude.tool_use_id` |
| a Claude tool call | the jackstay command it ran | the trace when Claude's beta tracing is on; else the session plus the time order (the guard's span for the call ends before the command span starts, same session, the command's argv in the guard's command text) — `jst log --tool-call` shows the candidates and says the join is by time |
| a review attempt | the Codex rollout | `jackstay.codex.thread_id` |
| a span | jackstay's own store | `jackstay.review.store`, `jackstay.test.output`, `jackstay.bg.id`, `jackstay.test.run_id` |

## 6. The GenAI adapter (`internal/telemetry/genai`)

The only package that knows a `gen_ai.*` name. It holds the pinned revision as constants
(`Revision = "e6658f14c9d9ea0760825f029cb82e7673f48fab"`, `SchemaURL`) and two halves:

**Writing** — functions that annotate a span jackstay already made:

- `Conversation(span, sessionID)` → `gen_ai.conversation.id` on every span of a process that
  knows a Claude session.
- `ToolCall(span, ToolCall{Name: "Bash", ID: toolUseID, Agent: agentType, AgentID: agentID})`
  → on the guard's span: `gen_ai.tool.name`, `gen_ai.tool.call.id`, `gen_ai.tool.type` =
  `function`, `gen_ai.agent.name`/`gen_ai.agent.id` for a subagent. The guard judges a tool call;
  it does not execute it, so it sets **no** `gen_ai.operation.name` (it would claim an
  `execute_tool` span that is Claude's).
- `AgentRun(span, AgentRun{Provider: "openai", Agent: "codex", Model, Effort, ThreadID, Usage,
  Failure})` → on the Codex child span, renamed `invoke_agent codex`, kind CLIENT:
  `gen_ai.operation.name` = `invoke_agent`, `gen_ai.provider.name` = `openai`,
  `gen_ai.agent.name` = `codex`, `gen_ai.request.model` (when set), `gen_ai.request.reasoning.level`
  (the effort), `gen_ai.conversation.id` = the thread id, and the usage summed over the run's
  turns: `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`,
  `gen_ai.usage.cache_read.input_tokens`, `gen_ai.usage.cache_write.input_tokens`,
  `gen_ai.usage.reasoning.output_tokens`; `error.type` on failure. Each turn is a `jackstay.codex.turn`
  event with the same usage names (the turn's own).
- `Tracer(provider)` is made with `trace.WithSchemaURL(SchemaURL)`; the agent span comes from it, so
  its `ScopeSpans` carries the pinned schema. Other spans use jackstay's tracer (scope `jst`).

**Reading** — the queries never name a `gen_ai.*` attribute; they call the adapter:
`ConversationOf(span)`, `ToolCallOf(span)`, `AgentRunOf(span)` (provider, agent, model, thread,
usage, failure) and `IsAgentRun(span)`. Each reads the current names and falls back to the
`jackstay.*` raw copy, so a journal written under an older revision still answers.

**Golden tests** (`internal/telemetry/golden`, in the gate): five scenarios run through the real
code with a fake clock, fake ids and fakes at the edges, each checked for content and query answer:

| scenario | produced by | content checked (golden file of the normalised spans: ids and times replaced by order) | query answer checked |
|---|---|---|---|
| model call | `jst review` round with a fake Codex: one attempt, `thread.started`, two turns with usage, a verdict | `invoke_agent codex` CLIENT child of `review.attempt`, `exec codex` under it, all usage attributes summed, two `jackstay.codex.turn` events, conversation = thread id, the scope's schema URL the pinned one | `jst stats reviews` gives the task, 1 round, tokens as summed, minutes, outcome `done` |
| tool call | the guard hook given a Bash call with `session_id`, `tool_use_id`, refused by `rm-recursive` | `jst guard` span with the tool call's attributes, a `jackstay.guard.decision` event, status Unset | `jst log --tool-call ID` finds it; `jst stats refusals` counts it under its rule |
| retry | a review round whose first attempt's fake Codex exits 1 and second answers | two `invoke_agent` spans under one round, the first with status Error, `error.type` = `exit_1`, `jackstay.review.attempt` 1 and 2 | `jst stats reviews` counts 1 round, 2 attempts, 1 Codex failure `exit_1`, tokens of both |
| error | the guard given empty standard input under a parent whose argv is known; and a command that panics | the guard span with status Error, `error.type` = `failed_open`, `jackstay.caller.argv`, `process.parent_pid`; the panicking command's span with an `exception` event holding the stack | `jst stats guard` lists the failed-open call with its caller; `jst log --outcome error` shows the stack's first frames |
| nested agent | a Claude subagent's call (`agent_type` = `Explore`) through the guard, then `jst review` started under the same session with an inherited `TRACEPARENT`, running Codex | the guard span with `gen_ai.agent.name` = `Explore`; `jst review`'s command span the child of the inherited parent; `invoke_agent codex` three levels below; one conversation id on all | `jst log --session ID` shows one tree under the inherited parent with the guard call beside it, and `jst stats sessions` attributes the review's minutes and tokens to the session |

**Update procedure**: moving the pinned revision, `otel` or `proto-otlp` is one commit that changes
the constants and the golden files only where a name changed; the query answers must not change.

## 7. Secrets and content in command text

**Threat model.** The journal is local (files 0600, nothing sent), and Claude Code's transcripts
already hold every command verbatim. Masking protects a journal that **leaves** by hand: an excerpt
pasted into a pull request, a day file attached to a bug report. The guarantees:

1. **Secrets** — credentials recognised by name, position or shape (below) — are never written, in
   any mode. This is the promise the tests hold, on all four command attributes.
2. **What jackstay itself handles** — the prompt `jst review` sends to Codex, a child's output, file
   contents jackstay reads, Codex's answers, transcript text — is never written.
3. **Content an agent typed into a command line** (a short quoted string, a `sed` script, a request
   body) is reduced where its place is known, and otherwise kept in `masked` mode as part of the
   command text, exactly as the transcript already keeps it. `skeleton` mode removes all of it.

**Every attribute name is declared** in `internal/telemetry`'s table as an identity (ids and
digests, never masked), a command attribute, free text or a plain value; an undeclared name is
refused at the call, and a test walks every instrumented path.

**Command attributes** (`jackstay.guard.command`, `jackstay.args`, `jackstay.child.argv`, `jackstay.caller.argv`). A command
text is split into simple commands and words by `mvdan.cc/sh` (the guard's parser, already a
dependency); text it cannot parse is recorded as `<unparsed N bytes>`. Wrappers are unwrapped and
what they run is classified as a command of its own: `env`, `nohup`, `time`, `timeout`, `nice`,
`sudo`, `xargs`, `exec`, `command`, `jst bg … --`, `jst test … --`, `jst lock … --`, and
`sh`/`bash`/`zsh -c TEXT` (TEXT parsed in turn). Then, in this order:

- **Credentials, by a table of commands and options** (`internal/telemetry/mask`, each row a fixture):
  options whose name contains `token`, `secret`, `password`, `passwd`, `pass`, `key`, `auth`,
  `credential`, `cookie` (case-insensitive, `-`/`_` ignored), with the value attached (`--x=v`,
  `-pVALUE`) or next; `-H`/`--header`, `-u`/`--user`, `--oauth2-bearer`, `-b`/`--cookie` of
  `curl`/`wget`/`http`; `-p` of `sshpass`, `mysql`, `mysqladmin`, `docker login`, `podman login`;
  `-passin`/`-passout`/`-pass`/`-k` of `openssl`; `--with-token`/`--password-stdin` sources;
  `git -c http.*extraheader=…` and `-c credential.*`; `NAME=value` with a secret-shaped name; URLs
  with user information (`scheme://…@`) in any word; any word containing a known token shape
  (`ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`, `github_pat_`, `sk-`, `xox[abprs]-`, `AKIA…`, `glpat-`,
  JWTs, PEM markers) or a run of 32 or more hex or base64 characters. Such a value becomes `***`.
- **Known content positions** become `<N bytes>`: every argument of `echo`/`printf`; here-documents
  and here-strings; `-m`/`--message`/`-F` of `git commit`/`git tag`/`git notes`; `--body`/`--title`
  of `gh`; the positional words of an agent (`codex`, `claude`, `llm`, `aider`) after its
  subcommand; the script of `sed`/`awk`/`perl -e`/`python -c`/`node -e`/`ruby -e`/`jq`;
  `-d`/`--data*`/`-F`/`--form`/`--json` of `curl`; any word longer than 200 bytes or holding a
  newline.
- **`skeleton` mode** (the setting below) keeps only each program's base name, its subcommand when
  the program has a known table of them (`git`, `gh`, `go`, `just`, `jst`, `npm`, `pnpm`, `cargo`,
  `docker`, `kubectl`, `swift`, `xcodebuild`, `codex`, `claude`), option names, and redirection
  operators; every other word is `<N bytes>`.

Examples (golden fixtures): `curl -u user:pw https://api` → `curl -u *** https://api`;
`codex exec 'Review the plan'` → `codex exec <15 bytes>`; in `skeleton` mode
`git checkout -- docs/plan.md` → `git checkout -- <12 bytes>`.

**Free text** (status and exception messages, stacks) is masked by the same token
shapes, URL userinfo, `Authorization`/`Cookie`/`Bearer`/`Basic` values and `NAME=value` rules, and
cut to 4 KiB. It comes from jackstay's own words, never a child's output or another process's argv
(the lock's holder note is reduced to its pid).

**The setting**: `telemetry.commands` in `jackstay.json` (section 10) — `masked` (the default: command
text kept with secrets masked, W-056) or `skeleton`. `jackstay.guard.command_sha256` of the unreduced
text is written in both modes, so equal commands stay equal.

## 8. The guard: what it writes and what it may cost

The guard writes **every** call after its answer, bounded by `exitBy`: an allowed call as its span
and decision event (about 0.6 KiB), a refusal, warning, failure or crash in full, a non-Bash call
as `ignored_tool` with no command.

**Budget: +1 ms on the median** of the hook's wall time, process start to exit. `just
guard-latency` runs the built binary 500 times each on an allowed and a refused call, telemetry off
and on, interleaved, with a fresh `JACKSTAY_HOME`, and reports median and p95. The span is the
guard's only record.

## 9. jackstay's stores stay the source of truth

Receipts, `runs.jsonl`, review folders, `procs`, id counters and the expected list are
written first and read by the commands. Spans **observe** them and link to them by path or id. No
command reads the journal to decide anything; its readers are `jst log`, `jst stats` and
`jst doctor`'s 7-day report. A journal deleted whole changes no behaviour (each instrumented
command is tested with the folder unwritable). The guard and `jst bg` keep no other event log.

## 10. Retention, compression, the `telemetry` section of `jackstay.json`

| key | meaning | default | bounds |
|---|---|---|---|
| `keep_days` | a day file older than this many days is deleted | 180 | 1–3650, and ≥ `compress_after_days` |
| `compress_after_days` | a day file older than this is gzipped to `<date>.otlp.jsonl.gz` | 14 | 1–3650 |
| `commands` | how command text and argv are recorded (section 7): `masked` or `skeleton` | `masked` | one of the two |

A project without the section, and `.all`, get the defaults.

**Upkeep** runs in the process that creates a day file, after its write, under a non-blocking
`telemetry/.upkeep.lock` (held: skip). It gzips each plain file older than `compress_after_days`
(`.gz.partial`, fsync, rename, then remove the plain file only once the `.gz` decodes whole),
deletes files older than `keep_days`, and stops after 2 s of work. The guard runs it only inside
its `exitBy` bound. Size, *inferred*: a few MB a day before gzip.

## 11. The questions the system must answer (acceptance tests)

Each is a test over a journal produced by the instrumented code, asserting the answer.

| # | question | answered by |
|---|---|---|
| A1 | What did Claude session X do through jackstay — every guard decision, command, test, gate, background process and review round, with outcomes and durations, in order? | `jst log --session X` |
| A2 | What happened inside one run of a command: its phases, waits, children with argv, exit codes and signals, and where its time went? | `jst log --trace T` (a tree with durations) |
| A3 | Why did the guard fail open or crash, and which process called it (parent pid and argv, inherited trace, version)? | `jst stats guard` (failures with caller and stack head); `jst log --outcome error --command guard` |
| A4 | How fast is the guard: calls, median and p95 latency, by version? | `jst stats guard` |
| A5 | What do refusals cost: per rule, how many, share, how often the agent retried (another call in the same session within 2 minutes whose command differs and is allowed) and how long it took, how often it gave up; by project? | `jst stats refusals` |
| A6 | Which test steps are flaky: same key and head, different outcomes? What did failed and stopped runs cost in time, per project? | `jst stats flaky`, `jst stats tests` |
| A7 | Where do commands spend time: each phase's count, median, p95; each lock's waits (count, total, p95, gave-ups)? | `jst stats phases` |
| A8 | What does a review cost and does it converge: rounds and attempts per task, findings per round by severity, Codex failures by `error.type`, tokens (input, cached, output, reasoning), minutes? | `jst stats reviews` |
| A9 | What is asked to run outside jst: Bash calls with `run_in_background` not through `jst bg`, **as requests** with the guard's decision on each (the guard sees a request, not a start: a refused or later blocked call never ran), per session and project; beside them the confirmed starts of `jst bg` (its spans), its deadlines hit and orphans stopped. | `jst stats background` |
| A10 | Where did a session's time go: jackstay's share (guard, waits, tests, reviews) per Claude session; with `--claude`, beside the session's own tokens and cost read from its transcript (`totalCostUSD`, `usage`, read defensively, on demand, never copied)? | `jst stats sessions [--claude]` |
| A11 | Which machine load did each project put on the machine: busy time of jackstay commands and their children per project and day, and the peak number at once? | `jst stats load` |
| A12 | Did a jackstay version make things better or worse? | any report with `--by version` |
| A13 | What did tool call Y lead to: the guard's decision, and the jackstay command(s) it ran (exact by trace, else the candidates by session and time, said so)? | `jst log --tool-call Y` |
| A14 | Is the journal readable by a standard OTLP reader? | a test decoding every line of the golden journals with `protojson` into `tracepb.TracesData` after the hex ids are turned back, and comparing |

## 12. `jst log` and `jst stats`

Flags: `jst log --help`, `jst stats --help`; use: the [observability guide](../guide/observability.md).

- Defaults: `jst log` the last 24 hours of the current project's folder (`--all-projects` reads
  every project's and `.all`); `jst stats` the last 30 days. `--session`, `--trace` and
  `--tool-call` read every project (a session crosses projects). `--since` takes `2h`, `7d`, or an
  RFC 3339 time.
- `jst log --json` prints one object per span: the flattened reading record of section 4, stable
  keys. `jst stats REPORT --json` prints `{"report", "range", "rows", "unreadable_lines"}`.
- Reports (section 11) are Go functions from spans to rows; derived episodes (retry after refusal,
  flakiness, convergence) are computed at query time, never stored.
- Exit 0 with an answer (an empty one included), 2 for usage, 1 when no folder could be read.

## 13. Packages and seams

| package | holds |
|---|---|
| `internal/telemetry` (the one shared package) | the provider (`Start` from `cli.Main`, `Finish` with the exit status, panic capture), `Phase`/`Wait`/`Child` helpers that take a `context.Context` or fall back to the command span, `ChildEnv` (TRACEPARENT out), the caller's argv, the `jackstay.*` names |
| `internal/telemetry/journal` | the exporter (conversion, mask hook, OTLP JSON, lock, day files), upkeep, the reader; `journal/otlpcheck` (tests only) holds the pinned proto |
| `internal/telemetry/mask` | section 7: the word classifier over the guard's parser, the free-text shapes, the output sweep (section 16) |
| `internal/telemetry/genai`, `/golden` | section 6 |
| `internal/telemetry/query` | what `jst log` and `jst stats` share: reading, ranges, clipping |
| `internal/logs` (`jst log`) and `internal/stats` (`jst stats`, nine reports, the transcript reader for `--claude`) | section 12 |
| `internal/cli` | the command span around `entry.Run`; `TRACEPARENT` in and out; `cli.Main` recovers a panic only to record it, then panics again (the crash is unchanged) |
| `internal/supervise` | a child span per `Run`, its `TRACEPARENT` in the child's environment; `EndBy` calls `telemetry.Finish` before re-raising the signal |
| `internal/flock` | a wait span in `Acquire`/`WaitFile` when the lock was not free at once |
| `internal/review` | the attempt spans; the agent span it owns around each Codex run, annotated from the attempt's `events.jsonl` and the run's outcome before it ends |
| `internal/receipts` (`RunLog.Write`) | each run-log line also becomes a span start/end or a verdict event — the seam for `jst test` and the gate, so `internal/testing` knows nothing of telemetry |
| `internal/procs`, `internal/procs/background` | `jst bg` events, unregistered processes |
| `internal/guard` (hook) | the decision span and the tool-call annotation; stack on a recovered panic |

`proto/otlp` and `protobuf` are test-only dependencies.

## 14. Tests

- **Unit**: the exporter (OTLP decoding, the 200 ms lock give-up, the 256 KiB cut, an unwritable
  folder), upkeep with a fake clock (including a stop at `.gz.partial` and a corrupt `.gz`), the
  mask corpus, `TRACEPARENT` in and out, the reader, each report, `jst log`'s filters.
- **Golden** (section 6) and **acceptance** (section 11: A1–A14, `internal/stats/acceptance_test.go`).
- **Integration** (built binary): a `jst test -- sleep 30` stopped by SIGTERM ends its span
  `stopped` with `signal_SIGTERM` and still dies by the signal; `jst test -- jst version` makes one
  trace; a panic leaves an `exception` event; the guard with empty input records its caller; two
  writers to one day file leave whole lines.
- **No journal changes behaviour**: one test per command makes the telemetry folder unwritable.
- **Latency** (section 8) is outside the gate; an exporter benchmark runs in it as a smoke check.

## 16. Implementation details

- **No protobuf in the binary** (section 4, step 3), and the mask's patterns compiled on first use,
  keep the guard within its +1 ms budget.
- **`test.run`** spans come from the run log's `start`/`run` lines; a gate's steps are runs under
  `test.gate`.
- **Attributes added**: `jackstay.guard.answer_ms` (the hook's time to its answer, the latency
  report's measure), `jackstay.guard.tool`, `jackstay.review.reviewer`, `jackstay.reason` (free text).
- **`jackstay.outcome`** of a command that says none is `ok` for exit 0; a command's own word (the
  guard's decision) is never written over.
- **`jst log --tool-call`** joins by time within 30 seconds of the guard's answer, in the same
  session, when no trace links them, and says so.
- **Ranges and days in `jst stats`**: one rule for every report — a span's counts and measures (a
  call, a run, its tokens, its verdict) belong to the moment it ended (the day file it is in); its
  time is clipped to the range. `--by day` is that rule with each day of the local calendar as the
  range, over every span, so a span keeps its parent across midnight and nothing is counted twice.
  `load` counts a command once, on the local day its span ended.

**The mask, as it works** (section 7 made exact; each rule is strict rather than precise, because
where a secret's value lies in arbitrary shell syntax cannot be found reliably):

- **Options** are read per program from one table of the options whose value is a secret or
  content, as getopt reads a short-option cluster (`-suUSER:PW`: everything after the first such
  letter is the value, else the next word is); a letter it does not know is a flag. A program's
  path is kept in `masked` mode (it is the command's own, not content) up to 200 bytes.
- **Assignments**: every assignment of the parse (before a command, `export`, `local`, `declare`,
  alone) goes through one rule, by its name and its value. Every array, and every indexed
  assignment `NAME[KEY]=v`, is `***` whole.
- **Strict commands.** Knowing *that* a command names a secret is easy, so such a command is
  written strictly: a command whose program carries credentials in its options (curl and its kin,
  wget, sshpass, the mysql tools, `docker`, `podman` and `helm` whatever their subcommand,
  openssl), or any of whose words is a secret-named option (`--token`, `-storepass`) or
  assignment, keeps only its program, subcommand, long option names (`--name=`), two-character
  short options, and URLs without user information; every other word, an assignment-shaped one
  included, becomes `***`. Commands that name no secret keep the content-position rules of
  section 7.
- **The sweep.** Last, every token of the output shaped like a secret (URL user information, a token
  prefix, a long hex or base64 run, a PEM marker) is replaced whole: the guarantee for everything
  with a shape.
