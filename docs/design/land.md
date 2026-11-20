# `jst land`: a thin landing behind a small hosting adapter; where tests run

Status: for 0.6.0; sections 1 to 6 are built, 7 is not yet (so doctor does not yet report the
CI-covered scopes of section 5). It follows
[W-090](../decisions/W-090-landing.md) (one forge-neutral `jst land`, hosting adapters, two autonomy
modes chosen explicitly) and [W-092](../decisions/W-092-thin-land-and-where-tests-run.md) (the
landing stays thin; where tests run is the project's declared split between local and CI). It keeps
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
  a `land` section, also one without `cards` or `changes`, and doctor checks the base's rules
  require it.
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
schema 2 without a `land` section.

`jst doctor` reports: the `land` section valid; the account connects; the rules readable, with the
checks they require (none: a note); the check of the job that runs `jst lint pr` required, the job
found in the checkout's workflows (its `name:`, else its ID; none is a problem; several, a name
with an expression or a matrix, or rules unread leave it unverified); `method` allowed; a required
queue; `gh pr merge` refused by the guard; and, from section 7, the CI-covered scopes the rules do
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

## 7. Where tests run

CI is the gate, but not every test runs in CI: jackstay itself has no macOS runner, so its macOS
code is tested only on a Mac. The project declares the split; jackstay does not fix it.

A test scope gains one key: `ci`, the operating systems on which CI runs it (absent: not in CI).

```json
"scopes": {
  "x-go":  {"run": ["just", "go"], "ci": ["linux"]},
  "x-ios": {"run": ["just", "ios"], "systems": ["darwin"]}
}
```

- **Before a pull request**: `jst test changed` ranks the scopes matched by the changed paths as
  today (`test.selection.order`, `gate` for a path no rule matches) and picks the strictest. That
  scope runs locally unless it has `ci`. Every other matched scope with no `ci` also runs locally,
  since a stricter covered scope will run only in CI. A composite scope such as `gate` counts as
  covered only through its own `ci`. Example, order `x-go`, `x-ios`, `gate`, `x-go` with
  `ci: ["linux"]`: a change to Go and Swift files picks `x-ios`, which runs locally, and leaves
  `x-go` to CI; with the order `x-ios`, `x-go`, `gate`, `x-go` is picked and left to CI, and
  `x-ios` still runs locally.
- **In CI**: each covered scope runs as one job per listed system, named `jst test SCOPE (SYSTEM)`,
  and the base's rules require those jobs.
  `jst doctor` reports a covered scope the rules do not require.
- **Before an install or a tag**: `jst test uncovered` runs, on the exact commit, every scope whose
  `ci` does not include this machine's operating system, of those that run without arguments (a
  selector scope that needs a test ID is left out) and that run here (a scope may name the systems
  it runs on in `systems`; absent: any). `jst release` and `bin/jst install` refuse
  without its passing receipt for that commit. For jackstay, on a Mac, that is the whole gate:
  CI covers Linux only ([W-083](../decisions/W-083-no-macos-job-in-ci.md)).
- **The AGENTS block** states the split, generated from the scopes: "CI runs x-go, x-integration,
  x-check, x-docs on Linux and is the gate. Before a pull request: `jst test changed`. Before an
  install or a tag: `jst test uncovered`."

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
- **Where tests run**: `jst test changed` requiring only uncovered scopes; `jst test uncovered` on
  Linux and macOS; release and install refusing without its receipt.
- **GitHub pilot**, by hand in a disposable repository with synthetic content: required checks,
  a private repository whose rules cannot be read, update-branch, the native merge queue, and the
  concurrency cancellation of #13.
- **Adopt**: golden files for both modes; the three-step upgrade from 0.5.11.
