# Monitoring and analytics: jackstay's journal of OpenTelemetry traces, `jst log`, `jst stats`

Decision: [W-056](../decisions/W-056-observability-otel-traces-local.md), from a study of what
Claude Code and Codex expose, the state of OpenTelemetry, and the guard's refusals. Rulings it
follows:
[W-037](../decisions/W-037-current-go-and-dependencies.md) (current versions, checked at the source),
[W-019](../decisions/W-019-repeated-findings-mean-redesign.md) (a place that draws findings again is
redesigned, not patched).

Status: built.

## 0. What it is for, in one paragraph

Separate per-command logs with their own field sets share no id, carry no Claude session on test
runs, no parent process and no stack, so a guard call that failed open cannot be traced to the
process that made it. This design gives every jackstay process one
OpenTelemetry trace context, writes
what each process did as spans into one local journal in the standard OTLP-JSON encoding, carries
the context to every child through `TRACEPARENT`, records the Claude and Codex ids that join jackstay's
spans to their sessions, and answers the questions of section 11 with two commands. jackstay's own
stores stay the source of truth (section 9): a lost span loses no state.

## 1. Versions, checked at the source on 2026-11-17 (W-037)

| what | pinned | source |
|---|---|---|
| Go tracing API and SDK | `go.opentelemetry.io/otel`, `/otel/trace`, `/otel/sdk` **v1.47.0** (stable, 2026-10-02) | `go list -m -versions` against the module proxy: v1.47.0 is the newest |
| OTLP v1 protocol types | `go.opentelemetry.io/proto/otlp` **v1.11.1** (packages `trace/v1`, `common/v1`, `resource/v1` only — never `collector/…`, so no gRPC is compiled in) | same |
| JSON of protobuf | `google.golang.org/protobuf` at the version `proto/otlp` v1.11.1 requires | its go.mod |
| GenAI conventions | repository `open-telemetry/semantic-conventions-genai`, commit **`e6658f14c9d9ea0760825f029cb82e7673f48fab`** (2026-11-06; the repository has no tags), schema `https://opentelemetry.io/schemas/gen-ai-dev/1.42.0-dev`, stability *development*; built on semconv **v1.45.0** | GitHub API, `model/manifest.yaml` at that commit |

The stable parts used: the Tracing API and SDK, the W3C trace context, the OTLP v1 data model and
its JSON encoding (OTLP specification, "JSON Protobuf Encoding": ids as hex strings, enums as
numbers, lowerCamelCase names), semconv's stable `error.type`, `exception.*`, `service.*`. What is
*development* — every `gen_ai.*` name and the environment-variable carrier of `TRACEPARENT`
(release candidate) — is confined to one adapter (section 6) or is trivially the W3C header text.

No metrics SDK, no logs SDK, no exporter module, no collector, no network. The SDK's
`resource.Default()` detectors are not used (they probe the host); the resource is built by hand.

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

Not spanned, and why: each `git` call through `gitcli` (hundreds per command, milliseconds each;
they get `TRACEPARENT` through the environment and are summed on the phase as
`jackstay.git.calls`/`jackstay.git.seconds`); reads of files and the process table.

**Span events** (point facts inside a span, never spans of their own):

| event | on | attributes |
|---|---|---|
| `jackstay.guard.decision` | the guard's command span | `jackstay.guard.outcome` (allowed, refused, warned, failed_open, ignored_tool, config_error), `jackstay.guard.rules` (list), `jackstay.guard.warnings` (list) |
| `jackstay.test.verdict` | a test step or gate step | `jackstay.test.outcome` (the receipts vocabulary: passed, failed, reused, …), `jackstay.test.count`, `jackstay.test.key`, `jackstay.test.head` |
| `jackstay.review.verdict` | a round | `jackstay.review.outcome`, `jackstay.review.findings` by severity (`jackstay.review.findings.blocker` …), `jackstay.review.disposition` |
| `jackstay.codex.turn` | the agent span | the turn's usage (section 6), `jackstay.codex.turn.index`, `jackstay.codex.turn.failed` |
| `jackstay.bg.started` / `jackstay.bg.requested_outside` | `jst bg`'s span (a confirmed start) / the guard's span (a request: on the guard's decision, allowed or refused, recorded with it) | `jackstay.github.operation`, `jackstay.github.repository`, `jackstay.github.pull_request` | string, string, int | `jst gh`'s subcommand and the repository and number of a resolved pull request |
| `jackstay.bg.id`, `jackstay.bg.deadline`; for a request, the guard's outcome on it |
| `jackstay.lock.acquired` | a wait span | `jackstay.lock.holder_pid` (the holder before, from its note), `jackstay.lock.queued`; the note's text is never recorded (it holds the holder's raw argv) |
| `exception` (semconv, stable) | any span that ends by a panic or a fault | `exception.type`, `exception.message`, `exception.stacktrace` (`runtime/debug.Stack()` at the recover) |

**Status and errors.** A span's status is *Error* when its operation did not do what was asked:
a command's exit status is not 0, a child exited non-zero or by a signal, a wait gave up, a Codex
run's outcome is not `done`, the guard failed open. `error.type` (stable semconv) names the class
in jackstay's own words (`exit_1`, `signal_SIGTERM`, `timeout`, `gave_up`, `no_verdict`, `panic`,
`failed_open`); `jackstay.outcome` keeps the command's own outcome word (a test's `failed`, a guard's
`refused`) so a query can tell a red test from a crash. A refusal by the guard is not an error
(it is the guard working); its status is *Unset* and its outcome `refused`.

**Every span is ended, a signalled command's included.** `internal/telemetry` keeps the process's
open spans on a stack. `telemetry.Finish(status)` ends them innermost first (each still open
ends with `jackstay.outcome` = `stopped`, status Error, `error.type` = `signal_<NAME>` when a signal
is the cause) and flushes; it is idempotent and bounded (it gives up after 200 ms). It is called
by `cli.Main` after `entry.Run` returns, and by `supervise.EndBy` **before** it restores the
signal's default action and signals jackstay itself (`internal/supervise/signals.go`: `jst test` and
`jst bg` end that way when stopped, and `entry.Run` never returns). A process killed by SIGKILL
or that crashes in the runtime writes nothing for its open spans; their ended children are in the
journal, and `jst log` shows such a child under "parent not recorded".

**The caller.** Every command span carries `process.pid` and `process.parent_pid`. The guard's
span, when the guard fails open or panics, also carries `jackstay.caller.argv` — the parent
process's arguments (masked, section 7), read from the kernel (`sysctl kern.procargs2` on darwin,
`/proc/<ppid>/cmdline` on linux), at most 1 KiB; only then, for its latency budget (section 8). So
a guard started with an empty standard input records its caller's argv, its pid and the inherited
trace.

## 3. Attributes

Resource (one per process, written on every line): `service.name` = `jst`, `service.version` =
`internal/version`, `jackstay.build.commit`, `process.pid`, `process.parent_pid`, `host.name` is **not**
recorded (one machine), `jackstay.project` (the project's name, `config.ProjectName`, or absent).

`jackstay.*` attributes (no convention covers them; they are jackstay's and stable by jackstay's own rule — a rename
is a change to this section and to the queries in the same commit):

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
| `jackstay.lock.name`, `jackstay.lock.path`, `jackstay.wait.gave_up` | string/bool | wait spans |
| `jackstay.guard.command` | string | guard: the masked command, at most 1,000 bytes; `jackstay.guard.command_sha256` of the unmasked text so equal commands stay equal |
| `jackstay.git.calls`, `jackstay.git.seconds` | int/double | phases and command |

The raw Claude and Codex ids are kept under `jackstay.*` as well as under the adapter's `gen_ai.*`
names on purpose: the queries read them through the adapter (section 6), and if a revision of the
conventions renames `gen_ai.conversation.id`, the journal written before the change still answers.

## 4. The journal: OTLP-JSON lines under `~/.jackstay`

**Where.** `<jackstay home>/<project marker>/telemetry/<YYYY-MM-DD>.otlp.jsonl` — the project's own
folder (`homedir.ProjectPath`), so each project's retention follows its own `jackstay.json`; a process
with no project writes under `<jackstay home>/.all/telemetry/`. The date is the local date when the span
ended. Folder 0700, files 0600.

**What a line is.** One `TracesData` message of the OTLP v1 protocol in its JSON encoding, on one
line: one `ResourceSpans` (the process's resource), one `ScopeSpans` (scope `jst`, version the jackstay
version), and the span(s) ended together — usually one. A line is self-contained, so any OTLP
file reader (the specification's file exporter format, otel-desktop-viewer, a collector's
`otlpjsonfile` receiver) reads the folder as it is; no separate export step exists.

**The exporter** (`journal.Exporter`, implementing the SDK's `trace.SpanExporter`), registered
with `trace.WithSyncer` — the SDK's simple processor, so each span is written synchronously when
it ends, and a process that crashes after a span ended has written it:

1. The SDK's `ReadOnlySpan`s are converted to the shape of OTLP v1's `ResourceSpans` — the
   conversion is jackstay's, written against the proto and the SDK's read-only interface.
2. Every string attribute value, event attribute and status message passes the mask (section 7).
3. The message is encoded in OTLP's JSON encoding (ids as lowercase hex, enums as numbers,
   64-bit integers as strings) by jackstay's own structs and `encoding/json`, not `protojson`: the
   protobuf runtime in the binary cost the guard's latency budget (section 16). The pinned
   `go.opentelemetry.io/proto/otlp` stays the reference: package
   `journal/otlpcheck` (imported by tests only) decodes every line of the tests' journals with
   `protojson` into `tracepb.TracesData` and encodes it back, and the two must be equal.
4. The day file is opened for reading and appending, locked with `flock(LOCK_EX)` tried without
   blocking every 5 ms for at most 200 ms, written in one `write`, unlocked, and closed. If the
   last byte is not a newline, the append starts with one. This keeps a fragment from an interrupted
   write separate from the new span, so readers can report the damaged line and read the new one.
   A lock not had in 200 ms drops the line.
5. A line larger than 256 KiB has its largest attribute values cut to 4 KiB each with
   `jackstay.truncated` = true; a span is never split.
6. When the open created the day's file, the folder's upkeep runs (section 10), after the write.

The exporter never fails a command: every error is swallowed. `JACKSTAY_TELEMETRY=off` turns the
provider into a no-op (for the latency measurement of section 8 and for tests that must not see a
journal); nothing else reads it.

**Reading.** `journal.Read(folders, from, to)` yields the spans that **overlap** the range —
started before `to` and ended at or after `from`. Files are named by the end date, so it reads
every day file from `from`'s date through today (a span ending after `to` is in a later file),
and keeps a span only if it overlaps; reports that sum time (`load`, `sessions`, `phases`) clip
each span to the range. Of one date, it reads **one** file: the plain `.otlp.jsonl` when it
exists (upkeep removes it only after the `.gz` was verified whole, so while both exist the plain
one is complete), else the `.gz` — never both, so an upkeep stopped between its rename and its
removal counts nothing twice (a test runs the queries in that state). It yields spans flattened into one Go record each
(trace, span, parent, name, kind, start, end, status, attributes, events, resource attributes),
decoding the lines with `encoding/json` into jackstay's own small mirror of the OTLP JSON shape (the
reader does not need protobuf). It reads only the files whose date lies in the range, `.gz` ones
through `gzip`, skips a line it cannot decode and counts it (`jst log` and `jst stats` print the
count on standard error), and tolerates a torn last line.

## 5. Context: `TRACEPARENT` in and out

- **In.** `cli.Main` reads `TRACEPARENT` (and `TRACESTATE`) from its environment with the SDK's W3C
  `propagation.TraceContext` over an environment carrier. A valid value makes the command span its
  child (a remote parent): jackstay under jackstay (`jst test` → `just` → `jst check`), and jackstay under Claude Code
  when the user turns on Claude's beta tracing (then a jackstay command is a true child of Claude's
  `claude_code.tool` span). An invalid value is ignored and recorded as `jackstay.traceparent.invalid`.
  The sampled flag of the parent is ignored: jackstay always records (`sdktrace.AlwaysSample`), its
  journal is not a sample.
- **Out.** As soon as the command span exists, `cli.Main` sets `TRACEPARENT` in its own process
  environment to the command span, so every child started by any path (`exec.Command` in `gitcli`,
  `os.StartProcess` in `supervise`) inherits it. `supervise.Run` additionally gives each child the
  context of that child's own span (its `Spec.Env`, or jackstay's environment with `TRACEPARENT`
  replaced), so a jackstay command run as a test step is the child of that step's `exec` span.
- **Claude's ids** come from the hook input in the guard (`session_id`, `tool_use_id`,
  `prompt_id`, `agent_id`, `agent_type`, `transcript_path`) and from `CLAUDE_CODE_SESSION_ID` in
  every other command (present in Bash children; undocumented, so best effort).
  `CLAUDE_CODE_SESSION_ID` is passed on to children unchanged (it is already in the environment).
- **Codex's id** is the `thread_id` of the `thread.started` event in the round's `events.jsonl`
  (written by `jst review` today); it is the join key into `~/.codex/sessions`. Codex is not given
  a `TRACEPARENT` it would honour (not documented); it gets it in its
  environment anyway, harmless.

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
- `Tracer(provider)` gives the tracer the GenAI-annotated spans are created from, made with
  `trace.WithSchemaURL(SchemaURL)` (in v1.47 the schema URL belongs to the tracer, not to a
  span); the agent span is created from it, so its line's `ScopeSpans` carries the pinned
  schema. Every other span comes from jackstay's own tracer (scope `jst`, no schema URL).

**Reading** — the queries never name a `gen_ai.*` attribute; they call the adapter:
`ConversationOf(span)`, `ToolCallOf(span)`, `AgentRunOf(span)` (provider, agent, model, thread,
usage, failure) and `IsAgentRun(span)`. Each reads the current names and falls back to the
`jackstay.*` raw copy, so a journal written under an older revision still answers.

**Golden tests** (`internal/telemetry/golden`, run in the gate and named in the
update procedure below). Five scenarios, each produced by the real instrumented code paths with a
fake clock, fake ids and fakes at the edges (a fake Codex printing a recorded `--json` stream, a
hook input from a fixture), written to a journal in `t.TempDir()`; then **both** checked:

| scenario | produced by | content checked (golden file of the normalised spans: ids and times replaced by order) | query answer checked |
|---|---|---|---|
| model call | `jst review` round with a fake Codex: one attempt, `thread.started`, two turns with usage, a verdict | `invoke_agent codex` CLIENT child of `review.attempt`, `exec codex` under it, all usage attributes summed, two `jackstay.codex.turn` events, conversation = thread id, the scope's schema URL the pinned one | `jst stats reviews` gives the task, 1 round, tokens as summed, minutes, outcome `done` |
| tool call | the guard hook given a Bash call with `session_id`, `tool_use_id`, refused by `rm-recursive` | `jst guard` span with the tool call's attributes, a `jackstay.guard.decision` event, status Unset | `jst log --tool-call ID` finds it; `jst stats refusals` counts it under its rule |
| retry | a review round whose first attempt's fake Codex exits 1 and second answers | two `invoke_agent` spans under one round, the first with status Error, `error.type` = `exit_1`, `jackstay.review.attempt` 1 and 2 | `jst stats reviews` counts 1 round, 2 attempts, 1 Codex failure `exit_1`, tokens of both |
| error | the guard given empty standard input under a parent whose argv is known; and a command that panics | the guard span with status Error, `error.type` = `failed_open`, `jackstay.caller.argv`, `process.parent_pid`; the panicking command's span with an `exception` event holding the stack | `jst stats guard` lists the failed-open call with its caller; `jst log --outcome error` shows the stack's first frames |
| nested agent | a Claude subagent's call (`agent_type` = `Explore`) through the guard, then `jst review` started under the same session with an inherited `TRACEPARENT`, running Codex | the guard span with `gen_ai.agent.name` = `Explore`; `jst review`'s command span the child of the inherited parent; `invoke_agent codex` three levels below; one conversation id on all | `jst log --session ID` shows one tree under the inherited parent with the guard call beside it, and `jst stats sessions` attributes the review's minutes and tokens to the session |

**Update procedure** (written into the package's doc comment): moving the pinned revision, or
`otel`/`proto-otlp`, is one commit that changes the constants, regenerates nothing by hand, runs
the golden tests, and changes the golden files only where the conventions changed a name — the
diff of the golden files is the review of the migration. The query answers must not change.

## 7. Secrets and content in command text

W-056 keeps command text with secrets masked, and no classifier of arbitrary shell text can keep
the text and also promise that no content is ever recorded. So this section states what is
protected, against what, and gives the user the strict alternative as a setting.

**Threat model.** The journal is local: files 0600 under `~/.jackstay`, the user's account, nothing sent
anywhere by jackstay. Every command an agent runs is already stored verbatim, at the same permissions, in
Claude Code's transcripts on the same disk (`~/.claude/projects`). What
masking protects against is a journal **leaving** that boundary by hand: a `jst log` excerpt pasted
into a pull request or chat, a day file attached to a bug report, a load into a viewer. So the
guarantees are:

1. **Secrets** — credentials recognised by name, position or shape (below) — are never written, in
   any mode. This is the promise the tests hold, on all four command attributes.
2. **What jackstay itself handles** — the prompt `jst review` sends to Codex, a child's output, file
   contents jackstay reads, Codex's answers, transcript text — is never written.
3. **Content an agent typed into a command line** (a short quoted string, a `sed` script, a request
   body) is reduced where its place is known, and otherwise kept in `masked` mode as part of the
   command text, exactly as the transcript already keeps it. `skeleton` mode removes all of it.

**Identity attributes are never touched.** Ids and digests — `jackstay.head`, `jackstay.test.head`, `jackstay.test.key`,
`jackstay.test.run_id`, `jackstay.guard.command_sha256`, `jackstay.codex.thread_id`, the `jackstay.claude.*` ids,
`jackstay.bg.id`, `jackstay.review.*`, `gen_ai.conversation.id`, `gen_ai.tool.call.id`, `gen_ai.agent.id`,
`process.pid`/`parent_pid`, trace and span ids — are declared identities in
`internal/telemetry`'s table of names. Every attribute name must be in that table, as an identity,
a command attribute, free text or a plain value (numbers, outcomes, rule names); an undeclared
name is refused at the call, and a test walks every instrumented path, so nothing escapes
classification by being new.

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
`sshpass -p s3cr3t ssh host` → `sshpass -p *** ssh host`; `curl -H 'X-Api-Key: 3f…' https://api` →
`curl -H *** https://api`; `codex exec 'Review the plan'` → `codex exec <15 bytes>`;
`jst bg --for 47 -- codex exec 'p'` → `jst bg --for 47 -- codex exec <1 bytes>`;
`sed -i '' 's/old/confidential/' note.txt` → `sed -i '' <20 bytes> note.txt`; `git reset --hard
HEAD~1` unchanged; and in `skeleton` mode `git checkout -- docs/plan.md` → `git checkout -- <12 bytes>`.

**Free text** (status and exception messages, stacks) is masked by the same token
shapes, URL userinfo, `Authorization`/`Cookie`/`Bearer`/`Basic` values and `NAME=value` rules, and
cut to 4 KiB. It comes from jackstay's own words and exit descriptions, never from a child's output, and never from a text that embeds another process's argv (the lock's holder note is reduced to its pid, section 2); a test makes a lock contended by `jst lock -- sshpass -p s3cr3t ssh host` and finds no `s3cr3t` in the journal in either mode.

**The setting**: `telemetry.commands` in `jackstay.json` (section 10) — `masked` (the default: command
text kept with secrets masked, W-056) or `skeleton`. `jackstay.guard.command_sha256` of the unreduced
text is written in both modes, so equal commands stay equal.

## 8. The guard: what it writes and what it may cost

The guard writes **every** call: an allowed Bash call as its command span with the decision event
(outcome `allowed`, the masked command, the Claude ids) — about 0.6 KiB; a refusal, a warning, a
failed-open or a crash in full (rules, warnings, the caller, the stack). Non-Bash calls write a
span with outcome `ignored_tool` and no command. All of it after the answer is on standard
output, in the existing "after" step (bounded by `exitBy`), as the guard's log is written today.

**Budget: +1 ms on the median** of the hook's wall time, measured end to end (process start to
exit, as Claude Code waits for it). The measurement: the built binary run 500 times on a recorded
allowed call and 500 times on a refused one, each with `JACKSTAY_TELEMETRY=off` and on, interleaved,
a fresh `JACKSTAY_HOME`; median and p95 of each. The `just guard-latency` recipe runs it (measured). If the budget is not met,
the order of remedies: (1) build the provider lazily after the answer, so nothing of OpenTelemetry
runs before it; (2) skip the caller's argv on allowed calls (already the design); (3) write allowed
calls without the SDK through the same exporter's line writer.

The guard keeps no log of its own: the span is the guard's only record.

## 9. jackstay's stores stay the source of truth

Receipts, `runs.jsonl`, the review folders and ledgers, the process table (`procs`), the id
counters, the expected list, the gate's marks: unchanged, written first, read by the commands as
today. Spans **observe** them: a span is made after (or around) the write it describes and links
to it by path or id (section 5, the last row). No command reads the journal to decide anything;
`jst log`, `jst stats` and the 7-day window of `jst doctor` (a report, not a decision) are its only readers. A journal deleted whole changes no command's
behaviour (a test of each instrumented command runs it with the journal folder unwritable).

The guard and the process table keep no separate event logs: `jst stats` and `jst log` answer from
the journal, the `jst bg` events `held` and `cleaned` are span events too, and `jst doctor`'s 7-day
window reads the guard's spans (a config error is the span attribute `jackstay.guard.config_error`).
`runs.jsonl` stays: receipts read it.

## 10. Retention, compression, the `telemetry` section of `jackstay.json`

```json
"telemetry": {
  "keep_days": 180,
  "compress_after_days": 14,
  "commands": "masked"
}
```

| key | meaning | default | bounds |
|---|---|---|---|
| `keep_days` | a day file older than this many days is deleted | 180 | 1–3650, and ≥ `compress_after_days` |
| `compress_after_days` | a day file older than this is gzipped to `<date>.otlp.jsonl.gz` | 14 | 1–3650 |
| `commands` | how command text and argv are recorded (section 7): `masked` or `skeleton` | `masked` | one of the two |

Strict like every section (unknown keys refused; `internal/config`'s table). A project without the
section gets the defaults; `.all` always gets the defaults.

**Upkeep** runs in the process that creates a folder's day file (so at most once a day per
project, by whichever jackstay process comes first), after its own write, under a non-blocking lock
`telemetry/.upkeep.lock` (held: skip). It gzips each plain file older than `compress_after_days`
(write `.gz.partial`, fsync, rename, then remove the plain file — a stop in between leaves both,
and the next upkeep removes the plain one only when the `.gz` decodes whole), deletes each file
older than `keep_days`, and stops after 2 seconds of work (the rest waits for tomorrow). The
guard runs upkeep only inside its `exitBy` bound; `jst tidy` is not involved.

Size, *inferred* (a few thousand guard calls a day at ~0.6 KiB, plus the other commands' spans): a
few MB a day before gzip, under 1 GB for 180 days.

## 11. The questions the system must answer (acceptance tests)

Each is a test over a journal produced by the instrumented code (section 13), asserting the
answer, not the formatting. The aim is to find out exactly what happened.

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

```
jst log [--since DUR|TIME] [--until TIME] [--project P|--all-projects] [--session ID]
       [--trace ID] [--tool-call ID] [--command NAME] [--outcome WORD|error]
       [--tree] [--json] [--limit N]
jst stats [REPORT] [--since DUR|TIME] [--until TIME] [--project P|--all-projects]
         [--session ID] [--by version|project|day] [--json] [--claude]
```

- Defaults: `jst log` the last 24 hours of the current project's folder (`--all-projects` reads
  every project's and `.all`); `jst stats` the last 30 days. `--session`, `--trace` and
  `--tool-call` read every project (a session crosses projects). `--since` takes `2h`, `7d`, or an
  RFC 3339 time.
- `jst log` prints one line per command span (time, project, command, outcome, duration, short
  session), indented children with `--tree` (implied by `--trace`), events under their span.
  `--json`: one JSON object per span — jackstay's flattened reading record (section 4), stable keys,
  for `jq`/DuckDB.
- `jst stats` alone lists the reports; `jst stats REPORT` prints a table; `--json` the same as one
  JSON object (`{"report": …, "range": …, "rows": [...], "unreadable_lines": N}`).
- Reports: `guard`, `refusals`, `flaky`, `tests`, `phases`, `reviews`, `background`, `sessions`,
  `load` (section 11). Each is a Go function from spans to rows, unit-tested on its own;
  derived episodes (retry after refusal, flakiness, convergence) are computed at query time, so
  their definitions can improve without touching the journal.
- Exit 0 with an answer (an empty one included), 2 for usage, 1 when no folder could be read.
- The AGENTS block (`internal/adopt/block`) names both commands: an agent reaches for them after a
  surprising failure, before asking what happened.

## 13. Packages and seams

| package | holds |
|---|---|
| `internal/telemetry` (the one shared package) | the provider (`Start` from `cli.Main`, `Finish` with the exit status, panic capture), `Phase`/`Wait`/`Child` helpers that take a `context.Context` or fall back to the command span, `ChildEnv` (TRACEPARENT out), the caller's argv, the `jackstay.*` names |
| `internal/telemetry/journal` | the exporter (conversion, mask hook, OTLP JSON, lock, day files), upkeep, the reader; `journal/otlpcheck` (tests only) holds the pinned proto |
| `internal/telemetry/mask` | section 7: the word classifier over the guard's parser, the free-text shapes, the output sweep (section 16) |
| `internal/telemetry/genai` | section 6 |
| `internal/telemetry/golden` | the golden scenarios of section 6 and their fixtures |
| `internal/telemetry/query` | what `jst log` and `jst stats` share: reading, ranges, clipping |
| `internal/logs` (`jst log`) and `internal/stats` (`jst stats`, nine reports, the transcript reader for `--claude`) | section 12 |
| `internal/cli` | the command span around `entry.Run`; `TRACEPARENT` in and out; `cli.Main` recovers a panic only to record it, then panics again (the crash is unchanged) |
| `internal/supervise` | a child span per `Run`, its `TRACEPARENT` in the child's environment; `EndBy` calls `telemetry.Finish` before re-raising the signal |
| `internal/flock` | a wait span in `Acquire`/`WaitFile` when the lock was not free at once |
| `internal/review` | the attempt spans; the agent span it owns around each Codex run, annotated from the attempt's `events.jsonl` and the run's outcome before it ends |
| `internal/receipts` (`RunLog.Write`) | each run-log line also becomes a span start/end or a verdict event — the seam for `jst test` and the gate, so `internal/testing` knows nothing of telemetry |
| `internal/procs`, `internal/procs/background` | `jst bg` start/end/deadline events, unregistered processes |
| `internal/guard` (hook) | the decision span and the tool-call annotation, in the existing "after" step; stack on a recovered panic |
| `internal/config` | the `telemetry` section's types and bounds |

Dependencies: `go.opentelemetry.io/otel`, `/otel/trace`, `/otel/sdk`, and, for tests only,
`go.opentelemetry.io/proto/otlp` with `google.golang.org/protobuf`. `govulncheck` and `gosec` run
over them in the gate.

## 14. Tests

- **Unit**: the exporter (lines decode as OTLP via `protojson`; ids hex; one line per span; the
  200 ms lock give-up with a held `flock`; a 256 KiB span cut; an unwritable folder changes
  nothing); upkeep with a fake clock (compress, delete, the `.gz.partial` stop, a corrupt `.gz`
  kept with its plain file, the 2 s bound); the mask corpus; `TRACEPARENT` in (valid, invalid,
  unsampled) and out (the child sees its own span); the reader (gz, torn line, a range); each
  report on hand-built spans; `jst log` filters and tree.
- **Golden** (section 6): five scenarios, content and query answers, produced by the guard hook,
  `jst review` with a fake Codex through the real runner, and `cli.Main`.
- **Acceptance** (section 11): A1–A14, each over a journal produced by running the instrumented
  code (in-process, fakes at the edges), asserting the answers (`internal/stats/acceptance_test.go`;
  A3, A4, A8, A10 and A13 also in the golden scenarios, A14 in `internal/telemetry`).
- **Integration** (`//go:build integration`): the built binary — a `jst test -- sleep 30` stopped
  by SIGTERM leaves its command span ended with outcome `stopped` and `error.type` =
  `signal_SIGTERM`, and jackstay still dies by that signal; `jst test -- jst version` makes one
  trace of two command spans and a child span between them; a panic in a command (a test-only
  command registered by the test binary) leaves an `exception` event with a stack; the guard
  with empty input records the caller's argv; two processes writing the same day file at once
  leave whole lines only.
- **No journal changes behaviour**: each instrumented command's existing tests run unchanged with
  `JACKSTAY_HOME` in `t.TempDir()`; one test per command makes the telemetry folder unwritable.
- **Latency** (section 8): `just guard-latency`, outside the gate (timing is not a pass/fail on a
  shared machine); a benchmark of the exporter's write in the gate as a smoke check.

## 16. Implementation details

Where the implementation refines the sections above:

- **No protobuf in the binary** (section 4, step 3): the OTLP JSON is written with jackstay's own
  structs and the mask's patterns are compiled on first use, which keeps the guard within its +1 ms
  budget (measured). Conformance with the
  pinned proto is a test (`otlpcheck`), so the lines are exactly what `protojson` reads and writes.
- **Span names**: `jst <command>`; `test.run` (a run of `jst test`, from the run log's
  `start`/`run` lines; a gate's steps are runs under it) and `test.gate`; `review.attempt` (the
  attempt that becomes a round, or does not); `wait <lock file name>`; `exec <program>`;
  `invoke_agent codex`.
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

**The mask, as it works** (section 7's rules, made exact; each rule is strict rather than precise,
because finding *where* a named secret's value lies in arbitrary shell syntax — option clusters,
glued values, unknown value-taking options, arrays — cannot be done reliably):

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
- **The sweep.** The mask ends with a sweep of its own output: every token of what it is about to
  write that is shaped like a secret (URL user information, a token prefix, a long hex or base64
  run, a PEM marker) is replaced whole, whatever brought it there. The rules before it remove what
  has no shape (a short password after a named option, content in known places); the sweep is the
  guarantee for everything with one.
