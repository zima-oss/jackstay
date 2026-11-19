# Licences

jackstay is under the [Apache License 2.0](../LICENSE): the text as the Apache Software Foundation
publishes it, with its appendix's example notice filled in (`Copyright 2026 Max Zima`); the docs
check holds it to the published text. There are no per-file headers (the licence does not ask for them) and no
`NOTICE` file (no dependency has one, measured below).

## The modules jackstay uses

Every module `go.mod` requires, with its licence as its own licence file says (read from the module
cache, 2026-11-17). `internal/docs` fails the gate when `go.mod` requires a module, or a version,
that this table does not list: a new dependency, or a new version of one, is read here first.

"In the binary" is what `go version -m` shows for `jst`; the others are used by tests, or by a
developer tool run with `go run` (`cmd/jst-sitelinks`, which `just docs` runs on the documentation
site).

| Module | Version | Licence | In the binary |
|---|---|---|---|
| `github.com/cespare/xxhash/v2` | v2.3.0 | MIT | yes |
| `github.com/go-logr/logr` | v1.4.4 | Apache-2.0 | yes |
| `github.com/go-logr/stdr` | v1.2.2 | Apache-2.0 | yes |
| `github.com/google/go-github/v93` | v93.0.0 | BSD-3-Clause | yes |
| `github.com/google/go-querystring` | v1.2.0 | BSD-3-Clause | yes |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | yes |
| `go.opentelemetry.io/auto/sdk` | v1.2.1 | Apache-2.0 | yes |
| `go.opentelemetry.io/otel` | v1.47.0 | Apache-2.0 | yes |
| `go.opentelemetry.io/otel/log` | v1.47.0 | Apache-2.0 | yes |
| `go.opentelemetry.io/otel/metric` | v1.47.0 | Apache-2.0 | yes |
| `go.opentelemetry.io/otel/sdk` | v1.47.0 | Apache-2.0 | yes |
| `go.opentelemetry.io/otel/trace` | v1.47.0 | Apache-2.0 | yes |
| `go.opentelemetry.io/proto/otlp` | v1.11.1 | Apache-2.0 | no (tests) |
| `golang.org/x/net` | v0.63.0 | BSD-3-Clause | no (`cmd/jst-sitelinks`: its HTML parser) |
| `golang.org/x/sync` | v0.25.0 | BSD-3-Clause | yes |
| `golang.org/x/sys` | v0.50.0 | BSD-3-Clause | yes |
| `google.golang.org/protobuf` | v1.36.12 | BSD-3-Clause | no (tests) |
| `mvdan.cc/sh/v3` | v3.14.1 | BSD-3-Clause | yes |

## Test report producer fixtures

Separate producer checks use pinned tools to generate representative reports. These dependencies
are confined to test fixtures and their CI jobs.

| Tool | Version | Licence | Role |
|---|---|---|---|
| [Maven](https://maven.apache.org/ref/3.9.11/) | 3.9.11 | Apache-2.0 | Run the JVM fixture |
| [Maven Surefire](https://maven.apache.org/surefire/maven-surefire-plugin/) | 3.5.4 | Apache-2.0 | Generate JUnit XML |
| [TestNG](https://github.com/testng-team/testng) | 7.11.0 | Apache-2.0 | Execute JVM fixture cases |
| [Jest](https://github.com/jestjs/jest) | 30.5.2 | MIT | Generate Jest JSON |
| [MSTest](https://github.com/microsoft/testfx) | 4.2.3 | MIT | Execute .NET fixture cases through VSTest |

## The documentation site

The site (`site/`, W-076) is built with tools and code that are not in `jst` and not in `go.mod`,
each under a licence of the compatible set below (read 2026-11-18):

| What | Version | Licence | Where |
|---|---|---|---|
| Hugo (`github.com/gohugoio/hugo`) | v0.170.1 | Apache-2.0 | run with `go run` by `just docs` and `just site`; not shipped |
| the Hextra theme (`github.com/imfing/hextra`) | v0.13.0 | MIT | `site/go.mod`; fetched at build, its files are in the published site |
| FlexSearch | 0.8.143 | Apache-2.0 | vendored in `site/assets/lib/flexsearch-0.8.143/`, its licence header kept |

## What this means

- **Compatible.** Apache-2.0, MIT and BSD-3-Clause code may be combined and shipped under
  Apache-2.0. A dependency under any other licence (GPL, LGPL, MPL, AGPL, SSPL, BUSL, or none) is not
  added without a decision recorded first.
- **Shipping a binary.** MIT and BSD-3-Clause ask that their copyright notice and licence text go
  with a binary that contains them, and Apache-2.0 asks for its licence text. A release archive of
  `jst` therefore carries jackstay's `LICENSE` and the licence files of the modules marked "in the
  binary" ([the install guide](guide/install.md)).
  `go install` from source needs nothing more: the sources carry their own licences.
- **Contributions** are under Apache-2.0 by the licence's own section 5; there is no separate
  contributor agreement.
- **The name.** Section 6 of the licence gives no right to the name jackstay: a fork names itself.
