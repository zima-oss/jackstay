# Local merge candidate queue

The first queue lane holds one branch head for manual landing. It runs locally and works without
GitHub Enterprise or GitHub's native merge queue. It does not open, approve or merge a pull request.

Enable the queue in `jackstay.json` with a full ref for the target branch:

```json
"queue": {"enabled": true, "base_ref": "refs/heads/main"}
```

`base_ref` may name a local branch or a fetched remote-tracking branch such as
`refs/remotes/origin/main`. Fetch it yourself before preparing a candidate; jackstay does not
fetch or change a ref. A head must also be a full local or remote-tracking branch ref.

```sh
jst queue add --head refs/heads/my-change
jst queue prepare
jst queue test
jst queue status
jst queue handoff
jst queue remove
```

`add` pins the base and head commits in a local, persistent record shared by linked worktrees.
Only one head can be queued in this slice. `prepare` uses Git's merge result to write an
unreachable candidate commit and records its commit and tree IDs. It refuses a conflict without
changing a branch or the checkout. If the base or head moves, `status` shows `stale` and `prepare`
starts a new attempt with its prior evidence cleared.

`test` runs the configured `gate` on a clean copy of that exact candidate with
`jst test gate --commit`. It accepts the result only when the gate passes, the run log names that
candidate, and at least one counted step has an exportable portable receipt. Configure
`receipts.inputs` to cover the files the gate tests; a gate can pass without an exportable receipt,
but the queue then refuses handoff. `handoff` exports the saved receipts again and checks their
digests and candidate IDs, as well as current base and head refs. It prints the tested candidate,
tree, gate run and receipt identities for a person to inspect before merging.

Manual landing still has a race: the base or head can move after `handoff`. Recheck both refs at
the merge decision and compare the landed tree with the tested tree afterwards. The queue does
not claim that a manual GitHub merge enforced an atomic exact-tree condition. `remove` empties
the local lane after the operator has reconciled the result; it leaves refs untouched.

The [pilot plan](../roadmap/merge-queue-pilot.md) describes the next provider-backed slices,
including review and policy freshness, recovery after ambiguous effects, batching and automatic
landing only after an exact-candidate condition is proven.
