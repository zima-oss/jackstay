# `jst review`: cold code reviews

Status: built ([W-051](../decisions/W-051-jst-review-codex-rounds.md)). It follows
[W-019](../decisions/W-019-repeated-findings-mean-redesign.md) (a place failing two rounds running
is redesigned), [W-048](../decisions/W-048-review-findings-triaged-by-realism.md) (findings rated by
likelihood and consequence), W-036 and
[W-037](../decisions/W-037-current-go-and-dependencies.md) (Go design, current versions) and
[W-013](../decisions/W-013-quality-testing-declarative.md) (tests). How to run a review is the
[review guide](../guide/review.md).

## What it removes

A review built by hand fails in known ways, each removed here: a prompt left on an open stdin
(section 3.4), Codex in Claude's sandbox losing its login (section 2), a prompt in the sandbox's
`$TMPDIR` (section 5), a tree changing under the reviewer (section 3.1), an ambiguous verdict line
(section 3.5), a prompt too long for argv (section 6).

## 1. The `review` section of `jackstay.json`

Every key is optional. The reviewer is `--reviewer`, else the project's `review.use`, else Codex.
Both providers' settings are available with or without a `review` section. An explicitly empty
`review.use` with no `--reviewer` exits 2 before starting a round.
Unknown keys and a
key written twice are refused, as everywhere in `jackstay.json` (`internal/config`, strict). Paths are
relative to the repository's root and read **from the reviewed commit's tree** (section 3.1), never
the live checkout, so a brief edited after the commit cannot change what was reviewed; `--brief`
on the command line is the exception (a file outside the tree, copied into the round's folder).

| Key | Meaning | Default |
|---|---|---|
| `reviewers` | overrides for built-in `codex` and `claude-code` settings, by name | built-in settings for both |
| `reviewers.codex.command` | the Codex program: a name looked up on `PATH`, or an absolute path | `codex` |
| `reviewers.codex.model` | passed as `-m`; empty means Codex's own default (with `--ignore-user-config` that is Codex's built-in one, not the user's `config.toml`) | empty |
| `reviewers.codex.effort` | reasoning effort, passed as `-c model_reasoning_effort="…"`; one of `minimal`, `low`, `medium`, `high`, `xhigh`; empty passes nothing | `medium` |
| `reviewers.codex.timeout_minutes` | the hard deadline of one run (section 3.4); 1–120 | 20 |
| `reviewers.codex.isolation` | the isolation flags, a list of argv words that **replaces** the default list whole. Words jackstay owns are refused here (below) | `--ephemeral`, `--ignore-user-config`, `--ignore-rules`, `--skip-git-repo-check`, and `-c` `mcp_servers={}`, `features.apps=false` |
| `reviewers.<name>.mcp_servers` | MCP servers given back to that reviewer, by name: `{"command": "…", "args": […]}`; the user's own stay closed (section 3.4). Needs `"jackstay_schema": 3` | none |
| `use` | the built-in reviewers a round runs when `--reviewer` is absent, and the reviewers required by `review check`; names must be unique | `["codex"]` |
| `brief` | the project's standing brief: what a reviewer of this project must know (its threat model, what to ignore). Put at the top of every prompt | none |
| `template` | a whole prompt template (Go `text/template`, section 3.2) replacing jackstay's own; for a project whose reviews differ in kind. Its output must still ask for the verdict form, which jackstay appends itself whatever the template says | jackstay's embedded template |
| `cite` | files and folders (a trailing `/`) the reviewer is told to read as the measure: rules, decisions, specs. Each must exist in the reviewed tree, or the round refuses to start | `["AGENTS.md"]` if it exists, else none |
| `severities` | the severity words a finding may carry, most severe first | `["blocker", "major", "minor"]` |
| `blocking` | which of `severities` fail a round | `["blocker"]` |
| `triage` | W-048 on: each finding carries likelihood and consequence and jackstay sorts realistic from rare (section 4). Off: every finding of a blocking severity blocks | `true` |
| `repeat_rounds` | W-019: how many rounds running a place must draw a finding (section 4) before jackstay says it is to be redesigned; at least 2 | 2 |
| `repeat_severities` | which severities count for W-019's repeats, rare findings included (W-048) | every one but the last of `severities` (`blocker`, `major`) |
| `queue_minutes` | one admission budget for a source view, reviewer slots and the start lock (section 6); 1–120 | 15 |
| `round_limit` | the round after which jackstay stops and the owner is asked (section 4); at least 2 | 6 |
| `max_concurrent` | how many reviewer runs this project's reviews may hold at once on the machine (section 6); 1–16 | 4 |
| `store` | where results are kept: an absolute path or one beginning `~/`; empty means `~/.jackstay/<project>/reviews` | empty |
| `keep_days` | a task whose newest round is older than this is removed (section 5); 1–3650 | 30 |

**Words jackstay owns** and refuses in Codex `isolation`, because each would break a promise of this spec:
`exec`, `-a`/`--ask-for-approval`, `-s`/`--sandbox`, `--dangerously-bypass-approvals-and-sandbox`,
`--full-auto`, `--approve-for-me`, `--add-dir`, `-C`/`--cd`, `-o`/`--output-last-message`,
`--output-schema`, `--json`, `-m`/`--model`, `-p`/`--profile`, `--worktree`, `-i`/`--image`, `-`,
any word that is not an option (a prompt), and `-c` values whose key is `sandbox_mode`,
`sandbox_permissions`, `approval_policy`, `model`, `model_reasoning_effort`, `default_permissions`,
`permissions`, keys below `permissions.*`, `features.network_proxy` or `project_doc_max_bytes`.
The argv jackstay runs is
always (section 3.4):

    <command> -a never exec --json -C <view> -o <answer> --output-schema <schema>
              [-m <model>] [-c model_reasoning_effort="<effort>"] <isolation…> <pinned permissions> -

The section's types go into `config.Table`; its meaning (the refused words, ranges, known names in `use`, `blocking`
⊆ `severities`, `cite` present in the tree) is `internal/review`'s validator, which `jst check` runs
as it runs the release's.

### 1.1. Claude Code adapter

Selected by `--reviewer claude-code` or `review.use`; overridden by `reviewers.claude-code`. Defaults:
command `claude`, the CLI's model and effort, a 20-minute timeout. Efforts: `low`, `medium`, `high`,
`xhigh`, `max`, `ultracode`. Its safety flags are fixed: a nonempty `isolation` is refused.

Jackstay verifies `claude --version` locally and requires 2.1.248 or newer before the run. It
starts the CLI in the pinned source view with `--print --restricted --safe-mode
--no-session-persistence --add-dir /`. The built-in tool allowlist is `Read,Glob,Grep,WebFetch,WebSearch`, the permission mode is
`dontAsk`, and MCPs are disabled by a strict empty configuration and a tool denial; section 3.4
gives the whole access profile it shares with Codex. Like Codex, it runs outside an agent's
sandbox, and jackstay checks that first: when Claude Code's state file (`$CLAUDE_CONFIG_DIR/.claude.json`,
else `~/.claude.json`) exists and cannot be opened for writing, the round stops with exit 6 before
anything starts ("run `jst review` outside the sandbox"). The
CLI consumes the prompt through stdin and uses existing CLI credentials. Managed policies and
managed hooks still apply on machines where administrators configure them.
[Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference)

Claude Code gets the same verdict JSON Schema via `--json-schema` and returns a JSON envelope via
`--output-format json`. Jackstay accepts `structured_output` only in a successful result,
writes it as the round's answer, through the same validation and ledger as Codex (token counts in
a second file; raw output not recorded). The start lock is released when it finishes or after 120 s,
since the envelope arrives only at the end. Both reviewers read the source view of section 3.1.
[Claude Code headless mode](https://code.claude.com/docs/en/headless)

## 2. Codex runs outside the sandbox, and its login is refreshed by one run at a time

The session runs `jst review` outside Claude's sandbox, and jackstay starts Codex itself; it never
tries to escape a sandbox it is in.

**Why the login matters.** Codex keeps its login in `$CODEX_HOME/auth.json` (`~/.codex` by default)
and refreshes it by writing new tokens back. A refresh where the file cannot be written (inside the
sandbox), or two at once, can spend the old refresh token and lose the new one, and with it the
login for every session (inferred). Codex refreshes rarely and keeps its own `auth.lock` (inferred),
so jackstay's part is small:

1. **Refuse inside a sandbox.** Before anything is started, jackstay opens `$CODEX_HOME/auth.json` for
   writing (`O_WRONLY|O_APPEND`, nothing written, closed at once; its time does not change). If that
   is refused (`EPERM`/`EACCES`) jackstay stops with exit 6: "Codex cannot save its login here — run
   `jst review` outside the sandbox". A missing `auth.json` stops with exit 6 too ("run `codex
   login`"); an API key in the environment is not used, because the environment is stripped
   (section 3.4).
2. **Serialise starts on the machine.** A lock file `~/.jackstay/.all/reviews/start.lock`, taken with
   `flock` (released by the kernel if jackstay dies), is held from just before Codex is started until its
   first event that needs the model to have answered — the first `item.started`, `item.completed`,
   `turn.completed`, `turn.failed` or `error` on its `--json` stream — or until Codex exits, or for
   at most 120 s, so a refresh at start is made by one run at a time on the machine.
3. **Stop the cascade.** A run that ends with no answer and whose failure is Codex's own
   authentication failure writes
   `~/.jackstay/.all/reviews/login-failed` (the time and the message). Every later `jst review` refuses with
   exit 6 while that file is newer than `auth.json`; the user's `codex login` rewrites `auth.json`
   and so lifts it. "Codex's own failure" is read only where Codex reports about itself: the
   message of a top-level `error` event or of `turn.failed`, and Codex's log lines on standard
   error (`ERROR`/`WARN` lines of its own modules) — never the content of an `item` (the model's
   text, a command's output), which in a review of login code is full of "401" and "refresh
   token". The patterns are a short table in the code (an HTTP 401 from the provider, "refresh
   token", "not logged in", "log in again"), each with a test from a recorded failure.

The lock does not cover a Codex run by hand or by another tool; Codex's `auth.lock` does
(inferred).

## 3. A round

### 3.1 The tree is pinned

`jst review [REV]` reviews one commit (default `HEAD`; its base is its first parent), or a range
`BASE..REV` (the commits after `BASE` up to `REV`). `REV` and `BASE` are each resolved once to a
full commit id (`git rev-parse --verify <name>^{commit}`); the prompt, the source view and
the stored record use only those ids, so a branch moved by another session during the run changes
nothing. **A chain has one base** (section 4): its first round fixes it, and a later round of the
chain takes no other — `BASE` may be left out there (the chain's base is used) and, if given, must
resolve to the chain's base, else the round refuses (exit 2, `this chain reviews from <base>;
--fresh starts a full review from a new one`).
Uncommitted changes in the session's checkout are never reviewed; jackstay says so on standard error when
the checkout has any (`git status --porcelain`), and goes on.

Jackstay creates one temporary source view for each attempt by reading the pinned Git objects. The
view omits repository metadata and uses raw blob contents. Symbolic links contain the target as
plain text, and gitlinks contain the recorded submodule object ID. The checkout, local hooks and
checkout filters supply no files to the view. The provider can read these paths:

| Path | Contents |
|---|---|
| `head/` and `base/` | files at the reviewed revision and the chain's base |
| `previous/` | files at the previous revision in an incremental round |
| `changes/base.paths` | JSON Lines with the status and applicable old and new paths, including renames and deletions |
| `changes/base.patch` | the patch from the base to the reviewed revision |
| `changes/previous.paths` and `changes/previous.patch` | the changed paths and patch from the previous revision |
| `review-brief.md` | the optional copy of the operator's `--brief` |
| `open-findings.json` | the complete open findings, when a prior round has any |

The view is removed after the reviewers stop, before the round is published; the next run of the
task reclaims abandoned views under the task lock, and pruning reclaims expired tasks and legacy
locked review worktrees. Projection is offline: the pinned objects must be local, and a missing
object or a path collision on the host's file system is a review failure. Change files use the
pinned head's attributes (a local `info/attributes` can still override them).

### 3.2 The prompt

The initial prompt includes pinned IDs, source paths, review instructions, open finding references and the
verdict form. Configured briefs and cited files are read on demand. The configured brief and template
contents belong to the chain policy. Editing either file requires a fresh full round. The default template is embedded
in the binary. A project's `template` replaces it and receives the same fields (`Brief`,
`Commit`, `Subject`, `Base`, `DiffStat`, `Tree`, `Cite`, `Round`, `Previous`, `OpenFindings`,
`Triage`, `Severities`, `Blocking`, `Focus`). `Brief` holds path references, `DiffStat` names the
changed-path index, and `Tree` is the relative `head` path. `OpenFindings` retains finding
metadata. The original titles and details are available in `open-findings.json`. Jackstay appends source navigation and
the verdict form to every template.

**Delta rounds.** Round N > 1 follows round N−1 unless `--fresh` starts a new chain. Reviewers use
`changes/previous.paths` and `changes/previous.patch` for the delta and keep the whole range
available for context. They reassess every open finding using the ledger ID and exact `place`
from the prompt. `--focus TEXT` tells reviewers where to look hardest while preserving that scope.
The attempt's `prompt.md` records the initial prompt. The provider learns source contents when the
reviewer requests them through its file tools.

### 3.3 Several reviewers

`--reviewer codex` (default: `use`). Each reviewer of a round gets the same prompt, the shared source view
and its own result folder, and runs concurrently with the others within the machine's cap (section 6). The
round's report merges their findings by `place`.

### 3.4 The run

- **The reviewer's access profile** ([W-090](../decisions/W-090-the-shape-of-0-6-0.md#review)), the same for Codex and Claude Code (`internal/review/access`).
  The reviewer's task is to read and report, so it is given everything it may need to read, and
  denied only what it clearly does not need:
  - **reads everywhere**, read-only. Codex: a permission profile `":root"="read"`. Claude Code:
    `--add-dir /` with the tools `Read`, `Glob`, `Grep`, `WebFetch`, `WebSearch` only (no tool
    that writes or runs a command).
  - **writes only its own temporary folder**, `<attempt>/<reviewer>/tmp`, which is its `TMPDIR`
    (Codex: the one `"write"` entry of the profile; Claude Code has no writing tool).
  - **the network is open** (Codex: `network.enabled=true`; Claude Code: its web tools).
  - **the environment** is the caller's whole one — the user's full `PATH`, locale, proxy
    variables (with any credentials in their URL), CA bundles, provider settings — less: the calling
    session's and jackstay's own variables (`CLAUDE*`, `JACKSTAY_*`, `OMC_*`, `GIT_*`,
    `SSH_AUTH_SOCK`, `SANDBOX_*`), and every variable whose name looks like a credential
    (`TOKEN`, `SECRET`, `PASSWORD`, `API_KEY`, `ACCESS_KEY`, `PRIVATE_KEY`, `CREDENTIAL`, `COOKIE`, `AUTH`…)
    except the reviewer's own provider's login: `OPENAI_*`, `AZURE_OPENAI_*`, `CODEX_API_KEY` for
    Codex; `ANTHROPIC_*`, `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CONFIG_DIR` and the Bedrock or Vertex
    credentials when that is its provider, for Claude Code. Codex's shell inherits it
    (`shell_environment_policy.inherit="all"`, with Codex's own default exclusion of key-like
    names kept); jackstay sets it before the project's isolation flags, so a project's own
    `shell_environment_policy` wins.
  - **secrets are not read**: `~/.ssh`, `~/.aws`, `~/.azure`, `~/.gnupg`, `~/.kube`,
    `~/.docker/config.json`, `~/.netrc`, `~/.npmrc`, `~/.pypirc`, `~/.config/gh`, `~/.config/gcloud`,
    `~/.config/hub`, `~/.config/op`, `~/.git-credentials`, `~/.password-store`, `~/.vault-token`,
    `~/.terraform.d/credentials.tfrc.json`, `~/.cargo/credentials.toml`, the keychain
    (`~/Library/Keychains`) or keyring (`~/.local/share/keyrings`); **the other reviewer's login and
    state** (Claude Code is denied `$CODEX_HOME`, else `~/.codex`; Codex is denied `~/.claude`,
    `~/.claude.json` and `$CLAUDE_CONFIG_DIR`); and `.env` / `.env.*` files. Their contents are
    also left out of the generated patches (`changes/*.patch`); the change index still names them.
    Claude Code: `Read`, `Glob` and `Grep` deny rules for each path and for `//**/.env` and
    `//**/.env.*` (anchored at the filesystem's root, not at its working folder), through
    `--settings`. Codex on macOS: each path `"none"` in the profile, and the globs `"/**/.env"` and
    `"/**/.env.*"` `"none"`, checked at every read, links included. **Codex on Linux denies no
    `.env` file**: its sandbox expands a glob before the run, takes regular files only, refuses one
    rooted at `/` and stops every sandboxed command when its scan meets a folder it cannot read
    (Codex 0.169.0's Linux sandbox source), three designs were tried and none could be verified
    here; so on Linux the reviewer keeps the read-only profile and the credential-path denials,
    and `.env` files stay readable to it (the redesign is an issue of its own). Not run on Linux.
  - **the user's MCP servers, hooks and rules are off**: Codex `--ignore-user-config`,
    `--ignore-rules`, `mcp_servers={}`; Claude Code `--restricted --safe-mode` (no user, project or
    local settings, no hooks, no plugins), `--strict-mcp-config` with an empty `--mcp-config`,
    `disableAllHooks`. `review.reviewers.<name>.mcp_servers` gives chosen servers back, and their
    tools only. Their definitions may hold a credential, so `run.json` and `round.json` keep every
    MCP setting of the argv — jackstay's, or a project's in its isolation flags, whole table or one
    key — by key only (a reused run's record too), and so does the argv kept in telemetry.
  Checked against Codex 0.169.0 and Claude Code 2.1.328 on macOS (2026-11-20): both were denied
  `~/.ssh`, the untracked `.env` of the checkout and of a checkout beside it, and the other
  reviewer's login, listed `/usr/share`, and fetched a public web page; Codex's shell had the user's full `PATH` and no `GH_TOKEN` set in the
  caller, and wrote nothing outside its folder.
- **argv** (Codex): as in section 1, then the access profile as `-c` permission settings,
  `-a never`, `--strict-config` and `project_doc_max_bytes=0` (repository instructions are read
  from the pinned view on demand). Codex CLI 0.169.0 or newer is required.
  [Inside the Codex sandbox](https://openai.com/index/inside-the-codex-sandbox/)
- **stdin**: the prompt is written to Codex's standard input from memory, which is then closed;
  the argv's last word is `-`. No run ever has an open terminal or an inherited stdin.
- **working folder**: the tree (`-C` and the process's own).
- **outputs**: `-o attempt-<k>/<reviewer>/answer.json` (the last message alone); standard output (the
  `--json` events) to `events.jsonl` and standard error to `stderr.txt`, both in that folder,
  each capped at 64 MiB (past the cap writing stops, noted; the run goes on).
- **deadline**: `timeout_minutes`; at it, or on TERM/INT to jackstay, Codex's process group gets
  TERM, then KILL after 5 s. Past the deadline the outcome is `timeout`.
- **probe**: `<command> --version` is run first (10 s deadline) and its answer recorded; a missing
  program is exit 6, "Codex is not installed".

### 3.5 The verdict

Codex is given a JSON Schema (`--output-schema attempt-<k>/<reviewer>/schema.json`, written from the
embedded one with this project's severity words), so its last message is one JSON object:

```json
{
  "verdict": "changes",
  "summary": "one paragraph",
  "findings": [
    {
      "id": "F1",
      "place": "internal/review/run.go: startLock",
      "severity": "blocker",
      "likelihood": "ordinary",
      "consequence": "severe",
      "title": "one line",
      "detail": "what, where, why, and the smallest fix",
      "ordinary_path": "",
      "repeats": ["r2-codex-F3"]
    }
  ],
  "prior": [{"id": "r2-codex-F3", "status": "open"}]
}
```

`verdict` is `approve`, `changes` (a new or still-open finding) or `incomplete` (the reason in
`summary`, the ledger unchanged). `severity` is one of `severities`; W-048's axes are `likelihood`
`ordinary` (happens by accident) or `unusual` (needs a hand-built setup or deliberate misuse), and `consequence`
`severe` (lost work or data, an unnoticed wrong decision) or `mild`. `repeats` names ledger
findings this one continues; `ordinary_path` names, for an `unusual` finding, the ordinary work by
which its setup arises by accident (empty when none does, and for an `ordinary` finding); `prior`
answers every open finding, `fixed` or `open`. A transient local disk error in an ordinary call is
rare (the error need only reach the caller); a process killed between two writes is `ordinary`, a
finding when recovery leaves the state wrong or a message lost or doubled (W-094). A finding that
only asks to cover more exotic cases (`unusual`, with an empty `ordinary_path`) never takes a
blocking severity (W-096). The schema
sets `additionalProperties: false` and every member required (`[]` where empty).

jackstay reads `answer.json` with `internal/strictjson` (one value, no member twice, no unknown member, 1
MiB at most) into a Go type, then checks it: ids unique; severities, likelihoods and consequences
from the lists; `repeats` and `prior` naming only findings of the ledger; every open finding of
the ledger answered in `prior`, once. The outcome is computed (section 4), not taken from `verdict`;
a disagreement is recorded in the report.

**Outcomes** of a reviewer's run, each named in the report and the index:

| outcome | when | exit (section 7) |
|---|---|---|
| `done` | an answer read and checked | 0, 1, 3 or 4 by section 4 |
| `timeout` | past `timeout_minutes` | 5 |
| `failed` | Codex exited non-zero, or a `turn.failed`/`error` event, with no answer | 5 |
| `login` | as `failed`, and the failure is about authentication (section 2, item 3) | 6 |
| `no-answer` | Codex exited 0 and `answer.json` is missing or empty | 5 |
| `incomplete` | the reviewer reports why required evidence or coverage was unavailable | 5 |
| `unparsable` | `answer.json` is not one JSON object of the schema (the strict reader's error kept) | 5 |
| `inconsistent` | read, but a check above fails (which one kept) | 5 |

A run that ends in anything but `done` is not retried, and takes no round number; the next run
of the task reuses N and the answers the other reviewers gave (section 4).

## 4. Rounds, triage and W-019

A **task** is what is reviewed in rounds: by default the session's current branch name, else
`--task NAME` (required on a detached head); a name is any text git accepts as a branch name, at
most 200 bytes. Its folder is the lower-case SHA-256 of the name, with `task.json` holding the name
(case variants stay distinct); a legacy percent-encoded folder stays readable and writable.

**One run of a task at a time.** A run takes the task's lock, `<store>/.locks/v2/<task hash>.lock`
(`flock`, outside the task's folder), before it reads the round number, until its report and index
line are written. A second run of the task refuses at once (exit 2, "task busy", naming the
holder). Pruning skips a task whose lock it cannot take. A writer of a legacy folder also takes its
old lock.

**Attempts and rounds.** Every run is an *attempt*, written from its first byte into
`<task>/attempt-<k>/` (k never reused). When every reviewer ended `done`, jackstay writes
`round.json` into it and, last, renames it to `<task>/round-<N>/`: only that rename takes round N, so
a killed run leaves a failed attempt and the next run still takes N. An attempt where some reviewers
failed is not a round (exit 5); its successful answers stay readable.

**Only the missing answers are paid for again** ([W-090](../decisions/W-090-the-shape-of-0-6-0.md#review)). `attempt.json` records the attempt's
**binding**: a digest of the round number, the task, the commits (base, head, previous), the review
policy and content policy, each selected reviewer's settings (command, model, effort, isolation; not its deadline),
and the digests of the prompt, the verdict schema and the `--brief` file. The prompt holds the open ledger and `--focus`, so one binding is one question. Before
starting reviewers, a run looks through the task's earlier attempts (never rounds), newest first,
for one with its own binding; a reviewer's `answer.json` there is reused when it reads and checks as a
fresh answer would (section 3.5), is not `incomplete`, and was given with the same model and
effort, its `run.json` saying `done` — or missing, when jackstay was killed after the answer was
written. The answer is copied into the new attempt, its run marked `reused` with the attempt it
came from (the report says so), and only the other reviewers run. When every answer is found, no
source view is built and no reviewer starts. Anything else that changed (a new commit, a round
taken meanwhile, a new focus, model or policy) is another binding, and every reviewer runs.

**Chains and the ledger.** A task's rounds form chains: round 1 starts one, `--fresh` starts
another (a full review with an empty ledger), every other round continues the previous one's chain
from the same base (3.1). So a chain at commit C has reviewed every change from its base to C: the
first round the whole range, each later one the tree difference since the round before. A chain
keeps one review policy and one set of reviewer names; a change to either, or a round without a
policy ID, needs `--fresh`.

The **ledger** of a chain is jackstay's list of its findings, computed from the rounds' answers and kept
whole in each `round.json` (section 5): every field the reviewer gave, plus the **ledger id** jackstay
assigns, `r<round>-<reviewer>-<answer id>` (`r2-codex-F3`), unique in the chain whatever ids the
answers reuse. The prompt shows ledger ids; `prior` and `repeats` must use them, and an answer that
names any other is `inconsistent`. A finding enters the ledger in the round that reports it:
- **open** if it is not rare (every finding, with `triage` off), until a later round answers
  `fixed` in `prior`. Every round answers the whole open ledger, so nothing leaves it by being
  skipped, by a focus, or by an empty `findings`.
- **deferred** if it is rare: kept with its history, listed for the project's later card, not put to
  later rounds. Found again later, it is a new finding of that round.

**Triage and blocking** (W-048). With `triage` on, a finding is **realistic** when `likelihood` is
`ordinary` or `consequence` is `severe` with an `ordinary_path`, **rare** when it is `unusual` and
either `mild` or without an `ordinary_path`. One predicate
says whether a finding blocks: its severity is in `blocking` and, with `triage` on, it is realistic
(with `triage` off, the severity alone). A round **blocks** when any *open* finding of its ledger
blocks after the round, found or answered `open` alike. Rare findings never block; the report lists
them with a ready row for the project's later-findings card. An `unusual` finding is realistic only
when its `ordinary_path` names the ordinary work by which that setup arises by accident
([W-094](../decisions/W-094-unusual-severe-findings-need-an-ordinary-path.md)): with it empty, a
`severe` one is rare too, deferred and never blocking. The session checks the ratings, the path
included, before it calls a finding a blocker or asks for a round past the limit; the next step
`jst review` prints for a blocking round says so.

**Places.** A place is the finding's `place` string, trimmed, with runs of spaces made one and
compared without regard to case. The prompt lists prior places and asks for the exact string again
when a finding is about the same place; a finding that names a prior one in `repeats` takes that
one's place whatever it wrote. Places are merged across the reviewers of a round.

**W-019, in the tool.** A round **hits** a place when, in that round, the place has a finding of a
`repeat_severities` severity that the round found or answered `open`, realistic or rare alike. When
a place is hit in `repeat_rounds` rounds running of one chain, jackstay says it is **to be
redesigned, not patched**, names it with the rounds and findings, and exits 3; the round is
**redesign-due** until a later round does not hit that place. The default leaves out `minor`, since
W-019 is about defects.

**The owner is asked** when round `round_limit` ends and still blocks: exit 4, and the report's
first line says the owner decides. A further round is refused (exit 4, nothing run) unless
`--past-limit` is given, after the owner's recorded word. Exit 3 wins over 4. After a redesign the
next round is ordinary, usually with `--focus` on the redesigned place.

## 5. Storage

```
<store>/                                  ~/.jackstay/<project>/reviews unless `store` says otherwise
  index.jsonl                             one line per reviewer run
  .locks/v2/<task hash>.lock              the task's lock (section 4)
  .tasks/<task hash>/                     a new task's folder
    task.json                            the original task name
    round-1/                              an attempt that became round 1 (renamed last)
      attempt.json  report.md  round.json  prompt.md
      codex/  answer.json  events.jsonl  stderr.txt  schema.json  run.json
    attempt-2/ …                          an attempt that did not become a round
      source/                            temporary pinned view while a review runs
  .trees/                                 legacy locked worktrees, if any
  .requests/<task hash>.json              the task's review running in the background (section 6)
  .requests/<task hash>-<id>.log          its output
  .ends/<task hash>-<id>.json             how a background request ended, and which sessions were told
```

`attempt.json` is written first, so a failed attempt keeps what it was for (task, attempt, commit,
base). `round.json` is the round's record: task, round, chain, commit, base, the round it follows,
reviewers, the versioned review policy ID, the ledger after the round (each finding whole — ledger id, reviewer, place, severity,
ratings, title, detail, `repeats` — with its status `open`, `fixed` or `deferred`, the round and
commit that opened it and the round that fixed it), the places hit, `blocks`, `redesign_due`,
`over_limit`. Later rounds read the ledger from the newest round's `round.json`, never from the
index.

`<project>` is jackstay's marker name of the project (`internal/homedir`). Folders are 0700, files 0600,
every file written whole (a temporary file in the same folder, synced, renamed).

`report.md` is for people and sessions: a header (task, round, commit, base, the round it follows,
reviewers with model, effort and Codex version, duration, outcome), one line saying whether the
round blocks and why, the new realistic findings by place (each with reviewer, id, severity, the
two ratings, title, detail), the ledger's open and newly fixed findings, W-019 places, the rare findings
with their later-card rows, and each reviewer's summary. `run.json` is the machine's copy of one
run: argv, Codex version, model, effort, start, end, exit status, outcome and its reason, the
answer's checks.

`index.jsonl` holds per run `at`, `task`, `round` (or `null`), `reviewer`, `commit`, `base`,
`outcome`, `blocks`, counts of realistic and rare findings, `seconds`, and the path. Every write of
it (an append, pruning's rewrite, a rebuild) holds `<store>/index.lock` (`flock`, never replaced)
from open to close. It is a convenience for `jst review list`, rebuilt from the folders by
`--rebuild` (an unreadable line is skipped with a warning).

**Pruning**, at the start of every `jst review` and by `jst review prune`, removes whole each task
whose newest round or attempt is older than `keep_days` and rewrites the index under its lock. A
task's rounds are kept together, so W-019's count never loses a round in a chain.

## 6. In the background, and several at once

A session runs a review under `jst bg` ([implementation.md](implementation.md) section 5.1;
the [review guide](../guide/review.md) shows the line) and reads `jst review last`. `jst review` is
a plain foreground command with **one budget**: `queue_minutes` (source admission, reviewer slots
and the start lock together) plus the longest `timeout_minutes` plus 2. It prints the budget first;
the documented `--for` is it rounded up (37 with the defaults: 15 + 20 + 2), `jst bg`'s deadline
being only the backstop.

**The machine's cap.** Source views and reviewer runs each take machine-wide slots, with the same
ceiling: a project whose `max_concurrent` is M holds only slot k < M of
`~/.jackstay/.all/reviews/slots/<k>.lock` (k = 0…15, `flock`). A view holds its slot through
cleanup. A run tries the slots in order and waits (saying so every minute); if a slot and then the
start lock (section 2) are not both held within `queue_minutes`, it ends `failed` ("no slot") and
Codex never starts. An error opening a lock fails at once. A round's reviewers run concurrently,
each returning a result value; none outlives the call.

**In the background** (W-090; the 0.5.x reviewer pool,
its store, worker and nine subcommands, folded into `jst review`). `jst review … --background`, and
the chat's `review.submit`, resolve the commits now and, under the lock of the task's request file
`<store>/.requests/<task hash>.json`, write the request (id, task, worktree, repository, commits,
reviewers, `--fresh`, `--past-limit`, `--focus`, the chat sessions to tell); then start, detached in
a session of its own, `jst bg --for <budget + 1> --name review-<task> -- jst review --task T
--reviewer R --request ID -- BASE..REV`, and wait at most 10 s for it to take the task's lock, so
`jst bg` finds its session among its parents. The `jst bg` entry is the pending run's record
(`jst procs`). There is no worker: reviews of different tasks run at once, bounded by the slots.
- **One request per task.** While its process lives (id and start in the process table; without a
  table, until its deadline), an equal submission (same repository, worktree, task, commits,
  reviewers, flags) joins it (at most 32 sessions) and a different one is refused (exit 2, task
  busy).
- **One end per request.** A request ends exactly once, in `<store>/.ends/<task hash>-<id>.json`,
  written under the request's lock before the request is removed: the request with its sessions,
  the outcome (status, word, next step cut to 2 KiB, round, report folder) or *lost*, and which
  sessions have been told. It ends when its run finishes; when its launch fails (sessions that
  joined meanwhile included); or, lost, when its process is found gone (by a new submission, by
  `review.status`, or when any session's chat looks for what it is owed). A request with an end is
  over: it cannot be joined, and a crash between the end and the removal tells the saved outcome.
- **Telling the sessions.** Each session's `review.finished` event is made from the end, keyed by
  request and session, and given to the chat's publisher (`internal/chatevent`,
  [chat.md](chat.md) section 10); each one stored is recorded in the end, the rest pulled at that
  session's next inbox read or hook. It names a round and report only while the folder still holds
  that request's attempt. Older fully told ends of the same repository are removed.
- **Status.** `review.status` answers for the asking repository (Git common folder and its inode)
  only: its running request and how its latest ended. It reads nothing of the rounds kept by
  project, which clones of one project share. The chat checks that the review's repository is the
  one its session registered in.

## 7. Commands

`jst review [REV | BASE..REV]` runs a round; `last` prints the newest round's report or the newest
attempt's failure; `check BASE..REV` answers whether a range is reviewed and clear (below); `list`
lists rounds; `show TASK ROUND` prints one round's report; `prune` removes tasks past `keep_days`
now. Flags: `jst review --help`.

The subcommands win over a revision of that name (`jst review --reviewer codex -- list` reviews a
branch `list`). `--model`, `--effort` and `--timeout` apply to every reviewer named. `--json` prints
`round.json`'s fields or the attempt's outcome; otherwise three lines: the outcome, the report's
path, and the next step.

| exit | meaning |
|---|---|
| 0 | the round is done and does not block |
| 1 | the round is done and blocks (an open finding of the ledger blocks, section 4) |
| 2 | usage or configuration: a bad flag, an unusable `review` section, an unknown revision, a task name that is not allowed, no reviewer named, the task busy with another run |
| 3 | done, and a place is due for redesign (W-019) |
| 4 | done, the round limit is reached and still blocks — or a round past it refused — the owner decides |
| 5 | a reviewer did not give a usable answer (timeout, failure, no answer, unparsable, inconsistent, no slot) |
| 6 | the environment refuses: inside a sandbox (Codex's `auth.json` or Claude Code's state file not writable), no `auth.json`, the login failed earlier and was not renewed, a reviewer not installed |

The gate never runs `jst review`: it costs money, needs the network and answers differently each
time (W-013).

**`jst review check [--task NAME] BASE..REV`** is the one answer to "is this change reviewed and
clear?"; no consumer reads the index or `last` for it. Both ends are resolved to commit ids and
nothing is inferred: exit 0 only when all hold, else exit 1 with each reason that failed:
- the task's newest **round** (attempts that did not become rounds are ignored; a newer failed
  attempt is reported as a note) was made at exactly REV;
- its chain's base is exactly BASE — so, by section 4, the chain reviewed every change from BASE to
  REV; a chain from any other base, nearer or farther, does not answer for this range;
- the round records the current review policy (`brief`, `template`, `cite`, severities, triage,
  repetition, round limit; not scheduling, storage or per-run overrides) and every reviewer of
  `review.use` finished (with `review.use` explicitly empty, any one completed reviewer);
- after it, no open finding of the ledger blocks;
- it is not redesign-due, and not a round past `round_limit` that followed a limit round still
  blocking by today's rule without `--past-limit` having been given.
No other field promises anything; `blocks: false` on a record is not "reviewed". A single commit
is checked as `REV^..REV`.

## 8. Go design (W-036)

| package | holds |
|---|---|
| `internal/review` | the settings and their validator; the prompt (embedded template and schema); rounds, chains, triage, places and W-019; the store, index and pruning; the commands (`cli.Register`) |
| `internal/review/codex` | the Codex runner (it knows no rounds or locks: the caller's start lock is released through a callback at Codex's first answer): argv, environment, stdin, deadline and process group, the event stream (first-answer signal, auth failure), outputs, outcomes |
| `internal/chatevent` | the event a producer publishes into the chat and the `Publisher` the chat registers ([chat.md](chat.md) section 10); without a chat, publishing answers "unavailable" and the event stays owed |

It reuses `internal/config`, `internal/project`, `internal/strictjson`, `internal/gitcli`,
`internal/homedir` and `internal/cli`. `internal/review` declares `Reviewer` (`Review(ctx, Request)
Result`: always a result, never only an error) and a small git interface; `internal/review/runner`
holds the shared subprocess types, and the dispatcher picks `internal/review/codex` or
`internal/review/claude` by name. Locks are `internal/flock`'s (`TryFile`, `WaitFile`,
`File.Release`); every child runs through `internal/supervise` (Mode Group, grace 5 s) with the
process model of [implementation.md](implementation.md) section 5.2, git with hooks and fsmonitor
off. `jst tidy` skips locked worktrees, including review trees under a store's `.trees/`. Libraries:
the standard library and `golang.org/x/sys` (W-037); no `errgroup`, since goroutines return result
values. Codex is not pinned; its version is recorded per run.

**Tests** (W-013: no clock, no network). Unit tests with fakes cover every rule of sections 1 to 7
(each refused word and range, prompt building, every outcome of 3.5, triage on and off, places,
W-019 across chains and not across `--fresh`, the round limit, exit precedence, killed attempts, the
ledger, every reason of `review check`, pruning, index rebuild). The runner is tested against a
fake `codex` built into `t.TempDir()` that checks its argv, environment and closed stdin, and
scripts events, deadlines, failures and auth errors. Cross-process tests cover the locks and slots;
an unwritable `auth.json` gives exit 6; integration tests (real git) cover the pinned tree. A real
Codex round is run by hand, outside the gate.

The measurement of the wrapper against direct calls, outside the gate too: seeded defects over
three ranges, recall, precision, time per stage, model and cost (`just review-bench`,
[bench/review](../../bench/review/README.md); the runs before 0.6.0 are in its `results/`).

## 9. What it cannot see, and limits

- Whether a reviewer's ratings are right: jackstay computes from them; the session checks them (W-048).
- Whether two places are one: places are strings; a reviewer that renames a place splits its count
  (the prompt lists prior places to keep them stable; `repeats` joins them).
- Codex's refresh timing and lock are inferred (section 2).
- Whether Codex honours `--output-schema` exactly in every version: an answer that does not match is
  `unparsable`, never guessed at.
