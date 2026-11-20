# Workspace board across repositories

jackstay 0.6.0 removed the local merge queue (`jst queue`), and with it the board's `queue` source;
the queue rows below are history.

## Problem and scope

An operator supervising several local agent sessions needs one quick view of work spread across
independent Git repositories. The board is an observation command. It reads only paths the operator
supplies, does not discover other projects, and does not contact GitHub or an agent host. Its first
slice shows local task cards, the local merge queue, test evidence, and session chat availability.
The board cannot decide whether work should be merged or whether a session has processed a chat
message. The existing commands remain the authority for those actions.

## Workflow

```sh
jst workspace board ~/src/app ~/src/service
jst workspace board --json ~/src/app ~/src/service
```

Each argument is an explicit checkout path. Relative paths are resolved against the caller's
working directory; `~` is expanded by the shell in normal shell use. A path inside a checkout
resolves to its Git top level. The command prints one row for each argument in input order,
including duplicates if supplied. There is no implicit `.` and no repository scan. The operator
can pipe the JSON output to another local tool. The command exits 0 when all requested sources
are known, 1 when at least one source is unknown or only partly read, and 2 for invalid command
syntax. An absent but configured source is `none` or `empty`, not a command failure.

## Repository identity and isolation

The board confirms each path is a Git checkout, finds its top level with Git, and requires
`jackstay.json` at that top level. It uses the resolved top level for checkout-specific sources,
and the resolved Git common directory for state shared by linked worktrees. Queue state uses its
existing project-name and Git-common-directory key. Chat state uses its existing Git-common-
directory namespace. Two independent repositories with the same `project` name must not share
queue or chat rows. Linked worktrees of one repository may show different cards, gates, and
receipts, while their queue and chat summaries agree.

The board never infers that two distinct remote URLs identify one project. An absent repository,
unreadable configuration, or failed Git query affects only that row. Other rows continue.

## Sources and meanings

| Field | Local source | Meaning |
| --- | --- | --- |
| `cards` | Card files directly in `cards.dir` in the named checkout | IDs and front-matter states other than `done` or `verified` are active. Bodies are never emitted. A missing or malformed state makes the card source `unknown`. |
| `queue` | Local merge queue state keyed by project and common Git directory | `disabled` if `queue.enabled` is false/absent, `empty` if enabled with no lane, `queued` for an active lane, `landed` when a recorded outcome exists. The board reads without creating a lock or state directory. |
| `gate` | Latest completed outer gate event in the shared run log for the exact checkout root and project | Reports the recorded outcome and end time. It does not rerun checks or imply that GitHub CI is current. |
| `receipt` | Latest completed, stored, passed run event in the same log for the exact checkout root and project | Reports that a receipt was recorded, not that it remains reusable under the current receipt policy. No receipt material or command output is emitted. |
| `chat` | Per-common-directory session chat state and current `chat` policy | `disabled` if chat is off; `available` if enabled and its state is readable or absent. Counts retained discoverable sessions and unacknowledged messages after applying retention in memory. No message bodies, names, IDs, or tokens are emitted. |

The latest gate and receipt are selected by recorded end epoch, then log order for equal epochs.
The source is `none` if the run log is missing or has no matching event. A malformed or unreadable
run log is `unknown`; a partial/truncated last line must not silently be interpreted as complete
evidence. The board does not present raw receipt files as verified artifacts.

## Freshness and incomplete information

Every board response has `observed_at` in UTC. Each row is a snapshot, not a transaction across
the independent stores. The source states have these exact meanings:

- `ok` means the source was read and classified from a valid local snapshot. It does not assert
  that the source will still be the same after the command returns.
- `none` and `empty` mean the source is configured and readable, but no matching record or queue
  lane exists.
- `disabled` means the project configuration does not enable that source.
- `unknown` means the board cannot establish a trustworthy answer; the field carries a concise
  error without source contents. The row remains present and the board is `partial`.
- `queued` and `landed` describe a valid queue snapshot, not permission to merge.
- `recorded` describes a matching local run event, not a fresh or cryptographically validated
  artifact.
- `available` describes a chat namespace that can be read. It does not prove that an agent host
  is connected or that a turn is active.

`freshness` is one of `current`, `stale`, or `unknown`. For a queue lane, `current` means both
saved base and head refs still resolve to the saved commits; `stale` means one differs. A
recorded landing keeps its explicit tree-match result and is not reclassified as a live lane.
For a gate or receipt, `current` means the recorded `head` equals the named checkout's current
`HEAD`; `stale` means it differs. This does not account for uncommitted edits, changed external
CI status, changed runners, expired receipts, or changed configuration. Missing record head or a
failed Git HEAD lookup yields `unknown`. Empty or disabled sources have `unknown` freshness.

The board includes source timestamps where available: queue update time and gate/receipt record
time. It does not estimate time since observation using a hidden threshold. Consumers can compare
timestamps with `observed_at` and apply their own policy.

## CLI and machine output

Human output is one concise status line per checkout, with a short indented diagnostic for each
unknown source. JSON is one object with a schema number, `observed_at`, `partial`, and an ordered
`repositories` array. Each row has `path`, `project` when known, `status`, and separate `cards`,
`queue`, `gate`, `receipt`, and `chat` objects. Source objects always include `status`; queue,
gate, and receipt always include `freshness`. Optional details are omitted when they are not
known. Card entries contain only `id` and `state`. Queue exposes `head_ref`, candidate identity,
update time, and recorded tree match where applicable. Gate and receipt expose only `outcome` and
record time. Chat exposes integer session and pending counts. JSON contains no chat body, agent
name, token, test output, command arguments, card body, or receipt material.

The schema number is incremented for a breaking JSON change. Consumers must accept extra
fields in a version they understand. The command's telemetry must mark arguments private so
repository paths do not enter command spans. Error text is limited to source/path diagnostics;
JSON has no raw file contents.

## Read-only and resource bounds

All readers use ordinary file reads and Git queries with optional locks disabled. The board
never calls a status method that creates a lock file, prunes a store, touches a session, writes a
cache, updates a ref, or fetches a remote. It must not write to `JACKSTAY_HOME`, a checkout, or
the Git common directory. Git questions have a bounded timeout. Input files have explicit size
limits before parsing, and errors remain local to one source/repository. The command should avoid
loading or printing body content except the minimum needed to parse card front matter; any body
read is bounded. The number of supplied repositories should have a documented upper bound to
prevent accidental large scans. The first slice can use a fixed bound because repository paths
are supplied per invocation; a future config section may add saved board views with a schema.
Byte sources are opened with nonblocking reads and must be regular files. A FIFO, device, or
directory is `unknown` without waiting for a writer or reading from a special file.

## Rollout and acceptance

The first slice is a local CLI feature and guide. It does not publish a server or install hooks.
`jst workspace board --help` states the source meanings and exit codes; the guide shows how to
interpret partial and stale output. The command is enabled for all local projects and reads each
project's existing cards, queue, and chat configuration. Future saved views or server UI require
their own design and privacy review.

| Case | Expected result |
| --- | --- |
| Two independent repos with the same project name | Two distinct rows; queue and chat keyed by distinct Git common directories. |
| Main checkout and linked worktree | Checkout-specific cards/gate/receipt; shared queue/chat. |
| Configured cards with ready and done files | Only ready card appears as active. |
| Malformed card front matter | Cards `unknown`, row/board partial, other sources still shown. |
| Enabled queue with no lane, then moved head ref | `empty`, then `queued` with `stale` freshness. |
| Queue state corruption | Queue `unknown`, board partial; no lock or state files created. |
| Gate/receipt for old checkout HEAD | `recorded` and `stale`; no claim of current proof. |
| Missing run log | Gate/receipt `none`; no file created. |
| Malformed or truncated run log | Gate/receipt `unknown`, board partial. |
| Card, run log, or chat state path is a FIFO | Its source is `unknown` immediately; no writer or timeout is needed for the test. |
| Chat enabled with no state, then pending and acknowledged messages | `available` with zero, then pending count, then zero; no bodies or IDs in output. |
| Chat state corruption | Chat `unknown`, board partial; no store update or pruning. |
| Missing repository followed by valid repository | First row `unknown`, second row complete; exit 1 and valid JSON. |
| Unknown flag or no path | Exit 2 and usage; no repository read. |
| Read-only fixture with writable-state directory absent | Same files and directory mtimes before and after the command. |

Run focused unit/integration tests for these cases, then `just gate` on macOS and Linux CI before
shipping. A full gate failure is investigated before the feature is considered complete.
