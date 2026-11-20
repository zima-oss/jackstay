# `jst review`: cold code reviews

Status: built ([W-051](../decisions/W-051-jst-review-codex-rounds.md)). It follows
[W-019](../decisions/W-019-repeated-findings-mean-redesign.md) (a place failing two rounds running
is redesigned), [W-048](../decisions/W-048-review-findings-triaged-by-realism.md) (findings rated by
likelihood and consequence), W-036 and
[W-037](../decisions/W-037-current-go-and-dependencies.md) (Go design, current versions) and
[W-013](../decisions/W-013-quality-testing-declarative.md) (tests). Codex runs outside Claude's
sandbox, and the reviewers are configured through `jackstay.json`. One executor outside the sandbox,
with a pool, is an idea kept for later and not designed here.

## What it removes

Without it, every session builds a review by hand: the prompt (read only, the verdict line, the
commit, earlier findings), the frozen tree, the round number and file name, the launch flags, where
the answer goes and how to read it. Each of those steps done by hand fails now and then (measured
on reviews run by hand):

| failure of a hand-built review | what removes it here |
|---|---|
| stdin left open with the prompt as an argument: Codex waits on stdin for minutes | the prompt is always written to Codex's stdin and stdin is then closed (section 3.4) |
| Claude's sandbox: Codex cannot write `~/.codex` or reach its auth host | jackstay refuses to start Codex where it cannot write Codex's login (section 2) |
| a prompt written to the sandbox's `$TMPDIR`, missing for the unsandboxed rerun | prompts live under `~/.jackstay/`, never `$TMPDIR` (section 5) |
| the tree changed while Codex read it | Reviewers read snapshots at fixed Git object IDs (section 3.1) |
| no or ambiguous verdict line; a malformed event | the answer is JSON against a schema Codex is given; every failure to read it is a named outcome (section 3.5) |
| a batch launch through `xargs` too long | several reviewers are one call; the prompt never goes through argv (section 6) |

## 1. The `review` section of `jackstay.json`

Every key is optional. The reviewer is `--reviewer`, else the project's `review.use`, else Codex.
Both providers' settings are available with or without a `review` section. An explicitly empty
`review.use` with no `--reviewer` exits 2 before starting a round.
Unknown keys and a
key written twice are refused, as everywhere in `jackstay.json` (`internal/config`, strict). Paths are
relative to the repository's root and read **from the reviewed commit's tree** (section 3.1), never
the live checkout, so a brief edited after the commit cannot change what was reviewed; `--brief`
on the command line is the exception (a file outside the tree, copied into the round's folder).

```json
"review": {
  "reviewers": {
    "codex": {
      "command": "codex",
      "model": "",
      "effort": "medium",
      "timeout_minutes": 20,
      "isolation": ["--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check",
                    "-c", "mcp_servers={}", "-c", "features.apps=false"],
      "mcp_servers": {}
    }
  },
  "use": ["codex"],
  "brief": "docs/review-brief.md",
  "template": "",
  "cite": ["AGENTS.md", "docs/decisions/", "docs/design/"],
  "severities": ["blocker", "major", "minor"],
  "blocking": ["blocker"],
  "triage": true,
  "repeat_rounds": 2,
  "repeat_severities": ["blocker", "major"],
  "round_limit": 6,
  "max_concurrent": 4,
  "queue_minutes": 15,
  "store": "",
  "keep_days": 30
}
```

| Key | Meaning | Default |
|---|---|---|
| `reviewers` | overrides for built-in `codex` and `claude-code` settings, by name | built-in settings for both |
| `reviewers.codex.command` | the Codex program: a name looked up on `PATH`, or an absolute path | `codex` |
| `reviewers.codex.model` | passed as `-m`; empty means Codex's own default (with `--ignore-user-config` that is Codex's built-in one, not the user's `config.toml`) | empty |
| `reviewers.codex.effort` | reasoning effort, passed as `-c model_reasoning_effort="…"`; one of `minimal`, `low`, `medium`, `high`, `xhigh`; empty passes nothing | `medium` |
| `reviewers.codex.timeout_minutes` | the hard deadline of one run (section 3.4); 1–120 | 20 |
| `reviewers.<name>.mcp_servers` | MCP servers given back to that reviewer, by name (letters, digits, `_`, `-`): `{"command": "…", "args": […]}`. The user's own servers stay closed (section 3.4); these are the project's explicit choice. Needs `"jackstay_schema": 3` | none |
| `reviewers.codex.isolation` | the isolation flags, a list of argv words that **replaces** the default list whole. Words jackstay owns are refused here (below) | the set above |
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

Projects configure it in their `jackstay.json` `review` section. The section's
types go into `config.Table`; its meaning (the refused words, ranges, known names in `use`, `blocking`
⊆ `severities`, `cite` present in the tree) is `internal/review`'s validator, which `jst check` runs
as it runs the release's.

### 1.1. Claude Code adapter

Select it with `--reviewer claude-code` or include it in `review.use`. Its defaults are command `claude`,
the CLI's own model and effort defaults, and the same 20-minute timeout as Codex. Available effort
overrides are `low`, `medium`, `high`, `xhigh`, `max` and `ultracode`. Set
`reviewers.claude-code` to override its settings. Claude Code safety flags
are fixed. Configuration with a nonempty Claude Code `isolation` array is refused.

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

Both reviewers use the same on-demand source layout (section 3.1). A large range adds files and
patch bytes to the local view. The initial prompt size depends on instructions and open findings.

Claude Code gets the same verdict JSON Schema via `--json-schema` and returns a JSON envelope via
`--output-format json`. Jackstay accepts `structured_output` only in a successful result,
writes that field as the round's answer, and passes it through the same schema validation and
ledger as Codex. A second file can carry numeric token counts. The run record omits the provider's
raw stdout and stderr. The machine-wide start lock is released when the subprocess finishes or
after 120 seconds, because the JSON envelope arrives only at completion.
[Claude Code headless mode](https://code.claude.com/docs/en/headless)

## 2. Codex runs outside the sandbox, and its login is refreshed by one run at a time

`jst review` starts Codex itself, outside Claude's sandbox. The session runs `jst review` as a
command that bypasses the sandbox, as sessions already run `codex`; jackstay does not try to escape a
sandbox it is in.

**Why the login matters**. Codex keeps its ChatGPT login in `$CODEX_HOME/auth.json`
(`~/.codex` by default) and refreshes it through `auth.openai.com`, writing the new tokens back. If
the refresh is made where the file cannot be written — inside the sandbox — the new refresh token
can be lost while the server has spent the old one (inferred: such refresh tokens are usually
single-use; a refresh failure of this kind has been seen), and the login is lost for every session
and the user. Two runs refreshing at once can race the same way (inferred).

Codex refreshes its login rarely, not per run, and keeps a lock file of its own (`auth.lock`)
around it (both inferred from the files, not documented). So the race is rare, and jackstay's part is
small and cheap:

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
   at most 120 s. Whatever Codex refreshes at start or on its first request is then done by one run
   at a time, across every project and session on the machine. It costs the time to first answer
   per start (seconds), against runs of minutes.
3. **Stop the cascade.** A run that ends with no answer and whose failure is Codex's own
   authentication failure writes
   `~/.jackstay/.all/reviews/login-failed` (the time and the message). Every later `jst review` refuses with
   exit 6 while that file is newer than `auth.json`; the user's `codex login` rewrites `auth.json`
   and so lifts it, with nothing to clear by hand. Starting more runs on a broken login only
   multiplies failures. "Codex's own failure" is read only where Codex reports about itself: the
   message of a top-level `error` event or of `turn.failed`, and Codex's log lines on standard
   error (`ERROR`/`WARN` lines of its own modules) — never the content of an `item` (the model's
   text, a command's output), which in a review of login code is full of "401" and "refresh
   token". The patterns are a short table in the code (an HTTP 401 from the provider, "refresh
   token", "not logged in", "log in again"), each with a test from a recorded failure.

The lock does not cover a Codex the user runs by hand or another tool runs; Codex's own
`auth.lock` is what covers those (inferred). An executor that owned the login whole is an idea kept
for later; this spec does not need it.

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

The attempt removes the view after the reviewers stop and before publishing a round. A subsequent
run of the same task reclaims its abandoned attempt views under the task lock. The existing
retention policy also reclaims expired tasks. Cleanup recognizes legacy locked review worktrees.

Projection is offline. A partial clone needs the objects for the pinned revisions locally. The
host filesystem must represent the tracked path names distinctly. Projection reports unavailable
objects and path collisions as review failures. Git uses the pinned head's tracked attributes
for the change files. Git-local administrative `info/attributes` can still override them.

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

- **The reviewer's access profile** ([W-090](../decisions/W-090-review-of-0-6-0.md)), the same for Codex and Claude Code (`internal/review/access`).
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
    names kept).
  - **secrets are not read**: `~/.ssh`, `~/.aws`, `~/.azure`, `~/.gnupg`, `~/.kube`,
    `~/.docker/config.json`, `~/.netrc`, `~/.npmrc`, `~/.pypirc`, `~/.config/gh`, `~/.config/gcloud`,
    `~/.config/hub`, `~/.config/op`, `~/.git-credentials`, `~/.password-store`, `~/.vault-token`,
    `~/.terraform.d/credentials.tfrc.json`, `~/.cargo/credentials.toml`, the keychain
    (`~/Library/Keychains`) or keyring (`~/.local/share/keyrings`); **the other reviewer's login and
    state** (Claude Code is denied `$CODEX_HOME`, else `~/.codex`; Codex is denied `~/.claude`,
    `~/.claude.json` and `$CLAUDE_CONFIG_DIR`); and `.env` / `.env.*` files. Their contents are
    also left out of the generated patches (`changes/*.patch`); the change index still names them.
    Claude Code: `Read`, `Glob` and `Grep` deny rules for each path and `Read(**/.env)`,
    `Read(**/.env.*)`, through `--settings`, enforced at every read. Codex: each path `"none"` in
    the profile; its rules take paths, not patterns, so the `.env` files are those found when the run
    starts under the source view, the live checkout and the folder beside it (the neighbouring
    checkouts), 6 folders deep, past dependency and cache folders, 2,048 at most. A `.env` elsewhere,
    deeper, or made during the run is not denied to Codex: the limit of this profile.
  - **the user's MCP servers, hooks and rules are off**: Codex `--ignore-user-config`,
    `--ignore-rules`, `mcp_servers={}`; Claude Code `--restricted --safe-mode` (no user, project or
    local settings, no hooks, no plugins), `--strict-mcp-config` with an empty `--mcp-config`,
    `disableAllHooks`. `review.reviewers.<name>.mcp_servers` gives chosen servers back, and their
    tools only.
  Checked against Codex 0.169.0 and Claude Code 2.1.328 on macOS (2026-11-20): both were denied
  `~/.ssh` and an untracked `.env` of the checkout, listed `/usr/share`, and fetched
  `https://example.com`; Codex's shell had the user's full `PATH` and no `GH_TOKEN` set in the
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
  each capped at 64 MiB (past the cap jackstay stops writing and notes it; the run goes on).
- **deadline**: `timeout_minutes`. Codex runs in a process group of its own; at the deadline, or
  when jackstay is asked to stop (TERM/INT, as `jst bg` passes it on), the group gets TERM, then KILL after
  5 s. A run past its deadline is outcome `timeout`.
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
      "repeats": ["r2-codex-F3"]
    }
  ],
  "prior": [{"id": "r2-codex-F3", "status": "open"}]
}
```

`verdict` is `approve`, `changes` or `incomplete`. An incomplete review records the reason in
`summary` and leaves the ledger unchanged. `changes` identifies a new or still-open finding.
`severity` is one of `severities`; `likelihood` `ordinary` (happens
in ordinary work by accident) or `unusual` (needs a deliberate or unusual setup); `consequence`
`severe` (lost work or data, a wrong decision that passes unnoticed) or `mild` (an inconvenience,
a wrong output seen at once) — W-048's two axes in two words each. `repeats` names ledger findings
this one continues. `prior` answers every open finding of the ledger, `fixed` or `open`. The schema sets `additionalProperties: false` and every member required (`[]`
where empty), as structured outputs require; Codex honours it.

jackstay reads `answer.json` with `internal/strictjson` (one value, no member twice, no unknown member, 1
MiB at most) into a Go type, then checks it: ids unique; severities, likelihoods and consequences
from the lists; `repeats` and `prior` naming only findings of the ledger; every open finding of
the ledger answered in `prior`, once. jackstay does not trust the reviewer's `verdict` for the outcome: it
computes it (section 4) and records a disagreement (`approve` with a blocking realistic finding,
or the reverse) in the report.

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

A run that ends in anything but `done` is not retried by jackstay — the session decides, with the reason
and the files in front of it — and it does not take a round number: the next run of the task reuses
N, and the answers the other reviewers gave (section 4, "Attempts and rounds").

## 4. Rounds, triage and W-019

A **task** is what is reviewed in rounds: by default the session's current branch name, else
`--task NAME` (required on a detached head); a name is any text git accepts as a branch name, at
most 200 bytes. New tasks use the lower-case SHA-256 of that name as a fixed-length folder
component. A `task.json` inside stores the original name, so the store can enumerate tasks. An
exactly named legacy percent-encoded folder stays readable and writable. Case variants remain
distinct on filesystems that fold case.

**One run of a task at a time.** A run takes the task's lock, `<store>/.locks/v2/<task hash>.lock`
(`flock`, outside the task's folder so pruning cannot remove it under a holder), before it reads
the round number and keeps it until its report and index line are written. A second `jst review` of
the same task while one runs refuses at once (exit 2, "task busy", naming the holder's process),
so two runs can never take one number or write one folder. Pruning takes the same lock without
waiting and skips a task it cannot take. A writer of an existing legacy folder also takes that
folder's old lock to avoid colliding with a previous binary that already holds it.

**Attempts and rounds.** Every run is an *attempt*, written from its first byte into
`<task>/attempt-<k>/` (k counts every attempt of the task, never reused). When every reviewer of
the attempt ended `done`, jackstay writes the round's record (`round.json`, section 5) into it and then,
as the last step, renames the folder to `<task>/round-<N>/` — that rename is the only thing that
takes round number N, so a jackstay killed at any point leaves an `attempt-<k>/` that is simply a failed
attempt, with its evidence, and the next run takes k+1 and still N. Rounds are numbered 1, 2, … in
the order of those renames. With several reviewers and some failing, the attempt is not a round
and exit is 5; the successful answers stay in its folder, readable.

**Only the missing answers are paid for again** ([W-090](../decisions/W-090-review-of-0-6-0.md)). `attempt.json` records the attempt's
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
another (a full review of `BASE..REV` with an empty ledger), every other round continues the
chain of the round before it, from the same base (3.1). So a chain at commit C has reviewed,
between its rounds, every change from its base to C: the first round the whole range, each later
one the tree difference since the round before (a rebase included, since the diff is of trees).
Every round in a chain uses the same review policy and the same reviewer names. A change to either,
or an older round without a policy ID, requires a full BASE..REV review with `--fresh`.

The **ledger** of a chain is jackstay's list of its findings, computed from the rounds' answers and kept
whole in each `round.json` (section 5): every field the reviewer gave, plus the **ledger id** jackstay
assigns, `r<round>-<reviewer>-<answer id>` (`r2-codex-F3`), unique in the chain whatever ids the
answers reuse. The prompt shows ledger ids; `prior` and `repeats` must use them, and an answer that
names any other is `inconsistent`. A finding enters the ledger in the round that reports it:
- **open** if it is not rare (with `triage` off, every finding is open). It stays open until a
  later round's reviewer answers `fixed` for it in `prior`. Every round sees and answers the whole
  open ledger (3.2, 3.5), so an open finding is re-examined until it is fixed; nothing leaves the
  ledger by being skipped, by a focus, or by an empty `findings`.
- **deferred** if it is rare: not fixed, kept with its history,
  listed in the report for the project's later card, and not put to later rounds. A rare finding
  found again later is a new finding of that round (it then counts for W-019 as any finding).

**Triage and blocking** (W-048). With `triage` on, a finding is **realistic** when `likelihood` is
`ordinary` or `consequence` is `severe`, **rare** when it is `unusual` and `mild`. One predicate
says whether a finding blocks: its severity is in `blocking` and, with `triage` on, it is realistic
(with `triage` off, the severity alone). A round **blocks** when any *open* finding of its ledger
blocks after the round — those it found and those it answered `open` alike. Rare findings never
block; the report lists them apart with a ready row for the project's later-findings card (jackstay does
not write the project's repository). The session checks the ratings; `jst review` only computes from them.

**Places.** A place is the finding's `place` string, trimmed, with runs of spaces made one and
compared without regard to case. The prompt lists prior places and asks for the exact string again
when a finding is about the same place; a finding that names a prior one in `repeats` takes that
one's place whatever it wrote. Places are merged across the reviewers of a round.

**W-019, in the tool.** A round **hits** a place when, in that round, the place has a finding of a
`repeat_severities` severity that the round found or answered `open` — realistic or rare alike:
a place that keeps drawing findings, rare ones included, is due for redesign. Found anew and confirmed still there count the same, so the count does not
depend on how a reviewer chooses to write it. When a place is hit in `repeat_rounds` rounds
running of one chain, jackstay says it is **to be redesigned, not patched**, names it with the rounds and
findings, and exits 3; the round is then **redesign-due** until a later round of the chain does not
hit that place. The default leaves out the lowest severity (`minor`: wording and small clarity),
because W-019 is about defects; a project that wants every finding counted lists every severity.

**The owner is asked** when round `round_limit` ends and still blocks: exit 4, and the report's
first line says the owner decides (merge with what is known, redesign, or more rounds). A further
round of the task is refused (exit 4, nothing run) unless `--past-limit` is given; the session gives
it only after the owner's word, which it records as any ruling. Exit 3 takes precedence over 4
when both hold: the redesign is the answer the rules already give.

**After a redesign** the next round is an ordinary round of the chain, usually with `--focus`
naming the redesigned place (3.2). It reviews the whole delta since the round before and every open
finding; `--focus` only says where to look hardest.

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
the index — an appended line, pruning's rewrite, a rebuild — is made under `<store>/index.lock`
(`flock`, a file that is never replaced), held from opening the index to closing it, so an append
can never land in a file that a rewrite is about to replace. The index is a convenience for
`jst review list`, rebuilt from the folders by `jst review list --rebuild` if it is lost or damaged
(a line that does not read is skipped with a warning).

**Pruning.** At the start of every `jst review` of a project and by `jst review prune`, a task whose
newest round or attempt is older than `keep_days` is removed whole, and the index rewritten
without it. Pruning acquires the index lock before removing tasks and holds it through the atomic
rewrite. A review of a recreated task can append its results after pruning releases that lock.
The rounds of a task are kept together while it is alive, so W-019's count never loses a round in
the middle of a chain.

## 6. In the background, and several at once

A review takes minutes. A session runs it as it runs other long commands ([implementation.md](implementation.md) section 5.1):

    jst bg --for 37 --name review-<task> -- jst review [REV] …

with the Bash tool's background flag; the session is told when the call ends and reads
`jst review last`. `jst review … --background` does that itself and returns at once (W-090: the
0.5.x reviewer pool, its store, worker and nine subcommands, folded into `jst review`):
- **Submission.** It resolves `REV` (and `BASE`) to commit ids now and, under the lock of the
  task's request file `<store>/.requests/<task hash>.json`, writes the request (id, task, the
  worktree, the commits, the reviewers, `--fresh`, `--past-limit`, `--focus`, the chat sessions to
  tell) and starts, detached in a session of its own, `jst bg --for <budget + 1> --name
  review-<task> -- jst review --task T --reviewer R --request ID -- BASE..REV`. The `jst bg` entry is
  the durable record of the pending run (`jst procs`). It waits, at most 10 s, for the run to take
  the task's lock, so `jst bg` finds the session it belongs to among its parents. The chat's
  `review.submit` tool is the same submission.
- **One request per task.** While the request's process lives (its id and start in the process
  table; without a table, until its deadline), an equal submission (same worktree, task, commits,
  reviewers and flags) joins it, adding its session (at most 32), and a different one is refused
  (exit 2, "task busy"). A request whose process is gone is ended as lost (below) before a new one
  is written.
- **Concurrency** is section 6's slots and nothing else: reviews of different tasks run at once, up
  to `max_concurrent` reviewer runs; there is no machine-wide worker.
- **The end.** The run with `--request` marks the request running once it holds the task's lock
  (a run whose request was replaced refuses, exit 2). Every request ends exactly once, in one
  record, `<store>/.ends/<task hash>-<request id>.json`, written under the request's lock before the
  request is removed: the request (its sessions included), its outcome (exit status, outcome word,
  next step, round and report folder), whether it was **lost**, and which sessions have been told.
  A request ends when its run finishes, whatever the outcome; when its launch fails (the sessions
  that joined meanwhile are told too); or, lost, when its process is found gone — by a new
  submission of the task, by `review.status`, or when any session's chat looks for what it is owed.
  Recovery has one rule: a request with an end is over — it cannot be joined, and a crash after the
  end was written and before the request was removed tells the saved outcome, never a second one.
  An end's next step is cut to 2 KiB, so it always fits its file.
- **Telling the sessions.** Each session's `review.finished` event is made from the end (outcome
  word, round, report path, next step, `jst:review/<task>/<round>`), keyed by request and session,
  and given to the chat's publisher (`internal/chatevent`, [chat.md](chat.md) section 10); each one
  the chat stored is recorded in the end. What it did not take is pulled when that session's chat
  next reads its inbox or reaches its hook; a repeat stores nothing new. An older end of the task
  of the same repository whose sessions were all told is removed when a newer one is written.
- **Status.** A request records its repository (the Git common folder). `review.status` answers for
  the asking repository only: its request running, if any, and how its latest one ended (outcome,
  round, report). It reads nothing of the rounds kept by project and task, which clones of one
  project share; a round made in the foreground is read with `jst review last`.

## 7. Commands

    jst review [REV | BASE..REV] [--task NAME] [--reviewer NAME,…] [--brief FILE] [--focus TEXT]
              [--fresh] [--model M] [--effort E] [--timeout MIN] [--past-limit] [--json]
    jst review [REV | BASE..REV] --background [--task NAME] [--reviewer NAME,…] [--focus TEXT]
              [--fresh] [--past-limit]                  the same round under `jst bg`, section 6
    jst review last [--task NAME] [--json]          the newest round's report, or the newest attempt's failure
    jst review check [--task NAME] BASE..REV        is BASE..REV reviewed and clear? (below)
    jst review list [--task NAME] [--rebuild] [--json]
    jst review show TASK ROUND [--json]             one round's report
    jst review prune                                remove tasks past keep_days, now

`last`, `check`, `list`, `show` and `prune` are two-word entries of the command table and win over a
revision of that name; a branch named `list` can be reviewed as
`jst review --reviewer codex -- list`. `--model`,
`--effort` and `--timeout` override the configuration for this call and apply to every reviewer
named. `--json` prints the round's
result as one JSON object (`round.json`'s fields, or the attempt's outcome) instead of the
summary. Without it,
`jst review` prints three lines: the outcome, the report's path, and the next step (fix, redesign the
named place, ask the owner, or rerun).

| exit | meaning |
|---|---|
| 0 | the round is done and does not block |
| 1 | the round is done and blocks (an open finding of the ledger blocks, section 4) |
| 2 | usage or configuration: a bad flag, an unusable `review` section, an unknown revision, a task name that is not allowed, no reviewer named, the task busy with another run |
| 3 | done, and a place is due for redesign (W-019) |
| 4 | done, the round limit is reached and still blocks — or a round past it refused — the owner decides |
| 5 | a reviewer did not give a usable answer (timeout, failure, no answer, unparsable, inconsistent, no slot) |
| 6 | the environment refuses: inside a sandbox (Codex's `auth.json` or Claude Code's state file not writable), no `auth.json`, the login failed earlier and was not renewed, a reviewer not installed |

**`jst test` and the gate.** `jst review` is not a test scope and the gate never runs it: a review
costs money and minutes, needs the network and gives a different answer each time, against W-013
(no test depends on time or the network).

**`jst review check [--task NAME] BASE..REV`** is the one answer to "is this change reviewed and
clear?", for a session before it lands, or later the merge queue; no consumer reads the index or
`last` for it. The consumer names the change it means — both ends, resolved to commit ids — and
nothing is inferred: exit 0 only when all hold, otherwise exit 1 with each reason that failed:
- the task's newest **round** (attempts that did not become rounds are ignored; a newer failed
  attempt is reported as a note) was made at exactly REV;
- its chain's base is exactly BASE — so, by section 4, the chain reviewed every change from BASE to
  REV; a chain from any other base, nearer or farther, does not answer for this range;
- the latest round records the current review policy and at least one completed reviewer. Every
  reviewer in `review.use` must have finished successfully. With `review.use` explicitly empty, either
  provider's completed round can qualify. The policy covers prompt configuration (`brief`, `template`, `cite`), severity,
  triage, repetition and round limits. Scheduling, storage and per-run model, effort and timeout
  overrides are independent of this policy;
- after it, no open finding of the ledger blocks;
- it is not redesign-due, and not past `round_limit` without `--past-limit` having been given.
No other field promises anything; `blocks: false` on a record is not "reviewed". A single commit
is checked as `REV^..REV`.

## 8. Go design (W-036)

| package | holds |
|---|---|
| `internal/review` | the settings and their validator; the prompt (embedded template and schema); rounds, chains, triage, places and W-019; the store, index and pruning; the commands (`cli.Register`) |
| `internal/review/codex` | the Codex runner (it knows no rounds or locks: the caller's start lock is released through a callback at Codex's first answer): argv, environment, stdin, deadline and process group, the event stream (first-answer signal, auth failure), outputs, outcomes |
| `internal/chatevent` | the event a producer publishes into the chat and the `Publisher` the chat registers ([chat.md](chat.md) section 10); without a chat, publishing answers "unavailable" and the event stays owed |

Reused, not duplicated: `internal/config` (the `review` keys added to `config.Table`; `Load` with
`need: ["review"]`), `internal/project` (`Open`, the root, the name, `Repo` for `brief`/`cite`
paths, kept inside the repository), `internal/strictjson` (the answer), `internal/gitcli` (rev-parse,
status, legacy worktree cleanup, log), `internal/homedir` (`ProjectPath`, `SharedPath`,
`AppendLine`, `MarkerName`), `internal/cli`.

**Interfaces where used** (W-037): `internal/review` declares `Reviewer` (`Review(ctx, Request)
Result`, a result always, never only an error) and a small git interface (resolve, status,
build the source view and remove legacy worktrees). `internal/review/runner` holds the shared subprocess
request and result types. The dispatcher selects `internal/review/codex` or
`internal/review/claude` by reviewer name. Tests use
hand-written fakes of both (a fake reviewer returning a scripted answer, a timeout, an unparsable
file, a login failure). `log/slog` only if a diagnostic log is kept; the outcomes go in `run.json`.

**Shared with jackstay's process runner**: review uses the shared packages, so jackstay has one of
each:
- *`flock` on a file* — `internal/flock`'s `TryFile`, `WaitFile` and `File.Release`: the task lock,
  the slots, the start lock and the index lock.
- *Every child process* — `internal/supervise` (Mode Group, grace 5 s): git and Codex alike run in a
  process group of their own, and a group is signalled **only while its leader is not reaped**, so
  its id cannot have been reused: the leader's end is watched without reaping it (kqueue on darwin,
  `waitid` with `WNOWAIT` on linux), the group is stopped (TERM, 5 s, KILL) at the deadline or what
  it left is ended, output reading is bounded, and the leader is reaped last. Review's git also runs
  with hooks and fsmonitor off, so it starts nothing of the repository's own. `internal/gitcli` runs
  git the same way (its own group, TERM then KILL at the context's end, the group never signalled
  after the reap): a git descendant (a hook, a checkout filter) cannot hold a call past the budget.
- *`jst tidy`.* It must skip locked worktrees, and in particular review trees under a store's
  `.trees/`; review removes its own.
- *`jst bg`.* Section 6 uses only its command line.

**Libraries** (W-037): standard library only — `os/exec`, `syscall` process groups,
`text/template`, `embed`, `encoding/json/v2` through `strictjson`, `regexp` — plus `golang.org/x/sys`
for `unix.Flock`. No `errgroup`: the goroutines return result values (section 6), so
`golang.org/x/sync` is not needed. jackstay does not pin Codex (the user's tool); it records its
version per run.

**Tests** (W-013: no clock, no network):
- Unit, with fakes: settings and every refused word and range; prompt building (fresh, delta,
  `--fresh`, `--focus`, a project template, the verdict form always appended); the answer's
  reading (every outcome of 3.5, a member twice, an unknown member, a `prior` that misses one);
  triage and blocking with `triage` on and off; places (case, spaces, `repeats`); W-019 across a
  chain of 2 and 3 rounds and not across `--fresh`; the round limit and `--past-limit`; exit-code
  precedence; failed runs not taking a number, and an attempt left by a killed run (no rename)
  becoming a failed attempt; the ledger over a chain (opened, answered `open` twice, fixed; a
  missing `prior` answer refused; an empty `findings` with an open blocker still blocking); every
  reason of `jst review check` (wrong commit, chain without a full start, an open blocker,
  redesign due, past the limit, a newer failed attempt only noted); pruning with dates from the
  files given, not now; the index rebuilt from folders.
- The Codex runner against a **fake `codex`**: a small Go test program built into `t.TempDir()`
  and named as `command`, scripted by a file — it checks its argv and environment, reads stdin to
  EOF (and fails if stdin is a terminal or never ends), emits events, writes `-o`, sleeps past a
  deadline, exits non-zero, prints an auth error. This proves the stdin, environment, deadline and
  group-kill rules without Codex.
- The locks across processes: two test processes hold the start lock in turn; slots beyond M wait;
  a holder killed releases.
- The sandbox refusal: `auth.json` made unwritable (mode 0400) gives exit 6.
- `//go:build integration` (real git): a pinned tree is at the commit and unchanged by edits to the
  checkout; removed after; a stale tree of a dead pid removed; locked trees survive `git worktree
  prune`.
- Not in the gate: a real Codex round, run by hand (it also confirms that Codex accepts the
  schema, section 3.5).

## 9. What it cannot see, and limits

- Whether a reviewer's ratings are right: jackstay computes from them; the session checks them (W-048).
- Whether two places are one: places are strings; a reviewer that renames a place splits its count
  (the prompt lists prior places to keep them stable; `repeats` joins them).
- Codex's own refresh timing and lock: inferred (section 2); the start lock is cheap insurance, not
  proof.
- Whether Codex honours `--output-schema` exactly in every version: an answer that does not match is
  `unparsable`, never guessed at.
