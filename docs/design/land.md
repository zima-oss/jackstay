# `jst land`: one declarative landing, behind a hosting adapter

Status: specification for 0.6.0, not built. It follows the owner's ruling W-090 for 0.6.0 (one
forge-neutral `jst land` with a declarative policy and hosting adapters, GitHub first; two autonomy
modes chosen explicitly at adopt and setup; approval checked by jackstay itself, with publishing to
the host as an optional switch; CI provenance through GitHub attestations), and keeps
[W-047](../decisions/W-047-release-redesign-fixed-settings-no-one-off-imports.md) (`jackstay.json`
changes alone and is a maintainer's to merge), [W-019](../decisions/W-019-repeated-findings-mean-redesign.md)
(this checkout is never switched, pulled or rebased by a landing) and
[W-013](../decisions/W-013-quality-testing-declarative.md) (declarative, nothing named after a
project). It replaces `jst gh pr-land`, the unreleased `jst queue` and the unreleased
`jst receipts attest-ci`/`verify-ci`, and the pilot specification
[merge-queue-pilot](../roadmap/merge-queue-pilot.md) for everything but batching.

## What it removes

| today (d542f11) | what goes wrong | here |
|---|---|---|
| `jst gh pr-land` merges, but reads no local evidence (gate, review) | a green host and an unreviewed, ungated change land together | one engine: policy, then evidence, then the local gate, then the merge (section 4) |
| `jst queue` checks local evidence, but never merges, and its PR mode needs an approval by someone other than the author (`internal/mergequeue/policy.go`) | two half-flows; a solo maintainer cannot use the queue | the queue's evidence becomes `jst land`'s preflight; jackstay checks approval itself (section 4.3) |
| `pr-land`'s `updateBehind` merges the base on GitHub and moves the head; a queue lane on that head is stale, which the queue reports only when asked | two records of one change that disagree | every attempt is one record; any moved input starts a new attempt (section 5) |
| every call is written against GitHub's API (`internal/githubapi`, `internal/gh`, `internal/mergequeue`, `internal/release`) | GitLab and Bitbucket mean a rewrite | a hosting adapter with declared capabilities (section 3) |
| a failed check that no rule requires stops `pr-land` | an optional job blocks a merge the rules allow | only required checks block; the others are reported (section 6) |
| a run cancelled by its concurrency group ([#13](https://github.com/zima-oss/jackstay/issues/13)): `reconcileCancelled` infers a newer run from its metadata, but a replacement that finished red marks the check failed without its own job's result | a check judged by another job's result | a neutral `superseded` state, judged by the newer run's own job only (section 6.2) |
| five shapes of landing evidence (section 5.1) | nothing to show afterwards what landed and why | one versioned landing record (section 5) |
| `receipts attest-ci`/`verify-ci` with jackstay's own Ed25519 key, no producer and no consumer | a key to keep and nothing that uses it | the host's provenance (GitHub artifact attestations) through the adapter (section 3.5) |

What stays the same: the push, the pull request's opening with its card line, the merge with an
expected head, the lease on deleting the remote branch, and that this checkout is left as it is.

## 1. The `land` section of `jackstay.json`

The section needs `jackstay_schema: 3`, so a jackstay older than 0.6.0 says a newer one is needed
instead of misreading it. Every key has a default, and `jst adopt` writes the section in full
(section 8), so what a project does is read in its file, not remembered from a default.

```json
"land": {
  "host": "github",
  "autonomy": "merge-when-green",
  "method": "squash",
  "checks": "rules",
  "gate": true,
  "review": true,
  "base": "merge-tree",
  "human_paths": [".github/workflows/**", ".claude/settings.json"],
  "via": "direct",
  "publish_statuses": false
}
```

| key | values | default | meaning |
|---|---|---|---|
| `host` | `github` (0.6.0); `gitlab`, `bitbucket` later | from the push remote's host | the adapter (section 3). Its connection settings stay in the adapter's own section: `github` (`account`, `remote`, `repository`, `wait`), later `gitlab`, `bitbucket` |
| `autonomy` | `merge-when-green`, `prepare` | none: adopt asks (section 8) | `merge-when-green`: the agent merges when everything is green, apart from changes a person must merge. `prepare`: the agent does everything but the merge, and a person merges |
| `method` | `squash`, `merge`, `rebase` | `squash` | the merge method asked of the host; refused at doctor when the host does not allow it |
| `checks` | `rules`, or a list of names | `rules` | which host checks block: those the base's branch rules require, or the listed ones (section 6) |
| `gate` | `true`, `false` | `true` | run the project's gate locally on the exact tree the merge will make (section 4.4) |
| `review` | `true`, `false` | `true` | a clean `jst review` round of the change is required (section 4.3) |
| `base` | `merge-tree`, `up-to-date` | `merge-tree` | base freshness (section 4.5) |
| `human_paths` | glob list | adopt's recommendation | paths that only a person may land, besides `jackstay.json`, which is always one (W-047) |
| `via` | `direct`, `auto-merge`, `queue` | `direct` | how the merge is asked of the host (section 4.8) |
| `publish_statuses` | `true`, `false` | `false` | publish jackstay's evidence to the host as commit statuses (section 7) |

Flags can lower autonomy for one run, never raise it: `jst land --prepare` stops before the merge in
a `merge-when-green` project; nothing turns a `prepare` project into one that merges.

The `queue` section (never released) is not in schema 3 and is refused as an unknown key. No other
existing key changes meaning. `github.wait` keeps giving the defaults of the check wait.

A project with no `land` section has no landing policy. `jst land` then refuses and names
`jst adopt` to choose one; the `jst gh pr-land` alias keeps its 0.5.11 code path there, with one
addition: a change that touches `jackstay.json` is never merged by it (section 9).

### 1.1 Upgrading a 0.5.x project

A jackstay older than 0.6.0 refuses a schema 3 file, and under
[W-085](../decisions/W-085-adopt-lands-settings-and-files-apart.md) the settings land before the
workflows, so CI runs the old pin on the first settings change. 0.6.0 therefore reads schema 2
(without a `land` section) as well, and `jst adopt` plans the upgrade in three pull requests, in
this order:

1. Settings, schema 2: `adopt.pin` moved to 0.6.0. CI still runs the old pin, which reads it.
2. Files: the workflows download 0.6.0.
3. Settings, schema 3: `jackstay_schema: 3`, the `land` section, `$schema`. CI now runs 0.6.0.

Then the AGENTS block is rewritten with the `land` items. Adopt does not plan step 3 while the pin
the workflows download is older than 0.6.0. Each step is a settings or files change, so a person
merges the settings ones (section 4.2).

## 2. Words

- **Change**: a host's unit of review and merge: a GitHub pull request, a GitLab merge request, a
  Bitbucket pull request. jackstay's code and text say "change"; output names the host's own word.
- **Head**: the change's source commit. **Base**: the branch it merges into, and that branch's tip.
- **Candidate**: the tree the merge will make. With `base: up-to-date` it is the head's tree (the
  head already contains the base tip); otherwise it is the tree Git's merge of the base tip and the
  head makes (`git merge-tree --write-tree`), and jackstay writes an unreachable commit of it to
  gate it, moving no ref.
- **Attempt**: one pass of the engine over one set of inputs (base tip, head, policy, rules). Any
  input that moves ends the attempt.
- **A person**: someone at a terminal, confirming at an interactive prompt (section 4.7). An agent
  has no terminal on its standard input; a host approval cannot tell an agent from its owner when
  both act as the same account.

## 3. The hosting adapter

The engine (`internal/land`) knows no host. An adapter (`internal/host/github` in 0.6.0) maps the
engine's calls to one host's API. `internal/githubapi` stays as the GitHub adapter's client;
`internal/gh` and `internal/mergequeue` fold into the engine and the adapter.

### 3.1 The interface

The core every adapter implements, and small optional interfaces for the rest. A capability is the
optional interface being implemented, confirmed by a probe at run time where the host's plan or the
account's rights decide it (GitHub's rulesets are refused on a private repository on the Free plan).

```go
// Host is what every adapter implements.
type Host interface {
    Name() string                                     // "github"
    Probe(ctx context.Context) (Capabilities, error)  // what this account can do on this repository now
    Change(ctx context.Context, ref ChangeRef) (Change, error)
    FindChanges(ctx context.Context, query ChangeQuery) ([]Change, error)
    OpenChange(ctx context.Context, request NewChange) (Change, error)
    Checks(ctx context.Context, change Change) ([]Check, error) // on the head and on the host's test merge
    Rules(ctx context.Context, branch string) (Rules, error)
    BranchTip(ctx context.Context, branch string) (string, error)    // the live ref, never the change's copy
    Commit(ctx context.Context, sha string) (Commit, error)           // tree and ordered parents
    Merge(ctx context.Context, change Change, request MergeRequest) (MergeResult, error)
    Request(ctx context.Context, change Change) (RequestState, error) // auto-merge or queue entry, if any
}

// Optional capabilities.
type ExpectedHeadMerger interface{ /* Merge honours MergeRequest.ExpectedHead atomically */ }
type BranchUpdater interface {
    UpdateBranch(ctx context.Context, change Change, expectedHead string) error // merges the base in; never rebases
}
type Approvals interface{ Approvals(ctx context.Context, change Change) ([]Approval, error) }
type AutoMerger interface {
    EnableAutoMerge(ctx context.Context, change Change, method Method, expectedHead string) error
}
type Queuer interface{ Enqueue(ctx context.Context, change Change, expectedHead string) error }
type StatusPublisher interface{ PublishStatus(ctx context.Context, commit string, status Status) error }
type ProvenanceVerifier interface {
    VerifyProvenance(ctx context.Context, digest string, expected Signer) (Provenance, error)
}
type Releaser interface{ /* the calls internal/release makes today: releases, and the release
    pull request's reads, description edits, draft conversion and closing */ }
type RunWaiter interface{ /* CI runs by workflow and ref, their jobs and logs */ }
```

`Change` carries the number, the URL, the state (open, merged, closed, draft), the head and base
branches and commits, the head repository, the author, the host's test merge commit when there is
one, and whether the host says it merges cleanly. The base commit a change carries can be stale
(the merge-queue pilot saw GitHub keep the old one after the base moved): the engine reads the base
through `BranchTip`, and uses the host's test merge only when `Commit` shows its parents are the
live base tip and the head, in that order, and its tree is the candidate's. `RequestState` says
whether an auto-merge request or a queue entry exists for the change, and at which head. `Rules` carries the required checks (name and,
where the host has one, the app that must report it), whether the base must be up to date, how many
approvals the host requires, whether a merge queue is required, and the merge methods allowed, each
with whether it could be read. `Check` is in section 6.1.

### 3.2 Capabilities and degradation

`Capabilities` is a map from a capability to `supported`, `unsupported` or `unknown` (the probe
could not tell, for instance a 403 on the rules). The engine reads it once per attempt and degrades
by a fixed table; it never guesses. `jst doctor` prints the same map (section 8.3).

| capability | when it is missing |
|---|---|
| rules readable | `checks: "rules"` counts every check, as `pr-land` does today. The plan says so in its own line, the record keeps `rules.read: false`, and doctor recommends listing the checks: an unknown policy is never taken as an empty one |
| expected-head merge | the engine reads the change again just before the merge and compares heads; the record marks the merge `guard: read-compare`, a narrower race than none. Allowed in both modes, and doctor names it |
| branch update | with `base: up-to-date`, the engine merges the base into the head locally in a clean copy and pushes it (never a rebase, never a force-push) |
| auto-merge | `via: auto-merge` is refused at doctor and at `jst land`; nothing falls back silently |
| merge queue | `via: queue` is refused the same way. When the rules require a queue that the adapter cannot use, `jst land` stops ready for a person |
| status publishing | `publish_statuses: true` is refused at doctor |
| provenance | `jst receipts verify --provenance` is refused |
| releases, runs | the release commands and `jst checks run-wait` are refused with the host's name |

### 3.3 GitHub (0.6.0)

| engine call | GitHub |
|---|---|
| change, find, open | REST pulls; GraphQL search by head commit (as `pr-find` today) |
| checks | GraphQL status check rollup on the head and on the test merge commit (as today); Actions workflow runs for supersession |
| rules | rulesets that apply to the branch, then classic branch protection (as `RequiredChecksFor` today); merge methods from the repository |
| merge | `PUT /pulls/{number}/merge` with `sha` (expected head) and `merge_method` |
| branch update | `PUT /pulls/{number}/update-branch` with `expected_head_sha` |
| approvals | pull request reviews |
| auto-merge | GraphQL `enablePullRequestAutoMerge` with `expectedHeadOid` |
| merge queue | GraphQL `enqueuePullRequest` with `expectedHeadOid` |
| statuses | `POST /statuses/{sha}` |
| provenance | artifact attestations for a subject digest, verified against the expected repository and signer workflow (section 3.5) |
| live base, commits | `GET /git/ref/heads/{branch}`, `GET /git/commits/{sha}` (as the queue's provider today) |
| request state | GraphQL `autoMergeRequest` and `mergeQueueEntry` of the pull request |
| releases, runs | as `internal/release` and `jst gh run-wait` today; the release pull request's management stays in `internal/release`, moved behind `Releaser` |

### 3.4 GitLab and Bitbucket: the fit

Not built in 0.6.0. The table names which API concept each call maps to, to show the interface
holds; the adapters are specified when they are built.

| engine call | GitLab | Bitbucket Cloud |
|---|---|---|
| change | merge request | pull request |
| checks | the merge request's pipelines and the commit's statuses; a pipeline the host cancelled as redundant maps to `superseded` | the commit's build statuses; `STOPPED` maps to `cancelled` |
| rules | protected branches, "pipelines must succeed", approval rules, merge methods (project settings) | branch restrictions: `require_passing_builds_to_merge`, `require_approvals_to_merge`; merge strategies |
| merge, expected head | accept merge request with `sha` (expected head) and `squash` | merge pull request with `merge_strategy`; no expected head: `guard: read-compare` |
| branch update | none that merges (the host rebases): the engine's local merge and push | none: the engine's local merge and push |
| approvals | merge request approvals | participants who approved |
| auto-merge | auto-merge ("merge when pipeline succeeds") with `sha` | `unknown` until probed; refused when absent |
| merge queue | merge trains | none: `unsupported` |
| statuses | commit status API | build status API (counts towards "minimum successful builds") |
| provenance | build provenance from GitLab CI, `unknown` until a pilot proves a verification path | none: `unsupported` |
| releases | releases API | none: `unsupported` |

### 3.5 Provenance (replaces `attest-ci`/`verify-ci`)

`jst receipts attest-ci` and `jst receipts verify-ci` (never released) are removed with
`internal/receipts/ci_evidence.go` and their help and AGENTS-block item. In their place:

- The gate workflow `jst adopt` writes exports one receipt, the counted receipt of the `gate`
  scope (`jst receipts export gate`), and attests it with GitHub's
  `actions/attest-build-provenance`, the receipt's bytes being the subject. The signer workflow and
  repository are template values filled from the project, never jackstay's own repository.
- A new key names the trusted signer: `receipts.provenance` =
  `{"workflow": ".github/workflows/gate.yml", "refs": ["refs/heads/main"]}`, written by adopt with
  the workflow it writes. The repository is the adapter's configured one. Neither ever comes from
  the attestation.
- `jst receipts verify FILE --digest D --revision R --key K --provenance` makes every check
  `jst receipts verify` makes today (the expected digest, revision and key, from a trusted source),
  and then asks the adapter whether that digest was attested by the trusted workflow, in that
  repository, on one of the trusted refs. Provenance adds to the receipt's checks; it never stands
  in for them.
- The landing record keeps the receipt's digest; provenance is not a `land` key in 0.6.0 (section 10).

## 4. The engine: `jst land`

```
jst land [--change N|URL] [--task NAME] [--title T] [--body-file F] [--card ID]... [--reason TEXT]
         [--prepare | --confirm] [--plan] [--timeout S] [--interval S] [--lines N]
jst land status [--change N|URL] [--json]
jst land reconcile [--change N|URL]
jst land find (--head BRANCH | --search TEXT) [--state STATE] [--wait S]
```

`--task` names the review task (default: the change's head branch, as `jst review` defaults to the
branch). `--lines` is the log lines shown per failed check, as `ci-wait`'s.

`jst land` prints its plan first (as `pr-land` does) and `--plan` stops there. Then it runs the
steps in order; each step that fails ends the run with its reason and the record saved. A second run
picks up the open attempt if its inputs have not moved, else starts a new one. One lock per change
(`internal/flock`, with a timeout) keeps two runs from interleaving.

### 4.1 The change

The change is the open one from this branch, or `--change`. `jst land` pushes the branch when it
has commits the remote lacks (verifying every push destination, as today), and opens a change when
none is open, with `--title` and the card line rules of today. The work tree must be clean, the
branch must not be the base, and this checkout's branch must not have commits the head lacks (as
`nothingLeftBehind` today). A draft is never merged: it stops as refused, naming the draft, so the
release flow's owner-only draft pull request is left to that flow.

### 4.2 Policy

Read before any evidence, offline where it can be:

1. The `land` section is valid, and the base's `jackstay.json` is the one judged (W-047).
2. The paths the change touches, from the merge base to the head, against `jackstay.json` and
   `human_paths`. A change to `jackstay.json` that touches anything else is refused (W-047), as
   `jst lint` refuses it.
3. The host's rules: required checks, required approvals, a required queue, allowed methods. A
   method the host does not allow, or a required queue the adapter cannot use, is said here.
4. The card line, as today.

The result is one of: the agent may merge; a person must merge (`autonomy: prepare`, a human path
touched, or host approvals still missing); refused.

### 4.3 Evidence

- **Review** (`review: true`): `jst review check MERGE-BASE..HEAD --task TASK` is clean, under the
  base's review settings. A head may differ from the head of that clean round only by merges of the
  base, and the round still counts when every such merge commit:
  - has two parents, the first being the reviewed head or an earlier carried merge, the second a
    commit reachable from the live base tip;
  - has the tree that `git merge-tree --write-tree` of those two parents gives, with no conflict.
  A merge with resolved conflicts or any other edit fails the second test and needs a new round.
  The record keeps the task, the round, the reviewed range and the carried merges.
- **Host checks**: waited for as `jst gh ci-wait` waits today, judged as section 6 says. Checks whose
  names are jackstay's own statuses (section 7) are not waited for here.
- **Host approvals**: read only when the host's rules require them, to say what is missing. jackstay
  never requires an approval by someone other than the author: a solo maintainer can land.

### 4.4 The local gate on the merge tree

With `gate: true`, the candidate (section 2) is gated on a clean copy of its tree, and its counted
receipt is kept by key and digest. The gate's settings (the `test` section and its scopes) are read
from the base tip's `jackstay.json`, never the candidate's, so a change cannot choose the gate that
judges it (W-047: the base's file judges). The form is `jst test gate --commit CANDIDATE
--settings-from BASE`, a new flag of the gate, and the receipt records both commits. The gate runs after the host checks pass, so a
red CI does not cost a local gate. A candidate already gated in this attempt is not gated again.

### 4.5 Base freshness

- `merge-tree` (default): the candidate is the merge of the base tip at the final read and the head.
  A base that moves after that read is not prevented: the host merges onto the new tip, and
  reconcile (section 4.9) compares the landed tree with the candidate and records a mismatch.
- `up-to-date`: the head must contain the base tip. A head behind is updated (the adapter's branch
  update, else the local merge and push), and the attempt starts again with the new head: checks,
  the review carried over by section 4.3, the gate. The candidate is then the head's own tree, so a
  status on the head is a status on the candidate. With the host's own "up to date" rule, the
  jackstay statuses required (section 7) and no bypass for the acting account, the host should
  refuse a merge whose candidate is not the one gated: the merge-queue pilot's atomic condition.
  That is a claim for the GitHub pilot (section 11) to prove for each merge method before 0.6.0
  relies on it; until it is proven, it is detected after the merge like `merge-tree`.

### 4.6 The final recheck

After the gate, and again immediately before any merge request (and before the prompt of section
4.7), the engine reads the change, the live base tip (`BranchTip`), the rules and, with a host test
merge, its commit, and compares them with the attempt's inputs: head, base branch and tip, state,
draft, rules, test merge parents and tree. Any difference ends the attempt as `moved`, and the next
run starts a new one. Gate and review evidence are never carried to inputs they were not made on,
apart from the review's carried merges of section 4.3.

### 4.7 The decision

- **The agent may merge**: step 4.8.
- **A person must merge** (`autonomy: prepare`, `jackstay.json` or a human path touched, or
  `--prepare`): `jst land` publishes the statuses if switched on, saves the record as `ready`,
  prints what is ready and why a person is needed, and ends with `jst land: ready for a person`.
  The person merges on the host, and the next `jst land` or `jst land reconcile` records it. Or
  the person runs `jst land --confirm` at a terminal: it asks them to type the change's number,
  runs the final recheck (section 4.6) and step 4.8. `--confirm` is refused when standard input
  is not a terminal, and together with `--prepare`; `--prepare` always stops before the merge.
- **The host's approvals are missing** (its rules require some): the same `ready` stop, naming the
  approvals. `--confirm` is refused until the host shows them; typing a number gives no approval.
- **Refused**: the reason, and the record saved as `refused`.

### 4.8 The merge

The request is saved in the record as `intended` (method, expected head, `via`) before it is sent,
then as `sent` with the host's answer. By `via`:

- `direct`: merge with the expected head. When the host's rules require a queue, `direct` is
  refused by the host; `jst land` says so and names `via: queue`.
- `auto-merge`: enable the host's auto-merge with the expected head. Allowed only with
  `base: up-to-date`, the host's own "up to date" rule, `publish_statuses: true`, the jackstay
  statuses required by the base's rules, and no bypass for the acting account where the host shows
  bypasses (doctor checks all of them, `jst land` again). Otherwise a base that moves after the
  request could land a tree no one gated.
- `queue`: enqueue with the expected head. The host's queue builds and tests its own candidate; the
  record says the landed tree was the host queue's.

**Recovery.** A run that finds a request `intended` or `sent` and not yet reconciled reads before
it writes anything:

- the change is merged: reconcile (section 4.9);
- `RequestState` shows an auto-merge request or a queue entry: the request is outstanding. The
  run reports it and sends nothing, whatever moved since; a new attempt keeps the outstanding
  request in its record (`outstanding`) until the host settles it. jackstay never cancels it on
  its own;
- `direct`, the change open and unmerged, and no request state: a direct merge is synchronous, so
  it did not take effect. The request is saved as `not-taken`, and the run continues with a new
  attempt;
- the host cannot say (an error, or a request state the adapter cannot read): `unsettled`. Nothing
  is sent until a person looks; `jst land status` says what was asked and when.

### 4.9 Reconcile

Once the change is merged (now, or later by a person, auto-merge or the queue): the landed commit is
fetched, its tree compared with the candidate's, and the outcome saved: `landed-matched`,
`landed-mismatch` (said loudly; nothing is reverted, the base's own CI covers the landed commit) or
`landed-unchecked` (no candidate was gated).

The remote branch is then deleted only when every check `pr-land` makes today holds: the host
confirmed this attempt's own merge request and its merge commit is the one the change shows; the
change's head repository is the configured one and its head branch is still at the merged head (a
lease); no other open change uses the branch; every push destination is verified. It is kept, and
the reason printed, after `landed-mismatch`, after a merge this run did not request (a person,
auto-merge, the queue) and whenever one of those checks cannot be confirmed. `jst tidy` removes
the local branch later, as today.

### 4.10 Exit status and the last line

Every run of `jst land` ends with the line `jst land: STATE`, so an agent reads one line, as with
`jst test`.

| state | exit | when |
|---|---|---|
| `landed` | 0 | merged; the landed tree is the candidate's, or no gate was asked (`landed-unchecked` in the record) |
| `ready for a person` | 0 | section 4.7 |
| `planned` | 0 | `--plan` |
| `landed, tree differs` | 1 | `landed-mismatch` |
| `refused` | 1 | the policy refuses; a required check failed or was cancelled; the review is not clean; the gate failed; a draft |
| `unsettled` | 1 | the host cannot say what became of a request (section 4.8) |
| `invalid` | 2 | usage; `jackstay.json` or the `land` section unusable; no account |
| `pending` | 3 | checks, auto-merge or the queue still pending at the deadline; an outstanding request |
| `moved` | 4 | the head, base or rules moved (section 4.6) |
| `unexpected` | 5 | a host answered in a form jackstay does not expect |

`jst land status` exits 0 when it printed the latest attempt, 1 when there is none, 2 on usage.
`jst land reconcile` exits 0 for `landed-matched` or `landed-unchecked`, 1 for `landed-mismatch`,
3 when the change is not merged yet, 2 on usage. `jst land find` keeps `pr-find`'s statuses.

## 5. The landing record

### 5.1 What it replaces

1. `pr-land`'s in-memory `checkEvidence` (`internal/gh/checks.go`): head, base and test merge
   commit, lost when the command ends.
2. `pr-land`'s trace attributes (`notePull`): the only lasting trace of a landing today.
3. The queue's `State` and `Outcome` (`internal/mergequeue/queue.go`).
4. The queue's receipt references and gate claim (`Evidence`, `GateClaim`, `GateRunID`).
5. The CI attestation claim (`ciClaim` in `internal/receipts/ci_evidence.go`).

### 5.2 The shape

One JSON file per change and attempt, `attempt-N.json` under
`<jackstay home>/<project>/land/<host>/<change>/`, with its own schema. It is not a
`internal/delivery` record: that envelope's plan is fixed when written, while this record grows
phase by phase. It shares the shared atomic write and strict reading, and a per-change lock
(`internal/flock`, with a timeout). It holds object IDs, names and digests; never a token, a URL
with credentials or a command's output.

```json
{
  "schema": 1,
  "host": "github",
  "repository": "owner/name",
  "change": 42,
  "attempt": 3,
  "policy": {"sha256": "…", "autonomy": "merge-when-green", "method": "squash", "via": "direct",
             "base": "merge-tree", "checks": "rules", "gate": true, "review": true},
  "base": {"branch": "main", "commit": "…"},
  "head": {"branch": "fix-parser", "commit": "…"},
  "rules": {"read": true, "required": ["test", "lint"], "up_to_date": false, "approvals": 0},
  "candidate": {"commit": "…", "tree": "…", "made": "merge-tree", "test_merge": "…"},
  "review": {"task": "fix-parser", "round": 2, "range": "…..…", "carried_merges": [],
             "verdict": "clean"},
  "checks": [{"name": "test", "required": true, "state": "pass", "commit": "…"},
             {"name": "docs", "required": false, "state": "fail", "commit": "…"}],
  "gate": {"settings_commit": "…", "receipt_key": "gate", "receipt_sha256": "…"},
  "person": {"needed": true, "why": ["human path: .github/workflows/ci.yml"], "confirmed": "host"},
  "statuses": [{"context": "jackstay/gate", "commit": "…", "state": "success"}],
  "request": {"via": "direct", "expected_head": "…", "guard": "expected-head",
              "state": "sent", "intended": "…", "sent": "…", "host_id": ""},
  "outstanding": null,
  "phase": "reconciled",
  "reason": "",
  "outcome": {"state": "landed-matched", "commit": "…", "tree": "…", "observed": "…"}
}
```

`phase` runs `prepared` → `evidenced` → `gated` → `ready` or `requested` → `merged` →
`reconciled`, or ends at `refused` (with `reason`: the policy, a check, the review, the gate, a
draft), `moved` or `unsettled`. `request.state` is `intended`, `sent`, `not-taken` or `settled`
(section 4.8); `outstanding` carries an earlier attempt's auto-merge request or queue entry until
the host settles it. The phases and outcomes map to the last line by section 4.10. `jst land status`
prints the latest attempt; `--json` prints the record as it is. A change's records are kept for
`telemetry.keep_days`.

## 6. Required checks

### 6.1 States

The adapter maps the host's states to: `pass`, `fail`, `pending`, `skipped`, `cancelled`,
`superseded`, `missing` (required, not registered). Each check also says whether it is required,
which commit it was read on (head or the host's test merge), and its run where the host has runs.

### 6.2 Judging

- **Required** (by the rules, or listed in `checks`): each must be `pass` or `skipped` on the commit
  the host judges: the test merge where the host has one and section 3.1's validation holds it to
  the live base and the head, else the head (as `withMergeCommitRows` falls back today). `pending`
  and `missing` are waited for; `fail` stops; `cancelled` stops, named as a cancellation, not a
  failure.
- **Not required**: never waited for and never blocking. A `fail` or `cancelled` one is printed and
  kept in the record. This changes 0.5.11, where a failing check no rule requires stops `pr-land`.
- **Superseded** ([#13](https://github.com/zima-oss/jackstay/issues/13)): the host does not say a
  run was cancelled by a concurrency group, so supersession is inferred, by the rule
  `reconcileCancelled` holds today, moved behind the adapter: a newer run of the same workflow,
  commit, event, head branch and change. The cancelled check is then `superseded` and judged by the
  newer run's own job of the same name and app only. While that job has no result it is `pending`;
  when the newer run has finished without that job, the check stays `cancelled` (named: superseded
  by a run that did not run it). A run's overall conclusion never decides a check: today's rule
  that a red replacement run marks the check failed goes. A cancelled run with no newer run stays
  `cancelled`.
- **Rules unread**: every check counts, said in the plan and the record (section 3.2).
- **Rules read, requiring none**: no host check blocks; failures are reported. The project's own
  gate and review still apply, and doctor notes that the base requires no check.

## 7. Publishing statuses (optional, off by default)

With `publish_statuses: true`, `jst land` publishes on the head commit, for the steps the policy
turns on:

| context | published when | success when | failure when |
|---|---|---|---|
| `jackstay/review` | `review: true` | the review evidence holds (section 4.3) | a round has findings, or none is current |
| `jackstay/gate` | `gate: true` | the candidate's gate passed | the gate failed |

The order avoids waiting on itself: the host checks are waited for first, leaving out jackstay's
own contexts (section 4.3); then the review and the gate run locally; then the statuses are
published; then, when the base's rules require them, the engine reads them back on the head
before any merge request. A person's handoff publishes the same two statuses, so a person's merge
on the host is not blocked by them; whether a person must merge is never a status. A policy with
`review` or `gate` off publishes nothing for it, and doctor reports an error when the rules require
a context the policy never publishes.

`jackstay/gate` on the head means the candidate only with `base: up-to-date` (the head's tree is the
candidate). With `merge-tree` it says the merge tree at the final read passed, and a base that
moves after it is caught by reconcile, not by the status. Descriptions are short and carry no
paths or output; no target URL is set, since the evidence is local. Published statuses are a signal
any account with write access could also set: jackstay's own record stays the evidence, and doctor
says this when the base's rules require them. A failure to publish is printed and does not stop a
`direct` merge whose rules do not require the statuses; otherwise it stops, because the host would
wait on a status that never comes.

## 8. Adopt, setup and doctor

### 8.1 `jst adopt`

The plan writes `jackstay_schema: 3` and a full `land` section. Adopt's prompt asks two things, each
with its recommended answer first, and writes the answers to the file:

1. **Who merges?** "The agent merges when everything is green; `jackstay.json` and the listed paths
   need a person (recommended)" or "The agent prepares; a person merges."
2. **Publish jackstay's evidence to the host as commit statuses?** "No (recommended)" or "Yes".

Every other key is written with its default, the human paths with
`[".github/workflows/**", ".claude/settings.json"]`, and the plan prints the section so the choice
is seen. Without a terminal, `--apply` needs `--autonomy merge-when-green|prepare` (and optionally
`--publish-statuses`), or an existing `land.autonomy`; it never picks one silently. An existing
`land` section is kept as it is; a changed answer is a settings change (W-085). Whoever runs adopt,
the choice reaches the base only through a change to `jackstay.json`, which a person merges
(section 4.2; the alias too, section 9): an agent can propose a mode, never put one in force.
Adopt also writes `receipts.provenance` with the gate workflow it writes (section 3.5), and plans
the upgrade order of section 1.1.

### 8.2 `jst setup`

Setup is per machine. It asks the same two questions once and keeps the answers in
`<jackstay home>/setup.json` as the defaults adopt offers first; the project's `jackstay.json` stays
the only thing that decides. Setup prints, for each project it finds adopted on the machine, the
autonomy that project chose.

### 8.3 `jst doctor`

| check | finding |
|---|---|
| `land` valid, schema 3 | error: what is wrong |
| `land.autonomy` set | error: run `jst adopt` |
| the adapter connects as the configured account | error, as for `github.account` today |
| capabilities probed | the map of section 3.2, `unknown` named with why |
| rules readable for the default branch | warning when unread: list `checks` |
| the rules require at least one check (with `checks: "rules"`) | note: no host check blocks |
| `method` allowed by the host | error |
| `via: auto-merge`: `base: up-to-date`, the host's "up to date" rule, statuses published and required, no bypass for the account | error otherwise |
| the rules require a `jackstay/*` context the policy never publishes | error: the host would wait forever |
| `via: queue`: the base requires a queue | error otherwise |
| `publish_statuses`: the account can write statuses | error otherwise |
| `human_paths` each match a tracked file | warning: a glob that matches nothing |
| `merge-when-green` without an expected-head merge | warning: `guard: read-compare` |
| `base: up-to-date` without the host's own rule | note: the race is detected, not prevented |

## 9. What becomes of `jst gh`, `jst queue` and the CI receipts

`jst gh` shipped in 0.5.10 and 0.5.11. `jst queue` and `receipts attest-ci`/`verify-ci` never
shipped.

| today | 0.6.0 |
|---|---|
| `jst gh pr-land` | `jst land`; an alias for 0.6.x that prints one line to stderr naming `jst land`. Which path it takes is below the table |
| `jst gh pr-status` | `jst land status`; alias for 0.6.x |
| `jst gh pr-find` | `jst land find`; alias for 0.6.x |
| `jst gh ci-wait`, `ci-status` | `jst checks wait`, `jst checks status`; aliases for 0.6.x |
| `jst gh run-wait` | `jst checks run-wait`; alias for 0.6.x |
| `jst queue add`, `prepare`, `test` | removed: `jst land` prepares and gates the candidate |
| `jst queue status`, `handoff` | removed: `jst land status`, and `jst land` in `prepare` |
| `jst queue reconcile` | removed: `jst land reconcile` |
| `jst queue remove` | removed: a new attempt replaces the old; a closed change ends it |
| `queue` in `jackstay.json` | refused (never released) |
| `receipts attest-ci`, `verify-ci` | removed: `jst receipts verify --provenance` (section 3.5) |

**The `pr-land` alias.**
- In a project **with** a `land` section it runs `jst land`. `--pr` is `--change`, `--dry-run` is
  `--plan`, `--no-merge` is `--prepare`; `--title`, `--body-file`, `--card`, `--reason`,
  `--timeout`, `--interval` and `--lines` pass through. `--no-push`, `--no-create` and `--no-wait`
  are refused with the reason: the engine's evidence needs those steps.
- In a project **without** one it runs the 0.5.11 code path unchanged (`internal/gh`'s `prLand`
  kept for 0.6.x): every flag, the push of an open pull request's new commits, `updateBehind`, and
  0.5.11's check rule, where a failing check no rule requires still stops. One addition: a pull
  request that touches `jackstay.json` is never merged by it; it stops before the merge, says a
  person merges it (W-047), and exits 0. That keeps an agent from landing the change that would
  choose its own autonomy.

The other aliases map flag for flag: `ci-wait`'s `--timeout`, `--interval`, `--lines` and
`--any-head` are `jst checks wait`'s; `run-wait`'s and `pr-find`'s flags are unchanged. The aliases
are removed in 0.7.0. The upgrade page lists them, the order of section 1.1 and the change of
section 6.2 for failing checks that no rule requires.

`jst land --help`:

```
usage: jst land [--change N|URL] [--task NAME] [--title T] [--body-file F] [--card ID]...
                [--reason TEXT] [--prepare | --confirm] [--plan]
                [--timeout S] [--interval S] [--lines N]
       jst land status [--change N|URL] [--json]
       jst land reconcile [--change N|URL]
       jst land find (--head BRANCH | --search TEXT) [--state STATE] [--wait S]

Land a change by the project's land policy: push and open it, wait for the checks its base
requires, check the review, gate the exact tree the merge makes, then merge with the head that
was checked, or stop ready for a person (land.autonomy, jackstay.json, land.human_paths). The
plan is printed first. This checkout is never switched, pulled or rebased.

  --change N|URL  the change (default: the open one from this branch)
  --task NAME     the review task (default: the head branch)
  --prepare       stop before the merge this once (never the other way)
  --confirm       a person at a terminal confirms the merge the policy keeps for a person
  --plan          print the plan and stop

exit: 0 landed, ready for a person, or planned; 1 refused, failed, unsettled, or landed with a
different tree; 2 bad request; 3 pending; 4 moved; 5 unexpected host answer.
The last line is `jst land: STATE`.
```

The AGENTS block (`internal/adopt/block`): the `github`, `queue` and `ci-evidence` items go. Three
come:

- land, wanted with a `land` section; its text depends on `land.autonomy`:
  - `merge-when-green`: "**Land a change**: `jst land` (push, open, checks, review, gate on the
    merge tree, merge). It ends at `jst land: ready for a person` for `jackstay.json` and the paths
    in `land.human_paths`: say so and stop. `jst land status` says what is missing. Never merge on
    the host by hand, and never run `--confirm`: it is a person's."
  - `prepare`: "**Land a change**: `jst land` prepares (push, open, checks, review, gate) and ends
    at `jst land: ready for a person`; a person merges. `jst land status` says what is missing.
    Never merge on the host by hand, and never run `--confirm`: it is a person's."
- checks, wanted with a `land` section or `github.account`: "**A change's checks**:
  `jst checks wait` / `jst checks status`, and `jst checks run-wait WORKFLOW` for a CI run; only the
  checks the base requires block."
- the receipts item gains, wanted with `receipts.provenance`: "Add `--provenance` to check that the
  project's CI attested the receipt."

The guard gains a rule, on by default where a `land` section exists, that refuses the host CLIs'
merge commands (`gh pr merge`, `glab mr merge`) and names `jst land`.

## 10. Deferred

- The GitLab and Bitbucket adapters (section 3.4 is their fit, not their design).
- Batching and bisection of several changes (the merge-queue pilot's later slices).
- A local-only lane with no host (the queue's local mode): `jst land` needs a host in 0.6.0.
- Gating the host queue's own candidate locally (`via: queue` relies on the host's tests).
- Provenance as a landing condition (`land` key); 0.6.0 records the receipt's digest only.
- An identity for published statuses other than the configured account (a GitHub App).
- Signed or recorded human decisions beyond the terminal prompt.

## 11. Tests

- **Fake adapter** (`internal/host/fake`): a scripted host whose every answer, capability and
  failure a test sets. The engine's tests run on it only.
  - Each capability missing, with the degradation of section 3.2.
  - Checks: required pass, fail, cancelled, superseded with the newer job pending, passed and
    absent from a finished run, missing, skipped; not required failing (reported, not blocking);
    rules unread; rules requiring none; a host test merge whose parents or tree do not match (the
    head is judged).
  - Policy: `jackstay.json` alone, `jackstay.json` with another file, a human path, `prepare`,
    `--prepare` in `merge-when-green`; `--confirm` refused without a terminal, with `--prepare`
    and with host approvals missing; a draft.
  - The gate reads its settings from the base while the candidate changes the `test` section.
  - Review carried across a clean base merge; not across a merge with conflicts resolved, nor one
    whose tree carries an extra edit.
  - Recovery from each request state: `intended` with nothing sent, `sent` and merged, `sent` and
    open, an outstanding auto-merge request across a moved head, and `unsettled`.
  - Branch deletion kept after `landed-mismatch`, after a merge by a person, with a sibling open
    change, and with an unverified push destination.
  - The `pr-land` alias: with a `land` section (flag mapping) and without one (0.5.11 behaviour,
    and a change to `jackstay.json` not merged).
- **Races** (fake adapter, deterministic, no timing): the head moves between the gate and the merge
  (the host refuses the expected head; `moved`); the base moves after the final read (`merge-tree`:
  `landed-mismatch`; `up-to-date` with the host rule: refused); a rule changes during the attempt;
  the merge answer is lost and the change is merged (reconciled, one request); the merge is
  accepted and held (pending); two `jst land` on one change (the lock); two changes into one base
  (the second's base moves, a new attempt); a crash after the intent is saved (resume reads before
  any request); a status publish that fails with `direct` and with `auto-merge`; the base moves
  during a long gate (the final recheck ends the attempt as `moved`).
- **Record**: strict reading, each phase transition, refusal of an unknown schema, no secret in any
  field (a made-up token in the inputs is absent from the file).
- **GitHub adapter**: recorded API answers for every call of section 3.3, including a 403 on
  rulesets and a test merge GitHub has not made yet.
- **GitHub pilot** (`-tags integration`, run by hand, not in CI): a disposable repository with
  synthetic content. Cases: a private repository on the Free plan (rules unread); a public one with
  required checks; `base: up-to-date` with the "up to date" rule and the jackstay statuses
  required, a base advance placed between the final read and the merge (the host must refuse); each
  merge method's landed tree against the candidate; auto-merge; a native merge queue; the
  concurrency-group cancellation of #13. The pilot's notes keep account and repository names
  outside Git.
- **Adopt and the block**: golden files for both autonomy modes, and for no `land` section; the
  upgrade order of section 1.1 from a 0.5.11 project, with CI's pin read at each step.

## 12. Open questions for the owner

1. **Default base freshness**: `merge-tree` (cheap; a base race is detected after the merge, not
   prevented) or `up-to-date` (every landing updates the branch and reruns CI; prevented when the
   host enforces it)?
2. **Adopt's recommended human paths**: `.github/workflows/**` and `.claude/settings.json`, or only
   `jackstay.json` (the owner's words were "settings and the listed paths")?
3. **"A person"** is someone typing the change's number at a terminal prompt. Enough, or should a
   person's merge on the host also be required to come from a different account?
4. **Setup's role**: setup keeps machine defaults that adopt offers first (section 8.2). Or should
   setup not ask at all, and only print each project's choice?
5. **The `jst gh` aliases** through 0.6.x, removed in 0.7.0, or removed in 0.6.0 outright?
6. **A review carried across a clean base merge** (section 4.3): acceptable, or must every new head
   be reviewed?
7. **Provenance verification**: a Sigstore library in jackstay (a new dependency, Apache-2.0) or
   calling `gh attestation verify` (needs `gh` installed, as `gh auth token` already does)?
8. **The guard rule** refusing `gh pr merge` and `glab mr merge` by default where `land` exists.
9. **The `pr-land` alias without a `land` section** now stops before merging a change to
   `jackstay.json` (section 9). A small change of 0.5.11 behaviour for safety: agreed?
10. **`via: auto-merge`** rests on the host refusing a stale candidate, which the GitHub pilot must
    prove per merge method (section 4.5). If the pilot fails, should 0.6.0 ship without
    `auto-merge`?
11. **The upgrade** takes three pull requests (section 1.1). Acceptable, or should 0.5.12 learn to
    read schema 3 first so that one settings change suffices?
