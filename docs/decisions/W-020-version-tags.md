# W-020 — One version, one commit; annotated `vX.Y.Z` tags, never moved

Date: 2026-11-16.

## Context

Early versions of jackstay were installed from several commits under one version number, so a
version did not name one build.

## Decision

- A version number names exactly one commit. A build that ships changed code moves the version.
- Each version is marked by an annotated tag `vX.Y.Z` on that commit.
- A tag is never moved. A wrong release is followed by the next version.

## Consequences

- A user can always tell which code a version contains.
- Releases are made from tags by the release workflow
  ([W-075](W-075-release-through-public-ci.md)).
