# `jst land`: a thin landing behind a small hosting adapter; where tests run

Status: for 0.6.0; sections 1 to 6 are built, 7 is not yet (so doctor does not yet report the
CI-covered scopes of section 5). It follows
[W-090](../decisions/W-090-landing.md) (one forge-neutral `jst land`, hosting adapters, two autonomy
modes chosen explicitly) and [W-092](../decisions/W-092-thin-land-and-where-tests-run.md) (the
landing stays thin; where tests run is the project's declared split between local and CI) and
[W-096](../decisions/W-096-task-graph-and-input-cache.md) (scopes as a task graph, cached by their
inputs). It keeps
[W-047](../decisions/W-047-release-redesign-fixed-settings-no-one-off-imports.md) (`jackstay.json`
changes alone and is a person's to merge) and
[W-019](../decisions/W-019-repeated-findings-mean-redesign.md) (a landing never switches, pulls or
rebases this checkout).

## What it is, and what it is not

`jst land` pushes the branch, opens the change request, waits for the checks the host's branch
rules require, and merges with the head that was checked. CI is the gate: what CI runs is what
blocks a merge. Tests that CI cannot run are run locally, as the project declares (section 7).

Not in it: a local gate on the tree's rules, landing-evidence records and a reconcile after the
merge, carrying a review across base merges, publishing jackstay's statuses to the host, provenance
as a landing condition (provenance stays in release and install), and jackstay's own merge queue.
The agent never puts a change in the host's native merge queue: the queue merges later, on a base
jackstay has not judged, which is an auto-merge. A person queues it (section 3.3).

`jst gh pr-land`, the other `jst gh` commands and the unreleased `jst queue` are removed in 0.6.0,
with no aliases (section 6).

## 1. The `land` section of `jackstay.json`

It needs `jackstay_schema: 3`. `jst adopt` writes it in full, so a project's behaviour is read in
its file.

```json
"land": {
  "host": "github",
  "autonomy": "merge-when-green",
  "method": "squash",
  "checks": [],
  "human_paths": []
}
```

| key | values | default | meaning |
|---|---|---|---|
| `host` | `github` (0.6.0) | the push remote's host | the adapter; its connection stays in `github` (`account`, `remote`, `repository`, `wait`) |
| `autonomy` | `merge-when-green`, `prepare` | none: adopt asks | the agent merges when the required checks pass, except for human paths; or it stops before every merge |
| `method` | `squash`, `merge`, `rebase` | `squash` | the merge method asked of the host |
| `checks` | list of names | `[]` | checks required in addition to the base's branch rules (section 4); never fewer |
| `human_paths` | glob list | `[]` | paths only a person lands, in addition to the fixed three: `jackstay.json`, `.github/workflows/**`, `.claude/settings.json` |

`autonomy` and `checks` are always read from the base's file, never the change's, so a branch
cannot keep a permission the base has withdrawn. `--prepare` lowers autonomy for one run. Nothing lets the agent merge in a `prepare` project; a
person may (section 3.3). A project without a `land` section has no policy: `jst land` refuses and
names `jst adopt`.

## 2. The hosting adapter

```go
type Host interface {
    Name() string
    DefaultBranch(ctx context.Context) (string, error)
    Change(ctx context.Context, ref ChangeRef) (Change, bool, error) // by number, or the open one from a head branch
    OpenChanges(ctx context.Context, head string) ([]Change, error)
    Open(ctx context.Context, request NewChange) (Change, error)
    Checks(ctx context.Context, change Change) ([]Check, error)        // head and test merge
    Rules(ctx context.Context, branch string) (Rules, error)
    BranchTip(ctx context.Context, branch string) (string, error)     // the base's tip, for the recheck
    UpdateBranch(ctx context.Context, change Change, expectedHead string) error // merges the base in
    Merge(ctx context.Context, change Change, request MergeRequest) (Merged, error) // method, head, message
    Enqueue(ctx context.Context, change Change, expectedHead string) error     // native queue, a person's
}
```

`Rules` gives the required checks (name, and the app that must report one where the host says),
whether the base must be up to date, whether a merge queue is required, and the methods allowed,
each with whether it could be read. An operation a host lacks returns `ErrUnsupported`, and
`jst land` says which one and stops; it never falls back silently. The code in `internal/gh` and
`internal/githubapi` that does this today becomes the GitHub adapter.

| operation | GitHub | GitLab | Bitbucket Cloud |
|---|---|---|---|
| change, open | pull request | merge request | pull request |
| checks | check runs and commit statuses on the head and the test merge | pipelines and commit statuses | build statuses |
| rules | rulesets and branch protection | protected branches, "pipelines must succeed" | branch restrictions, merge checks |
| update branch | `update-branch` with `expected_head_sha` | none that merges: unsupported | none: unsupported |
| merge, expected head | merge with `sha` | accept with `sha` | merge; no expected head: unsupported in `merge-when-green` |
| native queue | merge queue (`enqueuePullRequest`) | merge trains | none |

GitHub ships in 0.6.0; the GitLab and Bitbucket columns show the interface fits, not their design.

## 3. `jst land`

```
jst land [--change N|URL] [--title T] [--body-file F] [--card ID]... [--reason TEXT]
         [--prepare | --confirm] [--plan] [--timeout S] [--interval S] [--lines N]
jst land status [--change N|URL]
jst land find (--head BRANCH | --search TEXT) [--repo URL] [--state STATE] [--wait S]
```

The plan is printed first; `--plan` stops there. The steps are `pr-land`'s, kept with its safety
checks.

### 3.1 Steps

1. **Push and open.** The work tree is clean and not on the base; this checkout's branch has
   nothing the head lacks. New commits are pushed (every push destination verified); a change is
   opened when none is, with `--title` and the card line. A draft is not merged.
2. **Policy.** The paths the change touches (merge base to head), against the fixed human paths
   and `human_paths` read from the base's file and from the change's, so a change that adds or
   edits the `land` section is judged by both. Any match makes the change a person's (section
   3.3). The decision is bound to that base and head.
3. **Checks.** Wait for the required checks, judged by section 4.
4. **Freshness.** When the base's rules require the branch to be up to date and it is behind,
   update it on the host and go back to step 2.
5. **Recheck.** After the local checks of the description (below), read the rules and the checks
   afresh, the base's tip on the host, and last the change. A moved head, base, test merge or
   description, a retarget, changed rules, or a required check no longer passing stops with
   `moved`; the next run starts at step 2. Every read of the change is bound to the base the plan
   named.
6. **Merge.** Ask the host to merge with the head and the description from step 5 as the merge
   commit's message; with `--confirm` on a base that requires a queue, enqueue it instead. A lost
   answer is settled by reading the change, never by a second request.

The description is the merge commit's message, and a check read by commit cannot vouch for one
version of it. So where the base's file has `cards` with `unit` `pr`, `jst land` judges the Card
line of the exact description it sends, and where it has `changes`, the changelog rule (D1), on the
in-memory merge of the head with the judged base (pr-land's safety check, kept): at the opening and
before the recheck. A base whose Card line is in the description refuses `method: rebase`, which
lands no description.
7. **Clean up.** Delete the remote branch only under today's lease and checks (merged at that head,
   no other open change on it, push destinations verified). `jst tidy` removes the local branch.

### 3.2 Exit status and the last line

`jst land` ends with `jst land: STATE` on stderr, so `status` output stays clean. Only `landed`
exits 0 with the change merged, so `jst land && jst tidy …` never runs on an unmerged change.

| state | exit | when |
|---|---|---|
| `landed` | 0 | merged; with `--confirm` on a base with a queue, merged by the queue within the wait |
| `ready for a person` | 6 | `prepare`, `--prepare`, a human path, a base that requires a merge queue, a base that requires no check or whose rules cannot be read |
| `planned` | 0 | `--plan` |
| `refused` | 1 | a required check failed or was cancelled; a draft; a conflict; an unsupported operation |
| `invalid` | 2 | usage, configuration, no account |
| `pending` | 3 | checks or the queue still pending at the deadline |
| `moved` | 4 | head, base, test merge or description moved, or the change was retargeted |
| `unexpected` | 5 | a host answer jackstay does not expect |

### 3.3 Who may merge

The agent merges only in `merge-when-green`, only a change that touches no human path, and only on
a base that does not require the host's merge queue (a queued change merges later, without
jackstay's policy: no auto-merge). For any other change `jst land` stops ready for a person, who
either merges or queues it on the host's page or runs `jst land --confirm` at a terminal and types
the change's number. A person's change still goes through steps 1 to 5 before the stop, so it is
handed over with its checks passed and its description judged (a failing one is refused, not
handed over); `--confirm` always asks for the number, repeats steps 2 to 5
after it is typed, then merges (or queues). `--confirm` is refused without a terminal and with
`--prepare`.

Three things keep that true for an agent:
- **`jst lint pr`, rule D0**, keeps a change to `jackstay.json` alone in its pull request, judged by
  the base's file (W-047). Adopt writes the workflow that runs `jst lint pr` for every project with
  a `land` section, also one without `cards` or `changes`. Doctor reports that workflow missing,
  or base rules that require no `card-line` check; that D0 is enforced, a person confirms in the
  host's branch rules (section 5).
- **`jst land`** reads human paths from the base and the change (step 2), and autonomy from the
  base only. A change that introduces the first `land` section touches `jackstay.json`, so it is a
  person's, and it cannot authorise its own merge: the base it lands on has no policy yet.
- **The guard** refuses `gh pr merge` and `glab mr merge`, naming `jst land`. The rule is on in
  every project the 0.6.0 guard runs in, whether or not a `land` section exists, so the first policy
  cannot be merged that way either. It is configurable like every guard rule
  ([W-055](../decisions/W-055-every-guard-rule-configurable-now.md)).

The guard is against accidents ([W-017](../decisions/W-017-review-fixes-and-threat-model.md)): an
agent calling the host's API directly is outside it.

## 4. Required checks

- **Required** (by the base's rules, plus any listed in `checks`): each must pass or be skipped on
  the commit the host judges: the test merge once it has results, else the head (as
  `withMergeCommitRows` does today). Pending and not yet registered are waited for; a failure or a
  cancellation stops.
- **Not required**: never waited for, never blocking; a failure is reported. This changes 0.5.11,
  where any failing check stopped `pr-land`.
- **Superseded** ([#13](https://github.com/zima-oss/jackstay/issues/13)): a cancelled run with a
  newer run of the same workflow, commit, event and change is judged by the newer run's own job of
  the same name. While that job has no result it is pending; a newer run that finished without it
  leaves the check cancelled. A run's overall conclusion never decides a check.
- **Rules unread** (a plan or permission refuses them): which checks are required cannot be known.
  With `checks` listed, those are required and every other check is reported only. With none
  listed, `jst land` waits for the checks to settle, reports them, and stops ready for a person
  (exit 6), saying the rules could not be read. It never turns an optional check into a blocker.
- **Rules read and requiring none**, `checks` empty: CI is not a gate for this base, so
  `merge-when-green` stops ready for a person, saying so in the plan; doctor reports it as an
  error. This is the GitHub default for a new repository. The checks that do register are waited
  for and reported; when none registers within 120 seconds there is nothing to wait for, and it
  stops ready for a person then rather than at the deadline.

## 5. Adopt, setup and doctor

`jst adopt` asks one question, recommended answer first: "The agent merges when the required
checks pass; `jackstay.json` and the listed paths need a person (recommended)" or "The agent
prepares; a person merges." Without a terminal, `--apply` needs `--autonomy` or an existing
`land.autonomy`. `jst setup` asks the same once per machine and keeps it as the answer adopt offers
first; the project's file decides.

The upgrade from 0.5.x is three pull requests, planned by adopt and merged by a person: the pin
(schema 2), the workflows that download 0.6.0, then schema 3 with the `land` section. 0.6.0 reads
schema 2 without a `land` section. Whether the upgrade is due is read from the project's file, and
a schema-3 key waits for the third step. The second step is done when adopt's plan writes no
workflow, and, when the project keeps any workflow adopt does not manage, a person has confirmed
with the exception `upgrade:workflows` that none still downloads an older jackstay, directly or
through a script or an action; until then the plan, the apply and doctor name the blocker.

`jst doctor` reports: the `land` section valid; the account connects; the rules readable, with the
checks they require (none: a note); a missing lint workflow (`.github/workflows/card-line.yml` in
the checkout; adopt writes it) and readable rules that require no check named `card-line`, both
problems; whether D0 is enforced is the person's to confirm, in one standing note, since jackstay
does not verify that a required check runs `jst lint pr` on the base and fails with it (and pull
requests from forks skip the adopted job); `method` allowed; a required queue; `gh pr merge` refused by the guard; and, from section 7, the CI-covered scopes the rules do
not require.

## 6. What is removed

| 0.5.x | 0.6.0 |
|---|---|
| `jst gh pr-land` | `jst land` (`--pr` is `--change`, `--dry-run` is `--plan`, `--no-merge` is `--prepare`) |
| `jst gh pr-status`, `pr-find` | `jst land status`, `jst land find` |
| `jst gh ci-wait`, `ci-status`, `run-wait` | `jst checks wait`, `jst checks status`, `jst checks run-wait` (same flags) |
| `jst queue …`, the `queue` key (never released) | removed; the host's queue through `jst land` |
| `jst receipts attest-ci`, `verify-ci` (never released) | removed; provenance stays in release and install |

A removed command is unknown: the error names its replacement and runs nothing. The upgrade page
lists them and the change of section 4.

The AGENTS block: the `github`, `queue` and `ci-evidence` items go; a `land` item comes, its text
following `land.autonomy` ("`jst land` … stops at `jst land: ready for a person` for `jackstay.json`
and `land.human_paths`: say so and stop; never merge on the host, never run `--confirm`"); the
`checks` item names `jst checks wait`/`status`/`run-wait`.

## 7. Where tests run: one graph of scopes, cached by their inputs

A run of `jst test` runs each scope at most once, after the scopes it depends on, and skips a scope
whose inputs already have a counted pass in the cache. The project declares which scopes CI runs
and on which systems; CI is the gate for those, and a person's machine runs the rest. This follows
[W-096](../decisions/W-096-task-graph-and-input-cache.md): a task graph and a cache by input hash,
on the model of monorepo task runners, each with simple behaviour and written limits. It replaces
the ordered lists of `steps`, the gate's step marks and bars, and `--resume` (section 7.7).

**Prior art.** Nx runs a target after the targets named in its `dependsOn` and hashes a task from
its `inputs`, its command, the runtime and the hashes of what it depends on
([run tasks](https://nx.dev/docs/features/run-tasks),
[inputs](https://nx.dev/docs/reference/inputs),
[cache task results](https://nx.dev/docs/features/cache-task-results)). Turborepo builds a task
graph from `dependsOn` and keys each task by its `inputs`, its `env`, a global hash and the hashes
of its dependencies, replaying the logs on a hit
([configuring tasks](https://turborepo.dev/docs/crafting-your-repository/configuring-tasks),
[caching](https://turborepo.dev/docs/crafting-your-repository/caching)). Bazel keys an action by its
command line, the digests of its inputs, its environment and its platform
([remote caching](https://bazel.build/remote/caching)). jackstay takes the graph and the key, and
leaves out what a test runner does not need: outputs, named inputs, parallel runs and a remote
store.

### 7.1 The keys of a scope

| key | value | default | meaning |
|---|---|---|---|
| `deps` | scope or step names | `[]` | what must pass first, in the order given |
| `ci` | systems | `[]`: not in CI | the systems CI runs this scope on as a job of its own |
| `systems` | systems | any | the systems the scope runs on at all |
| `inputs` | repository-relative globs | every tracked file | the tracked files whose content enters the scope's hash |
| `cache` | boolean | `true` | `false`: the scope always runs; nothing is read from or written to the cache for it |

A system is Go's name for it: `darwin`, `linux`. The five keys need `"jackstay_schema": 3`
([W-090](../decisions/W-090-compatibility-of-0-6-0.md)).

```json
"scopes": {
  "x-build": {"run": ["just", "build"], "judge": "exit", "cache": false},
  "x-go":    {"run": ["just", "go"], "deps": ["x-build"]},
  "x-docs":  {"run": ["just", "docs"], "judge": "exit", "inputs": ["docs/**", "site/**"]},
  "x-ios":   {"run": ["just", "ios"], "deps": ["x-build"], "systems": ["darwin"]},
  "gate":    {"deps": ["x-docs", "x-go", "x-ios"], "ci": ["linux"]}
}
```

**A scope with no `run`** (a composite, such as `gate`) runs nothing of its own. It passes when
every dep that runs on this system passed, and at least one did; it takes no `judge`, `timeout`,
`lock_wait`, `lock`, `inputs` or `cache`. It may take `requires`, `ci`, `systems` and `when`.

**`steps` is read as `deps`.** A scope's `steps` list is its `deps` in the same order, in schema 2
and 3, so a schema-2 project keeps working unchanged; a scope with both is an error in
`jackstay.json`. A name in the `steps` section is a node of the graph like a scope. A list of steps
with no deps between its members runs in its declared order and stops at the first failure, as in
0.5.x; what changes is that a step two lists share runs once, and that `--resume` is gone. The
0.5.x key `"resume": false` is read as `"cache": false`.

`jst check` refuses an unknown name in `deps`, a cycle (naming it), `steps` and `deps` together,
a scope with `run` whose dep has `systems` that leave out one of the scope's own systems (it could
never run there), and a dep that needs test arguments (`{tests}` or a typed selector). Each refusal
is also `not-started` (2) before anything runs. `jst check` warns about a scope with `run` that
another scope depends on and that has no `cache` key (section 7.3, "Verdicts only").

### 7.2 The run

1. **The graph.** The scopes asked for, with their deps, and the deps of those, to the end. Each
   name is one node, however many scopes depend on it.
2. **The order.** A depth-first walk from the scopes asked for, in the order given, visiting each
   node's deps in their declared order and listing a node after its deps; a node already listed is
   not visited again. So the order follows the declarations wherever the graph allows, and `steps`
   lists keep their 0.5.x order. With the example above, `jst test gate` runs `x-docs`, `x-build`,
   `x-go`, `x-ios`, then judges `gate`.
3. **One at a time.** Nodes run one after another, in that order, each under its own `timeout`,
   lock and leftovers (testing.md sections 3 and 6). No two run at once in 0.6.0.
4. **Each node, in turn**, is settled by the first of these that applies:
   1. **Not on this system**: its `systems` leave out this machine, or it has `run` and a dep is
      `skipped`. It is `skipped`. A `skipped` dep is not a failed one: a composite ignores it
      (section 7.1), and nothing else is stopped by it.
   2. **A dep did not pass**: a dep ended other than `passed`, `exited-0` or `skipped`. It is
      `not-run`.
   3. **`requires`**: its checks run, every time, before the cache is looked at. One that fails
      makes the node `not-started`, and the cache is neither read nor written for it. A composite's
      `requires` run before its first dep, with the same result.
   4. **The cache** (section 7.3): an entry of its hash is found. It does not run and keeps the
      stored outcome, marked `cached`.
   5. Otherwise it **runs**.
5. **After a failure.** A node that did not pass (anything but `passed`, `exited-0` or `skipped`,
   cached or not) is a failure. Without `--keep-going`, the first failure ends the run and every
   node after it is `not-run`. With `--keep-going`, the walk goes on and step 4 settles each node as
   usual, so work that does not depend on the failure still runs. Either way the run keeps the
   failure as its outcome. `--keep-going` changes nothing else.

A node's outcome is one of `jst test`'s outcomes (testing.md and implementation.md section 9.3),
or one of two that only a graph has:

| outcome | when | a pass? |
|---|---|---|
| `skipped` | not on this system (step 4.1): ignored by a composite, not a failure | no |
| `not-run` | a dep did not pass (step 4.2), or an earlier failure ended the run (step 5) | no |

The last `jst test:` line reads the run as a whole:

- **`passed`** when every scope asked for passed: by running now, or `cached` (the cache holds
  only counted passes of identical inputs, so a cached pass is the same claim), with `skipped` deps
  of a composite named. Only `passed` is a pass, as before; the detail says how much ran now:
  `jst test: outcome=passed (gate, 41 s: x-docs=exited-0 cached, x-build=exited-0,
  x-go=passed, x-ios=passed)`. When nothing ran now, the detail says `nothing ran now: every scope
  cached`. A scope asked for that is exit-judged ends `exited-0`, cached or not.
- **The failure** otherwise, by the order of testing.md section 2 (`failed` > `timed-out` >
  `stopped` > `unjudged` > `nothing-run`, then `not-started`), naming the node: `outcome=failed (gate failed at x-go)`.
- **`skipped`** (exit 79) when a scope asked for is `skipped` and nothing failed: it does not run
  on this system.

`jst test plan SCOPE` prints the order, each node's hash, and whether it would be cached, skipped
or run; it runs nothing.

### 7.3 The cache

A node with `run` has a **hash**: SHA-256 over

- its definition after defaults and placeholders: the expanded words (so a new `{slow}` list is a
  new hash), `cwd`, `env` names and values, the judge with its `evidence`, `veto`, report settings
  and adapter version, `timeout`, `leftovers` of the section, and `requires`;
- the content of its `inputs` at HEAD: each matching tracked file's path, mode and blob id, so no
  file is read; plus the content of `receipts.extra_files`;
- what the receipts key reads of the machine (implementation.md section 9.2): the output of each
  `receipts.toolchain` command, the variables matching `receipts.env`, the system's version and
  build, the machine kind; and jackstay's version;
- the commits of `receipts.refs` and the command's `refs`, HEAD's merge bases with
  `receipts.merge_bases` and the command's `merge_bases`, and, for a `{base}` in its words, the
  base's tip and its merge base with HEAD;
- the hash of each dep, in declared order. A composite dep contributes the hashes of its own deps.

HEAD's commit is not in the hash: two commits whose inputs are the same share an entry, so a scope
that passed on a branch is cached on the merge commit with the same tree.

**Only a clean checkout reads or writes the cache.** Clean means: `git status --porcelain
--untracked-files=all` is empty; no entry is marked skip-worktree or assume-unchanged; and no
untracked file is hidden only by a rule outside the committed `.gitignore` files
(`.git/info/exclude`, a global excludes file), except under an agent's top folder (`.claude`,
`.omc`, `.omx`, `.codex`, `.cursor`), under a regenerable folder (`.build`, `DerivedData`,
`node_modules`, `__pycache__`, …) or a `.DS_Store`. A checkout that is not clean runs every node
and says `cache=off (not clean)`; a copy made by `--commit` is always clean. Hashing the working
files instead would let uncommitted work enter the cache under a commit's name; jackstay does not
do that.

**No hash**, so no cache for the node and every node that depends on it, said in the line: a ref
that cannot be resolved, a toolchain command that fails, `receipts.toolchain` or `receipts.env`
not written out, or git failing.

**A hit.** Looked up only after the node's `requires` passed (section 7.2, step 4). An entry of
this hash with outcome `passed` (or `exited-0` for an exit-judged node), at most
`receipts.max_age_hours` old, with no failure note of the hash: the node is not run. The run
record gets a `cached` line naming the run that stored the entry; that run's output is named when
it is still kept. No log is replayed. The lookup is made before the lock and again under it.
`--no-reuse` or `JACKSTAY_NO_REUSE` skips the lookup; the run still writes.

**An entry is written** under the rules of a receipt (implementation.md section 9.4): the node
`passed` or ended `exited-0` for an exit-judged node; the checkout was clean before and after it,
with the same HEAD and hash; no tracked file or `receipts.extra_files` entry was rewritten while it
ran, even to the same content (identity, size and times compared); and no failure note of the hash
is newer than its start.

**A failure is never cached.** A node that ends `failed` or `passed-on-retry` writes a failure note
of its hash; while the note lives (`receipts.max_age_hours` and one more day), the hash gets no
entry and every older entry of it is unused. So the node runs every time until its inputs change,
and a rerun that passes is reported `passed` but cached on no hash that failed
([W-013](../decisions/W-013-quality-testing-declarative.md)). A node that ends `timed-out`,
`unjudged` or `stopped` writes neither.

**Verdicts only.** An entry records that a scope passed, never what it made: no output, no build
product. So a scope whose files other scopes use, such as a build, sets `"cache": false`: a hit
would skip it and leave the scopes after it without its files.

**The store.** `<jackstay home>/<project>/cache/`: one entry per hash in the receipt's format,
beside its failure notes. An entry is a few hundred bytes. It is pruned like receipts, by age, and at 10,000 entries per project (a constant)
the oldest go first. In CI the same folder is kept in CI's own cache store: the workflow adopt
writes restores it before the run and saves it after, under a key holding the system, so a rerun of
a workflow or a merge commit with the same inputs skips what passed.

**Not in the hash**, written down as limits: files ignored by a committed `.gitignore` (unless in
`receipts.extra_files`); anything outside the checkout (another package by path, a simulator, the
keychain); the network; variables not declared in `env` or matched by `receipts.env`; the date and
time; flakiness. A scope that depends on one of these declares it (`inputs`, `receipts.env`,
`receipts.extra_files`) or sets `"cache": false`. A `"cache": false` node still has a hash, for the
nodes that depend on it. An entry is unsigned: any session of the account can write one, as it can
a receipt ([W-017](../decisions/W-017-review-fixes-and-threat-model.md)).

**Open: cache across machines.** Whether a cached pass may cross between a person's machine and CI
is not decided. The options:

- **A. No crossing** (recommended). Each machine and CI keeps its own store; a pass counts only
  where it was counted.
- **B. CI to a person's machine.** CI publishes each scope's hash and outcome on its check, and
  `jst test changed` takes a pass CI reports for the same hash.
- **C. A person's machine to CI.** CI takes a local pass for the same hash; this needs signed
  receipts, since CI cannot trust an unsigned file.
- **D. Both B and C.**

Everything in this section works under A without change; B, C and D would add a reader of another
store, not change the hash.

### 7.4 Local and CI

A scope is **covered on a system** when it runs in CI there: its own `ci` lists the system, or a
scope covered on that system depends on it; and its `systems` include that system. A scope is
**covered** when it is covered on some system.

**Three groups.** `jst test changed` and `jst test uncovered` expand the graphs of their selected
scopes and put every node with `run` (a composite only leads to its deps) in exactly one group, the
first that fits:

1. **here**: it runs on this system (its `systems`) and is not covered in the command's sense;
2. **ci**: it is covered on some system;
3. **elsewhere**: neither; it is not covered, and its `systems` leave out this machine.

| command | selected scopes | not covered, for **here**, means |
|---|---|---|
| `jst test changed` | the scopes matched by the changed paths | covered on no system |
| `jst test uncovered` | every scope that needs no test arguments | not covered on this system |

The **here** nodes run now through the graph (section 7.2), each with its deps from any group. The
last line names the three groups:
`jst test: outcome=passed (changed, 40 s; here: x-build=exited-0, x-ios=passed; ci: x-docs, x-go;
elsewhere: none)`. Its outcome, the first that applies:

| outcome | when | exit | a pass? |
|---|---|---|---|
| a failure | a **here** node or a dep of one did not pass (section 7.2) | its own | no |
| `needs-other-system` | `jst test changed` only: **elsewhere** is not empty; the line names each system and its scopes | 80 | no |
| `passed` | **here** is not empty and passed | 0 | yes |
| `left-to-ci` | **here** and **elsewhere** are empty: CI runs everything selected | 0 | no |

`needs-other-system` before a pull request tells the person that a test of the change runs on no CI
system and not on this machine: it needs another machine (`needs-other-system (darwin: x-ios)`).
`jst test uncovered` lists **elsewhere** without changing its outcome, since an install vouches for
this system only.

Examples with the scopes of section 7.1 (`gate` has `ci: ["linux"]`, `x-ios` runs only on
`darwin`), for a path no rule matches, which selects `gate`:

- **`jst test changed` on a Mac**: **here** is `x-ios`, which runs after `x-build`; **ci** is
  `x-build`, `x-docs`, `x-go`; the outcome is `passed` when they pass.
- **`jst test changed` on Linux**: **here** is empty; **elsewhere** is `x-ios`; the outcome is
  `needs-other-system (darwin: x-ios)`. A change only to `docs/` selects `x-docs`, which is in
  **ci**: `left-to-ci`.
- **`jst test uncovered` on a Mac**: nothing is covered on `darwin`, so **here** is every node:
  the whole gate.
- **`jst test uncovered` on Linux**: **here** is empty, since CI covers the rest on Linux; the
  outcome is `left-to-ci`, and `x-ios` is listed under **elsewhere**.

Where each command is used:

- **Before a pull request**: `jst test changed` matches and ranks scopes by the changed paths as
  today (`test.selection.order`, `gate` for a path no rule matches).
- **Before an install or a tag**: `jst test uncovered`, on a clean copy of the commit
  (`--commit REV`, default HEAD). Its `passed` or `left-to-ci` record, for that commit, the project
  named by that commit's `jackstay.json` and this system, is what `jst install` from a checkout
  needs: without it, the install is refused and names `jst test uncovered`. For jackstay on a Mac
  that is the whole gate: CI covers Linux only ([W-083](../decisions/W-083-no-macos-job-in-ci.md)).
  Before a tag, a full gate on a Mac, including `jst test uncovered`, runs on the version-bump
  commit.
- **In CI**: the covered scopes run in jobs whose names the base's rules require; `jst doctor`
  reports such a job the rules do not require. How scopes map to jobs is open (below).
- **The AGENTS block** states the split, generated from the scopes: "CI runs x-go, x-check, x-sec,
  x-docs on Linux and is the gate. Before a pull request: `jst test changed`. Before an install or
  a tag: `jst test uncovered`."

**Open: CI jobs and the graph.** How the scopes with `ci` become CI jobs is not decided. The
options:

- **A. One graph job per system** (recommended). One job per system, named `jst test (SYSTEM)`,
  runs the graph of every scope whose `ci` lists that system, so each node runs once on that system
  by construction.
- **B. One job per scope, shared nodes refused.** Each scope with `ci` runs as `jst test SCOPE
  (SYSTEM)`, and `jst check` refuses two jobs of one system whose graphs share a node; the remedy is
  one composite with `ci`.
- **C. One job per scope, the cache deduplicates.** As B without the refusal: a shared node is
  skipped by a job that finds the other job's entry in CI's store, which jobs running at once do
  not guarantee.

Everything else in this section holds under each option; only the job names doctor checks and the
`jst check` rule of B depend on it.

### 7.5 What it does not do

- Run two nodes at once, or split one scope into shards: one node at a time.
- Keep what a scope makes. The cache holds verdicts only (section 7.3); a scope whose files other
  scopes use sets `"cache": false` and runs every time.
- Find overlap between scopes: `quick` inside `full` both run when both are asked for; the graph
  removes repeats of a scope, not of a test.
- Keep a test from running twice in CI. jackstay schedules each scope once, but cannot see inside
  its command. Scopes that CI runs on one system must test disjoint sets: one scope must not rerun
  another's tests, as an integration scope that also runs the unit tests would. That is the
  project's duty; jackstay does not check it.
- Read a store other than this machine's or CI's own (section 7.3, "Open").

### 7.6 jackstay's own scopes

The configuration jackstay applies to itself with the code, so that no test runs twice on one
commit in Linux CI. Each scope with `ci` tests a set no other one does:

```json
"scopes": {
  "x-go":    {"run": ["go", "test", "-race", "-tags", "integration", "-json", "-count=1", "./..."],
              "judge": "go-json", "ci": ["linux"]},
  "x-check": {"run": ["just", "check"], "judge": "exit", "ci": ["linux"]},
  "x-sec":   {"run": ["just", "security"], "judge": "exit", "ci": ["linux"]},
  "x-docs":  {"run": ["just", "docs"], "judge": "exit", "ci": ["linux"]},
  "gate":    {"deps": ["x-check", "x-go", "x-sec", "x-docs"]}
}
```

- `x-go`: the unit and the integration tests, each once, with the race detector; the
  `internal/docs` Go tests are among them.
- `x-check`: `gofmt`, `go vet` and the linter; no test.
- `x-sec`: `govulncheck` and `gosec`; no test.
- `x-docs`: the site build and its link check only; the `docs` recipe no longer runs `go test`.
- `gate`: the local composite, with no `ci`. On a Mac it runs `x-go` too, which tests the
  `*_darwin.go` code no CI job reaches; that is what `jst test uncovered` runs there.

### 7.7 What goes away

| 0.5.x | 0.6.0 |
|---|---|
| `steps`: a list run in order, expanded into its leaves, a shared step run each time it appears | read as `deps`; each node once |
| `jst test gate --resume` and the step mark of a leaf (`gate-marks/`) | the cache, read on every run of a clean checkout; `--resume` is an unknown flag whose error names the cache |
| bars (`gate-bars/`) and the `refused` outcome | failure notes: a hash that failed is never cached and runs again |
| the gate's `resumed` outcome | `cached` on each node; the run is `passed` |
| the `resume` key | `cache` (`"resume": false` read as `"cache": false`) |
| receipts reuse for a scope whose words match a `receipts.commands` entry | the cache; `jst test -- COMMAND` keeps receipts |

## 8. Tests

- **Fake adapter**: scripted answers for each operation, `ErrUnsupported`, and failures.
- **Checks**: required pass, fail, cancelled, superseded (newer job pending, passed, absent),
  missing; not required failing; rules unread; rules requiring none.
- **Policy**: `jackstay.json` alone, a human path, a change adding the first `land` section, the
  base with and without `land`, a branch whose file says `merge-when-green` on a `prepare` base;
  `prepare`, `--prepare`, `--confirm` without a terminal; rules unread with and without `checks`;
  rules requiring none.
- **Races**, deterministic: head moved before the merge, base moved during the wait, a check rerun
  red before the recheck, a lost merge answer with the change merged, a queue that holds it, a
  retarget, a description edited after the checks.
- **Guard**: `gh pr merge` and `glab mr merge` refused with and without a `land` section.
- **The graph**: a dep shared by two scopes runs once; the order of section 7.2 for a diamond, for
  `steps` lists (0.5.x order kept) and for several scopes asked for; a cycle, an unknown dep,
  `steps` with `deps`, and a dep on fewer systems refused by `jst check` and `not-started` (2); a
  failure stops the run and leaves the rest `not-run`, and `--keep-going` runs only the nodes whose
  deps passed; a composite passes when its deps passed, with a `skipped` dep named, and is `skipped`
  (79) when all are; a node with `run` after a `skipped` dep is `skipped`; a dep `skipped` for
  `systems` neither stopping the run nor failing a composite; `--keep-going` running the nodes that
  do not depend on a failure and nothing else changed; `requires` checked before every cache
  lookup, a failing check making the node `not-started` with the cache neither read nor written,
  and a composite's `requires` checked before its first dep; a schema-2 file with `steps` and
  `"resume": false` read unchanged.
- **The cache**: a hit for each passed and each exit-judged node, the run `passed` with `nothing ran
  now` when all hit; a miss for a change of each part of the hash (an input file, a file outside
  `inputs` not, the expanded words, `env`, a toolchain line, a `receipts.env` variable, jackstay's
  version, a ref, `{base}`, a dep's hash); the same entry used on two commits with one tree; no read
  or write on a checkout that is not clean, nor without a hash; no entry when a file is rewritten
  during the run or the checkout stops being clean; a failure note keeps a later pass of the hash
  from the cache; `timed-out`, `unjudged`, `stopped` write nothing; `--no-reuse` and
  `JACKSTAY_NO_REUSE` skip the read and still write; `"cache": false`; age and count pruning.
- **Where tests run**: covered through `ci` and through a dependent, and not on a system its
  `systems` leave out; every node in exactly one of **here**, **ci** and **elsewhere**, for
  `jst test changed` and `jst test uncovered`, with the four examples of section 7.4 as tests;
  **here** running with deps from any group; `needs-other-system` (80) only from `jst test changed`
  and only after **here** passed; `left-to-ci` only when **here** and **elsewhere** are empty;
  `jst test uncovered` leaving out selector scopes, on a clean copy; `jst check` refusing a dep
  that needs test arguments and warning about a depended-on scope with no `cache` key; the jobs of
  the option chosen for CI;
  install refusing without its `passed` record for the commit, project and system; doctor reporting
  a `jst test SCOPE (SYSTEM)` job the rules do not require.
- **GitHub pilot**, by hand in a disposable repository with synthetic content: required checks,
  a private repository whose rules cannot be read, update-branch, the native merge queue, and the
  concurrency cancellation of #13.
- **Adopt**: golden files for both modes; the three-step upgrade from 0.5.11.
