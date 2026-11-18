# W-005 — The machine-wide test lock is off by default, with a stated threshold for turning it on

Date: 2026-11-14.

## Context

Several agents on one machine often run test suites at the same time. jackstay has a machine-wide
lock that serialises heavy runs, but measurements showed that overlapping runs are normal and were
not shown to slow or break suites. Locking by default would make agents wait for nothing.

## Decision

- `jst test` takes no lock unless the project's `jackstay.json` says `"lock": {"enabled": true}`,
  or the call says `--lock`. `jst lock -- COMMAND` stays as the explicit tool.
- A test that fails on a busy machine is fixed as a test, not shielded by the lock.
- The lock is proposed for a project only when `jst baseline` shows evidence
  (`lock_evidence.shown`): a span of at least 7 days; at least 50 recorded runs on at least 5 days;
  at least 20 of them alone; at least 5 failures beside another run that the unchanged tree passed
  again within two hours; and a rate of such failures at least three times that of runs alone.
- Turning the lock on is always a deliberate project decision; nothing turns it on by itself.

## Consequences

- Runs never wait by default. The run record still collects the data that would justify a lock.
- See [the specification](../design/implementation.md) sections 5.4 and 9.6, and
  [testing](../design/testing.md).
