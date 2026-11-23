# W-090 — The shape of 0.6.0

Date: 2026-11-20.

One ruling in 6 parts (compatibility, agent chat, landing, overview, sandbox, review); each part has its own context, decision and
consequences.

## Compatibility

0.6.0 stays readable by older pins, and Codex is the default reviewer again.

### Context

The keys added after 0.5.11 kept `"jackstay_schema": 2`. Releases 0.5.10 and 0.5.11 refuse unknown
keys, so a project pinned to one of them failed on "unknown key `$schema`" instead of being told
it needs a newer jackstay. `jst adopt` wrote `$schema` on `main`, about 13 new keys were internal
limits nobody asked for, and `jst review` without `--reviewer` or `review.use` stopped choosing
Codex, while the AGENTS block of 0.5.10 and 0.5.11 tells agents to run plain `jst review`.

### Decision

- The keys added since 0.5.11 and `$schema` need `"jackstay_schema": 3`. With 1 or 2 the file is
  refused naming them, so an older jackstay says it needs a newer one.
- `$schema` and the schema's `$id` point at the release's tag, not `main`. `jst adopt` writes
  `$schema` and other schema-3 keys only when the pin is 0.6.0 or later.
- Internal limits are constants: the chat's sizes, quotas, retention, poll and acknowledgement
  timing, the review pool's retry and retention limits, and the timeout of a test's `requires`
  command. What users touch gets keys: `brief.max_age_hours` and `status.repos`.
- Without `--reviewer` or `review.use`, `jst review` runs Codex, and `jst review check` requires
  it. Where adopt and setup ask about review, Codex is the default choice.

### Consequences

A project moves to schema 3 in steps: the pin first, then the workflows that download it, then the
new keys. Changing a chat or pool limit is a code change. A project that relied on a Claude-only
round passing `jst review check` without `review.use` lists `claude-code` in `review.use`.

## Agent chat

The agent chat in 0.6.0: Claude Code's model, extended.

### Context

The 0.5.x chat (`internal/sessionchat`) kept one store per repository, gave a session a random
identity per start, told Claude Code only how many messages were waiting and never woke an idle
session, let pending messages expire silently, and had no outbox. Its bounds could exceed its own
state file. Claude Code meanwhile ships cross-session messaging of its own: a per-session socket that
wakes an idle session, `SendMessage` with `to`, `summary` and `notify_when_idle`, and the outcomes
delivered, held and refused. The maintainer ruled on the shape of 0.6.0 (this part is about chat; the
others concern compatibility, landing, the overview, the sandbox and review).

### Decision

- **Engines.** Claude Code is the primary engine. Codex is served mainly as the reviewer, talking
  with the author in both directions between review rounds; a live dialogue during a round comes
  later.
- **Scope.** One store per local account. A repository is a channel, and `#common` holds sessions
  outside a repository. An optional list of related repositories limits both the channels and the
  sessions a repository's sessions may write to.
- **Identity.** A session is its engine's session ID (Claude Code's `session_id`, Codex's thread
  ID); a token overrides it. Names are readable and disambiguated with a short ref.
- **Contract.** The chat inherits Claude Code's messaging model (`to`, `message`, `summary`,
  `notify_when_idle`, the outcomes; a message is never consent) and extends it with threads,
  structured requests and responses, receipts and an outbox, expiry with a notice to the sender,
  priority, attachments as references with `meta`, channels with explicit subscription, sender
  status, and system events from jackstay.
- **Delivery to Claude Code.** A layer over Claude Code's own per-session socket, which wakes an
  idle session. Hooks are the fallback: they carry the text, and `Stop` holds the turn while urgent
  messages are unread. Claude Code's channels are opt-in.
- **Setup** registers the MCP server and the hooks in one step, with a diff and a backup.
- **Limits are constants**; `jackstay.json` keeps `chat.enabled` and `chat.related`.

### Consequences

The [chat design](../design/chat.md) gives the contract: the tool and record schemas, bounds that
fit the store, the store on the shared durable package, delivery and its fallback, setup, the test
plan and the decisions on its open questions. The socket's line format is undocumented, so the native layer
is captured from a live session, pinned by a contract test and used only for verified Claude Code
versions. 0.5.x chat state is not imported, and its limit keys are constants that schema 3 refuses as
unknown keys.

### The open questions, decided (W-091)

The maintainer then decided the spec's open questions. The related list limits sending only. A
session hears its own repository's channel by default. No messaging token is presented. Chat is
enabled per repository in `jackstay.json`, never by setup (sessions outside a repository only by
the person's separate choice). A Codex session uses its thread ID when
it can be read, otherwise a launch token. `asyncRewake` waits until after 0.6.0. Delivery is at most
once, and a lost answer is reported as `unconfirmed`. `crossSessionInbound` is not imitated off the
native route. The delivery state machine is modelled formally before code.

## Landing

Landing in 0.6.0: one `jst land` behind a hosting adapter, two autonomy modes.

### Context

0.5.x lands a change in two half-flows, both written directly against GitHub's API. `jst gh
pr-land` merges; the unreleased `jst queue` checks local evidence but never merges, and needs an
approval by someone other than the author. GitLab and Bitbucket are planned for later releases.
The maintainer ruled on the shape of 0.6.0 (this part is about landing, with the
answers to the landing design's open questions given the same day). How thin the landing is, is
[W-092](W-092-thin-land-and-where-tests-run.md).

### Decision

- **One command.** `jst land` replaces `jst gh pr-land`, its `jst gh` neighbours and the queue
  commands, which are removed in 0.6.0 with no aliases.
- **Adapters.** The engine knows no host; a small hosting adapter maps it to one. GitHub ships in
  0.6.0; GitLab and Bitbucket follow.
- **Two autonomy modes**, chosen explicitly at adopt and setup: in the recommended one the agent merges when the
  required checks pass, while `jackstay.json`, `.github/workflows/**` and `.claude/settings.json`
  always need a person, as do any paths a project adds; or the agent prepares and a person merges. A person is a
  change number typed at a terminal, or a merge on the host. Setup keeps the machine's answer as
  the one adopt offers first.
- **The guard** refuses `gh pr merge` and `glab mr merge`.
- **The upgrade** from 0.5.x takes three pull requests: the pin, the workflows, then schema 3.

### Consequences

The [landing design](../design/land.md) gives the policy keys, the adapter, the steps, the judging
of required checks, adopt and doctor. Scripts that call `jst gh` move to `jst land` and
`jst checks`.

## Overview

One `jst status` replaces the timeline, the inventory and the workspace board.

### Context

Three read-only overview commands came in one wave and were never released: `jst timeline`
(delivery stages from the journal), `jst inventory` (worktrees and observed sessions) and
`jst workspace board` (cards, merge queue, gate, receipt and chat across named repositories). They
overlapped, each scanned the journal or Git on its own, and the board read other packages' state
files directly. The owner chose one command (W-090 in the owner's record, "Overview, setup and
docs").

### Decision

- `jst status` is the one overview. With no path it shows this repository in detail: worktrees,
  sessions and delivery events from one journal scan, chat counts, the brief, background jobs, the
  last gate and receipt, active cards. With one path it shows that repository; with several, or
  with none when `status.repos` lists them, one row per repository.
- `--json` prints one envelope (`schema` 1, `kind` `repository` or `board`); `--since` sets the
  window, `--limit` the events kept. Exit 0 complete, 1 partial, 2 a wrong request (an unusable
  `status.repos` included).
- It reads other packages through small public read functions (`receipts.LatestRuns`,
  `contextbrief.SummaryOf`, `background.Jobs`, the chat store's overview), never their files.
- The merge-queue source is not carried over: the local queue leaves with the thin `jst land`
  (W-092), whose host adapter reads the host's own queue.
- The three commands, their packages and their AGENTS-block items are removed; the block gets one
  `jst status` item. As none was released, no alias is kept.

### Consequences

A tool that parsed the old JSON reports moves to the envelope: the timeline's rows are a
repository's `events`, the inventory's lists its `worktrees` and `sessions`, the board's rows the
envelope's `repositories`. The roadmap's board specification is kept as history.

## Sandbox

The agent sandbox writes only what jst needs; evidence stays read-only there.

### Context

`jst setup` added the whole jackstay home to Claude Code's `sandbox.filesystem.allowWrite`
(W-078). The same folder holds what jackstay later trusts: gate marks (`jst test gate --resume`
treats a matching one as a passed step), receipts, review rounds and the run log. With that grant
an agent inside the sandbox could write a gate mark and get a green gate without running it
(#59). The owner chose to grant only the needed subfolders (W-090 in the owner's record).

### Decision

- Setup grants, by reading what jst writes from a sandboxed Bash command: the journals (shared
  and per project), each project's ids, the heavy-suite lock with its queue and holder note,
  `procs`, `chat`, `session-chat`, `context-brief` and the trash. Hooks and stdio MCP servers run outside
  the sandbox and need nothing.
- Receipts and failure notes, gate marks and bars, review rounds, the run log, the delivery record
  and the expected projects are never granted. Granting the negative records (failure notes, bars)
  would not do: write access also deletes them, which re-opens an earlier pass to reuse.
- So `jst test` and `jst review` run outside the sandbox. Every `jst test` that runs something
  writes the run log, and most also receipts or gate marks. Before running, it opens the run log for
  appending and checks it can make a file in the project's receipts, gate marks and bars; it then
  removes the receipt or mark the run would replace. When any of that fails, inside the sandbox or
  under an owner's deny rule, it runs nothing and says to run it outside the sandbox. A run whose
  log line still cannot be written at the end (a full disk) prints its output path instead of
  pointing at `jst test last`. The AGENTS block says so.
- Per-project grants are written for each project folder in the home and each expected project,
  as plain paths (glob support differs between Claude Code's macOS and Linux sandboxes). Doctor
  reports a project whose grants are missing; running setup again adds them.
- Doctor reports `sandbox-writes-evidence` when an allowWrite entry covers evidence. Setup removes
  an entry naming the home itself and leaves any broader entry to the owner.
- The settings file keeps its key order (the ordered model adopt uses), a backup beside it, and
  the plan's diff shows only the entries removed and added. A linked settings file or shell
  profile is named in the error and left alone.

### Consequences

An agent in the sandbox can no longer forge evidence, and must ask to run every `jst test` that
runs something (raw, a scope, the gate) and `jst review` outside it. `jst test` checks the run log
and the project's receipts, gate marks and bars where they are, so an owner's deny rule on one of
them stops it too. A new project's journal and ids need one more `jst setup` before sandboxed commands
write them; until then the journal loses those spans and `jst id next` fails, and doctor says so.

## Review

0.6.0 review: one command in the background, only missing answers paid again, one access profile.

### Context

The 0.5.x reviewer pool capped the machine at one review, needed a person to run its worker, kept
an event store of its own and replayed a whole paid review, up to five times, after a crash. Pool
and review spoke through a marker on standard error. Reviewers were held tighter than their task
needs: Codex read only the source view, with no network and a `PATH` of `/usr/bin:/bin`. The
comparison of reviewers rested on one concurrent run per arm. The owner's words: "the reviewer is
not an enemy; efficiency comes first, everything else is insurance against accidents."

### Decision

- The pool is folded into `jst review`: `--background` (and the chat's `review.submit`) runs the
  round under `jst bg`, as many at once as `review.max_concurrent` allows, and its result reaches
  the sessions that asked as a `review.finished` chat event.
- After a crash or a failed reviewer, only the reviewers whose answer is missing run again. A
  saved answer is reused when its attempt asked the same question of the same reviewer settings:
  the round, the commits, the prompt, the schema, the review policy, and each reviewer's command,
  model, effort and isolation (not its deadline: a retry with a longer one reuses what was answered).
- Codex and Claude Code get one access profile: everything readable, read-only, with the network,
  the user's `PATH`, proxy, CA and provider settings; closed only are writes outside the reviewer's
  own temporary folder, the account's secrets and token-like variables other than the provider's
  own login, and the user's MCP servers, hooks and rules. A project can give chosen MCP servers
  back. Claude Code, like Codex, refuses to start inside an agent's sandbox (exit 6).
- The wrapper's worth is measured before the release: seeded defects, at least five sequential runs
  per arm, recall and precision, time per stage, model and cost (`just review-bench`).

### Consequences

The four decisions land as separate changes, in the order above; each holds from its own change on.

`jst review pool` and the `review.pool` key are gone; there was no release with them. A review run
in the background is listed and stopped with `jst procs`. A changed model, effort, command or
isolation pays every reviewer again. Reviewers may now reach the network and read outside the view,
so instructions planted in a reviewed change could send out what a reviewer can read: the risk is
accepted for efficiency, and narrowed by denying the credentials by path and by name (the other
reviewer's login included) and leaving `.env` contents out of the patches. Both reviewers deny `.env`
files by a pattern on macOS; on Linux Codex denies none until its rule is redesigned.
