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
| `pr-land`'s `updateBehind` merges the base on GitHub and a queue lane goes stale without saying so | evidence silently out of date | every attempt is one record; any moved input starts a new attempt (section 5) |
| every call is written against GitHub's API (`internal/githubapi`, `internal/gh`, `internal/mergequeue`, `internal/release`) | GitLab and Bitbucket mean a rewrite | a hosting adapter with declared capabilities (section 3) |
| a failed check that no rule requires stops `pr-land` | an optional job blocks a merge the rules allow | only required checks block; the others are reported (section 6) |
| a run cancelled by its concurrency group ([#13](https://github.com/zima-oss/jackstay/issues/13)) | a superseded run reads as red | a neutral `superseded` state, judged by its newer run (section 6.2) |
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
| `via` | `direct`, `auto-merge`, `queue` | `direct` | how the merge is asked of the host (section 4.7) |
| `publish_statuses` | `true`, `false` | `false` | publish jackstay's evidence to the host as commit statuses (section 7) |

Flags can lower autonomy for one run, never raise it: `jst land --prepare` stops before the merge in
a `merge-when-green` project; nothing turns a `prepare` project into one that merges.

The `queue` section (never released) is not in schema 3 and is refused as an unknown key. No other
existing key changes meaning. `github.wait` keeps giving the defaults of the check wait.

A project with no `land` section has no landing policy. `jst land` then refuses and names
`jst adopt` to choose one; the `jst gh pr-land` alias keeps its 0.5.11 behaviour there (section 9).

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
- **A person**: someone at a terminal, confirming at an interactive prompt (section 4.6). An agent
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
    Merge(ctx context.Context, change Change, request MergeRequest) (MergeResult, error)
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
type Releaser interface{ /* the calls internal/release makes today: list, create, edit releases */ }
type RunWaiter interface{ /* CI runs by workflow and ref, their jobs and logs */ }
```

`Change` carries the number, the URL, the state (open, merged, closed, draft), the head and base
branches and commits, the head repository, the author, the host's test merge commit when there is
one, and whether the host says it merges cleanly. `Rules` carries the required checks (name and,
where the host has one, the app that must report it), whether the base must be up to date, how many
approvals the host requires, whether a merge queue is required, and the merge methods allowed, each
with whether it could be read. `Check` is in section 6.1.

### 3.2 Capabilities and degradation

`Capabilities` is a map from a capability to `supported`, `unsupported` or `unknown` (the probe
could not tell, for instance a 403 on the rules). The engine reads it once per attempt and degrades
by a fixed table; it never guesses. `jst doctor` prints the same map (section 8.3).

| capability | when it is missing |
|---|---|
| rules readable | `checks: "rules"` counts every check, as `pr-land` does today, and the record says the rules were unread; doctor recommends listing the checks |
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
| releases, runs | as `internal/release` and `jst gh run-wait` today |

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

- The workflows `jst adopt` writes export the gate's counted receipt (`jst receipts export`) and
  attest it with GitHub's `actions/attest-build-provenance`, the receipt's bytes being the subject.
  The signer workflow and repository are template values, never jackstay's own repository.
- `jst receipts verify FILE --provenance` asks the adapter to verify the attestation of the file's
  SHA-256 against the expected repository and signer workflow, which come from `jackstay.json` and
  the remote, never from the attestation.
- The landing record keeps the receipt's digest; provenance is not a `land` key in 0.6.0 (section 10).

## 4. The engine: `jst land`

```
jst land [--change N|URL] [--title T] [--body-file F] [--card ID]... [--reason TEXT]
         [--prepare] [--plan] [--timeout S] [--interval S]
jst land status [--change N|URL] [--json]
jst land reconcile [--change N|URL]
jst land find --head BRANCH | --search TEXT [--state STATE] [--wait S]
```

`jst land` prints its plan first (as `pr-land` does) and `--plan` stops there. Then it runs the
steps in order; each step that fails ends the run with its reason and the record saved. A second run
picks up the open attempt if its inputs have not moved, else starts a new one. One lock per change
(`internal/flock`, with a timeout) keeps two runs from interleaving.

### 4.1 The change

The change is the open one from this branch, or `--change`. With none, `jst land` pushes the branch
(verifying every push destination, as today) and opens one, with `--title` and the card line rules
of today. The work tree must be clean, the branch must not be the base, and this checkout's branch
must not have commits the head lacks (as `nothingLeftBehind` today).

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

- **Review** (`review: true`): `jst review check` for the change's range is clean. A head that
  differs from the reviewed one only by merges of the base, each a clean merge whose first parent
  is the previous head, keeps the review: those merges are Git's, not the author's. Anything else
  needs a new round.
- **Host checks**: waited for as `jst gh ci-wait` waits today, judged as section 6 says.
- **Host approvals**: read only when the host's rules require them, to say what is missing. jackstay
  never requires an approval by someone other than the author: a solo maintainer can land.

### 4.4 The local gate on the merge tree

With `gate: true`, the candidate (section 2) is gated with `jst test gate --commit CANDIDATE` on a
clean copy, and its counted receipt is kept by key and digest. The gate runs after the host checks
pass, so a red CI does not cost a local gate. A candidate already gated in this attempt is not gated
again.

### 4.5 Base freshness

- `merge-tree` (default): the candidate is the merge of the base tip at the final read and the head.
  A base that moves after that read is not prevented: the host merges onto the new tip, and
  reconcile (section 4.8) compares the landed tree with the candidate and records a mismatch.
- `up-to-date`: the head must contain the base tip. A head behind is updated (the adapter's branch
  update, else the local merge and push), and the attempt starts again with the new head: checks,
  the review carried over by section 4.3, the gate. With the host's own "up to date" rule and the
  jackstay statuses required (section 7), the host refuses a merge whose candidate is not the one
  gated: the merge-queue pilot's atomic condition. Without them it is the same race as `merge-tree`,
  checked in the same way.

### 4.6 The decision

- The agent may merge: step 4.7.
- A person must merge: `jst land` publishes the statuses if switched on, saves the record as
  `ready`, prints what is ready and why a person is needed, and ends with
  `jst land: ready for a person`. The person either merges on the host (and the next `jst land` or
  `jst land reconcile` records it), or runs `jst land` at a terminal, which asks them to type the
  change's number before step 4.7. That prompt is refused when standard input is not a terminal.
- Refused: the reason, and the record saved as `refused`.

### 4.7 The merge

The intent (method, expected head, `via`) is saved before the request. Then, by `via`:

- `direct`: merge with the expected head. When the host's rules require a queue, `direct` is
  refused by the host; `jst land` says so and names `via: queue`.
- `auto-merge`: enable the host's auto-merge with the expected head. Doctor allows it only with
  `publish_statuses: true` and the base's rules requiring the jackstay statuses, so the host cannot
  merge without jackstay's evidence.
- `queue`: enqueue with the expected head. The host's queue builds and tests its own candidate; the
  record says the landed tree was the host queue's.

An answer lost or ambiguous is settled by reading the change and the base, never by a second
request while the first one's effect is unknown (the merge-queue pilot's rule).

### 4.8 Reconcile

Once the change is merged (now, or later by a person, auto-merge or the queue): the landed commit is
fetched, its tree compared with the candidate's, and the outcome saved: `landed-matched`,
`landed-mismatch` (said loudly; nothing is reverted, the base's own CI covers the landed commit) or
`landed-unchecked` (no candidate was gated). Then the remote branch is deleted under a lease at the
merged head, as today, and `jst tidy` removes the local branch later, as today.

### 4.9 Exit status and the last line

0: landed, or ready for a person; 1: refused, a check failed, the gate failed; 2: a bad request or
configuration; 3: pending at the deadline (checks, auto-merge, the queue); 4: the head or base moved
while it was read; 5: a host answered in a form jackstay does not expect. The last line is always
`jst land: STATE` with `landed`, `ready for a person`, `pending`, `refused` or `moved`, so an agent
reads one line, as with `jst test`.

## 5. The landing record

### 5.1 What it replaces

1. `pr-land`'s in-memory `checkEvidence` (`internal/gh/checks.go`): head, base and test merge
   commit, lost when the command ends.
2. `pr-land`'s trace attributes (`notePull`): the only lasting trace of a landing today.
3. The queue's `State` and `Outcome` (`internal/mergequeue/queue.go`).
4. The queue's receipt references and gate claim (`Evidence`, `GateClaim`, `GateRunID`).
5. The CI attestation claim (`ciClaim` in `internal/receipts/ci_evidence.go`).

### 5.2 The shape

One JSON file per change and attempt, under `<jackstay home>/<project>/land/<host>/<change>/`,
written through the delivery record (`internal/delivery`: versioned, atomically replaced, with
phases), read strictly. It holds object IDs, names and digests; never a token, a URL with
credentials or a command's output.

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
  "candidate": {"commit": "…", "tree": "…", "made": "merge-tree"},
  "review": {"task": "fix-parser", "round": 2, "reviewed_head": "…", "verdict": "clean"},
  "checks": [{"name": "test", "required": true, "state": "pass", "commit": "…"},
             {"name": "docs", "required": false, "state": "fail", "commit": "…"}],
  "gate": {"receipt_key": "…", "receipt_sha256": "…"},
  "person": {"needed": true, "why": ["human path: .github/workflows/ci.yml"], "confirmed": "host"},
  "statuses": [{"context": "jackstay/gate", "commit": "…", "state": "success"}],
  "request": {"via": "direct", "expected_head": "…", "guard": "expected-head", "sent": "…"},
  "phase": "reconciled",
  "outcome": {"state": "landed-matched", "commit": "…", "tree": "…", "observed": "…"}
}
```

`phase` runs `prepared` → `evidenced` → `gated` → `ready` or `requested` → `merged` →
`reconciled`, or ends at `refused` or `moved`. `jst land status` prints the latest attempt; `--json`
prints the record as it is. A change's records are kept for `telemetry.keep_days`.

## 6. Required checks

### 6.1 States

The adapter maps the host's states to: `pass`, `fail`, `pending`, `skipped`, `cancelled`,
`superseded`, `missing` (required, not registered). Each check also says whether it is required,
which commit it was read on (head or the host's test merge), and its run where the host has runs.

### 6.2 Judging

- **Required** (by the rules, or listed in `checks`): each must be `pass` or `skipped` on the commit
  the host judges (the test merge where the host has one, as `withMergeCommitRows` today). `pending`
  and `missing` are waited for; `fail` stops; `cancelled` stops, named as a cancellation, not a
  failure.
- **Not required**: never waited for and never blocking. A `fail` or `cancelled` one is printed and
  kept in the record. This changes 0.5.11, where a failing check no rule requires stops `pr-land`.
- **Superseded** ([#13](https://github.com/zima-oss/jackstay/issues/13)): a run cancelled because a
  newer run of the same workflow, commit, event and change replaced it is `superseded`, and the
  check is judged by the newer run's job of the same name (the rule `reconcileCancelled` holds
  today, moved behind the adapter). A newer run without that job's result is `pending`; a
  workflow's success alone never satisfies a required check. A cancelled run with no newer run stays
  `cancelled`.
- **Rules unread**: every check counts (as today), and the record says so (section 3.2).
- **Rules that require none**: every check counts, and doctor says the base requires none.

## 7. Publishing statuses (optional, off by default)

With `publish_statuses: true`, `jst land` publishes on the head commit:

| context | success when | failure when |
|---|---|---|
| `jackstay/review` | the review evidence holds | a round has findings, or none is current |
| `jackstay/gate` | the candidate's gate passed and the candidate is the head's tree (`base: up-to-date`) or the merge tree at the final read | the gate failed |
| `jackstay/land` | the policy lets the agent merge | a person must merge, or the policy refuses |

A status is pending while its step runs. Descriptions are short and carry no paths or output; no
target URL is set, since the evidence is local. Published statuses are a signal any account with
write access could also set: jackstay's own record stays the evidence, and doctor says this when
the base's rules require the statuses. A failure to publish is printed and does not stop a `direct`
merge; with `via: auto-merge` it stops, because the host would then wait on a status that never
comes.

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
`land` section is kept as it is; a changed answer is a settings change (W-085).

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
| the rules require at least one check (with `checks: "rules"`) | warning: every check counts |
| `method` allowed by the host | error |
| `via: auto-merge`: statuses published and required by the rules | error otherwise |
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
| `jst gh pr-land` | `jst land`. The alias stays for 0.6.x: it prints one line to stderr naming `jst land` and runs it. `--dry-run` is `--plan`; `--no-merge` is `--prepare`; `--no-push`, `--no-create`, `--no-wait` are refused with the reason (the engine needs those steps). In a project with no `land` section the alias runs the 0.5.11 policy (squash, required checks by the rules, no gate, no review, the agent merges), printed in the plan |
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

The aliases are removed in 0.7.0. The upgrade page lists them and the change of section 6.2 for
failing checks that no rule requires.

`jst land --help`:

```
usage: jst land [--change N|URL] [--title T] [--body-file F] [--card ID]... [--reason TEXT]
                [--prepare] [--plan] [--timeout S] [--interval S]
       jst land status | reconcile [--change N|URL]
       jst land find --head BRANCH | --search TEXT

Land a change by the project's land policy: push and open it, wait for the checks its base
requires, check the review, gate the exact tree the merge makes, then merge with the head that
was checked, or stop ready for a person (land.autonomy, human paths). The plan is printed first.
This checkout is never switched, pulled or rebased.

  --prepare  stop before the merge this once (never the other way)
  --plan     print the plan and stop

exit: 0 landed or ready for a person; 1 refused or failed; 2 bad request; 3 pending; 4 moved;
5 unexpected host answer. The last line is `jst land: STATE`.
```

The AGENTS block (`internal/adopt/block`): the `github` and `queue` items and the `ci-evidence` item
go; two items come, each wanted when the project has a `land` section. The land item's text depends
on `land.autonomy`:

- `merge-when-green`: "**Land a change**: `jst land` (push, open, checks, review, gate on the merge
  tree, merge). It stops at `jst land: ready for a person` for `jackstay.json` and the paths in
  `land.human_paths`; say so and stop. Never merge on the host by hand."
- `prepare`: "**Land a change**: `jst land` prepares (push, open, checks, review, gate) and ends at
  `jst land: ready for a person`; a person merges. Never merge on the host by hand."
- checks item, both modes: "**A change's checks**: `jst checks wait` / `jst checks status`; only
  the checks the base requires block."

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
  - Checks: required pass, fail, cancelled, superseded with and without the newer job, missing,
    skipped; not required failing (reported, not blocking); rules unread; rules requiring none.
  - Policy: `jackstay.json` alone, `jackstay.json` with another file, a human path, `prepare`,
    `--prepare` in `merge-when-green`; the prompt refused without a terminal.
  - Review carried across a clean base merge, and not across a merge with conflicts resolved.
- **Races** (fake adapter, deterministic, no timing): the head moves between the gate and the merge
  (the host refuses the expected head; `moved`); the base moves after the final read (`merge-tree`:
  `landed-mismatch`; `up-to-date` with the host rule: refused); a rule changes during the attempt;
  the merge answer is lost and the change is merged (reconciled, one request); the merge is
  accepted and held (pending); two `jst land` on one change (the lock); two changes into one base
  (the second's base moves, a new attempt); a crash after the intent is saved (resume reads before
  any request); a status publish that fails with `direct` and with `auto-merge`.
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
- **Adopt and the block**: golden files for both autonomy modes, and for no `land` section.

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
