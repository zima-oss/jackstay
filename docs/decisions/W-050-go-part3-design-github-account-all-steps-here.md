# W-050 — GitHub through go-github, with a project's own `github.account`

Date: 2026-11-16.

## Context

jackstay talks to GitHub for its `gh`, `tidy`, `release` and `lint` commands. Calling the `gh` CLI
for each request is slower and harder to test than a library. A user may also be logged in to
several GitHub accounts, and a project should not depend on which one is active.

## Decision

- jackstay talks to GitHub through the go-github library, one client for every command. Checks are
  read from GraphQL `statusCheckRollup`; only reads are retried.
- `jackstay.json` has a `github` section. `github.account` is required wherever jackstay asks
  GitHub outside GitHub Actions. Its token comes from `gh auth token --user <account>`, without
  changing the `gh` CLI's active account, and `GET /user` checks it.

## Consequences

- A user with several GitHub accounts names the right one per project.
- See [the specification](../design/implementation.md) section 8.1 and the guide's
  [branches and GitHub](../guide/branches-and-github.md).
