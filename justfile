# jackstay's developer recipes (W-035: a justfile, never a Makefile). jackstay.json's test scopes call these.
# Tool versions are pinned and recorded in docs/design/implementation.md section 10 (W-037).

set shell := ["bash", "-euo", "pipefail", "-c"]

# Go's caches where the agent sandbox can write them (its default under ~/Library cannot be).
export GOCACHE := env("GOCACHE", home_directory() / ".cache/go-build")
export GOMODCACHE := env("GOMODCACHE", home_directory() / "go/pkg/mod")
export GOLANGCI_LINT_CACHE := env("GOLANGCI_LINT_CACHE", home_directory() / ".cache/golangci-lint")
export HUGO_CACHEDIR := env("HUGO_CACHEDIR", home_directory() / ".cache/hugo")

golangci := "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0"
govulncheck := "golang.org/x/vuln/cmd/govulncheck@v1.9.0"
gosec := "github.com/securego/gosec/v2/cmd/gosec@v2.30.0"
# Hugo for the documentation site, built from its module at a pinned version (the standard edition:
# the Hextra theme needs no Sass); the theme itself is pinned by site/go.mod.
hugo := "github.com/gohugoio/hugo@v0.170.1"
# The site's address on GitHub Pages (site/hugo.yaml's baseURL), which `just docs` builds and checks it at
site_address := "https://zima-oss.github.io/jackstay/"

default:
    @just --list --unsorted

# The binary, into .build/jst (the checkout's own build; bin/jst runs it)
build:
    go build -trimpath -o .build/jst ./cmd/jst

# gofmt (fails on any file it would change), go vet, golangci-lint, shellcheck on install.sh
check:
    test -z "$(gofmt -l cmd internal)" || { gofmt -l cmd internal; echo "FAILED: gofmt"; exit 1; }
    go vet ./...
    go run {{golangci}} run ./...
    shellcheck -s sh install.sh

# Every Go test, with the race detector, and a build (jackstay.json scope x-go)
go:
    sh tests/go.sh

# govulncheck and gosec (jackstay.json scope x-sec); gosec walks folders, not the module, so the
# worktrees under .claude/ are left out
security:
    go run {{govulncheck}} ./...
    go run {{gosec}} -quiet -severity high -confidence medium -exclude-dir=.claude ./...

# The tests that need real git or processes (`//go:build integration`, a scope of their own; jackstay.json x-integration)
integration:
    #!/usr/bin/env bash
    set -euo pipefail
    out=$(mktemp); trap 'rm -f "$out"' EXIT
    status=0
    go test -count=1 -race -tags integration -v ./... > "$out" 2>&1 || status=$?
    grep -E '^(--- FAIL|FAIL|ok|panic:)' "$out" || true
    if [ "$status" -ne 0 ]; then grep -B2 -A20 -E '^--- FAIL' "$out" | head -200; echo "FAILED: go test -tags integration"; exit "$status"; fi
    echo "Ran $(grep -c -E '^    --- PASS|^--- PASS' "$out") tests"

# The tests that need a terminal (`//go:build integration && terminal`): the agent sandbox refuses
# /dev/ptmx, so they run outside it before an install, not in the gate (docs/design/implementation.md section 5.2)
terminal:
    go test -count=1 -race -tags 'integration terminal' -run 'Terminal' ./...

# A focused local macOS check. W-083 keeps the required CI gate on Linux.
darwin-smoke:
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ "$(uname -s)" != Darwin ]]; then
      echo "darwin-smoke: this check needs macOS" >&2
      exit 2
    fi
    mkdir -p .build
    test_home=$(mktemp -d "$PWD/.build/darwin-smoke.XXXXXX")
    trap 'rm -r "$test_home"' EXIT
    export HOME="$test_home" JACKSTAY_HOME="$test_home/.jackstay"
    git config --file "$test_home/.gitconfig" init.defaultBranch main
    git config --file "$test_home/.gitconfig" user.name "Jackstay smoke"
    git config --file "$test_home/.gitconfig" user.email "smoke@example.invalid"
    go test -count=1 -race -tags integration \
      -run 'Test(HookRefusesWithTheDocumentedAnswer|RealTheDeadlineStopsTheGroup|RealAHeldWriterIsNotStoppedUntilAsked|InstallStoppedAfterEachStep|ToolchainKeyUsesTheCommandsDirectoryAndEnvironment|TheScopeRunsOnACleanCopyOfTheCommitAndTheCopyGoes)$' \
      ./internal/guard ./internal/install ./internal/procs/background ./internal/receipts ./internal/testing

# jackstay's own documents: relative links lead somewhere, every decision and every module's
# licence is listed, everything is in English; and the site builds from them with no warning and no
# link that leads nowhere (site/layouts/_markup/render-link.html as the pages are made, then
# cmd/jst-sitelinks over every page the build wrote: the menus and the home page's buttons and cards
# too, at the Pages address's path); and a guide page has the sidebar beside it on a desktop (the
# guide is of type docs: Hextra's default type hides it, `hx:md:hidden`), its desktop list (not the
# phone's menu, which lists the guide too) naming the current page and the guide's first
docs:
    go test -count=1 -v ./internal/docs
    folder="$(mktemp -d)" && trap 'rm -r "$folder"' EXIT && \
    go run {{hugo}} --source site --panicOnWarning --baseURL {{site_address}} --destination "$folder/public" --quiet && \
    go run ./cmd/jst-sitelinks "$folder/public" {{site_address}} && \
    page="$folder/public/guide/tests/index.html" && \
    sidebar="$(grep -o '<aside class="hextra-sidebar-container[^"]*"' "$page")" && \
    desktop="$(perl -0ne 'print $1 if /<ul class="[^"]*hx:max-md:hidden[^"]*">(.*?)<\/aside>/s' "$page")" && \
    if [[ "$sidebar" != *hx:md:sticky* || "$sidebar" == *hx:md:hidden* ]] || \
      [[ "$desktop" != *hextra-sidebar-active-item* ]] || \
      [[ "$desktop" != *'href="/jackstay/guide/getting-started/"'* ]]; then \
      echo "FAILED: docs: the guide's sidebar is not shown on a desktop ($page)"; exit 1; fi && \
    echo "docs: the site builds"

# The generated pages, written anew from the code: every command's help (internal/cli/testdata/help.txt),
# the command and configuration references (docs/reference/) and the editor schema
# (schemas/jackstay.schema.json). cmd/jst's tests fail while any of them differs from the code.
reference:
    go test -count=1 ./cmd/jst -run 'TestEveryCommandHelp|TestReferencesAreCurrent' -update
    go run ./cmd/jst-schema > schemas/jackstay.schema.json
    echo "reference: written; commit what changed"

# The documentation site (W-076), built into .build/site as the Pages workflow builds and publishes
# it, at HUGO_BASEURL (the Pages workflow's) or the site's address, and its links checked there
site:
    go run {{hugo}} --source site --panicOnWarning --gc --minify --destination ../.build/site
    go run ./cmd/jst-sitelinks .build/site "${HUGO_BASEURL:-{{site_address}}}"

# The documentation site served at http://localhost:1313, rebuilt on every change to docs/ or site/;
# rendered in memory, so it leaves nothing in site/
site-serve:
    go run {{hugo}} server --source site --panicOnWarning --renderToMemory

# The chat delivery model (docs/design/chat-model) checked by TLC, when Java (JAVA, default `java`)
# and the TLA+ tools (TLA2TOOLS, default ~/.cache/tla/tla2tools.jar) are installed; otherwise it says
# so and passes, since the gate runs the same model as Go tests (internal/sessionchat/deliverystate)
tla:
    #!/usr/bin/env bash
    set -euo pipefail
    jar="${TLA2TOOLS:-$HOME/.cache/tla/tla2tools.jar}"
    java="${JAVA:-java}"
    if [ ! -f "$jar" ] || ! "$java" -version > /dev/null 2>&1; then
      echo "tla: skipped: no Java, or no TLA+ tools at $jar (docs/design/chat-model/README.md)"
      exit 0
    fi
    cd docs/design/chat-model
    for model in ChatDelivery ChatSend; do
      meta=$(mktemp -d); out=$(mktemp)
      status=0
      "$java" -XX:+UseParallelGC -cp "$jar" tlc2.TLC -workers auto -metadir "$meta" \
        -config "$model.cfg" "$model.tla" > "$out" 2>&1 || status=$?
      grep -E '^[0-9]+ states generated|^Error|is violated|No error has been found' "$out" || true
      rm -r "$meta"
      if [ "$status" -ne 0 ] || ! grep -q 'No error has been found' "$out"; then
        cat "$out"; rm -f "$out"; echo "FAILED: tla $model"; exit 1
      fi
      rm -f "$out"
      echo "tla: $model holds"
    done

# What a push needs (W-040): the Go checks, the integration tests, the security checks and the
# docs check.
gate:
    #!/usr/bin/env bash
    set -euo pipefail
    total=0; log=$(mktemp); trap 'rm -f "$log"' EXIT
    for recipe in check go integration security docs; do
      echo "gate: $recipe"
      if ! just "$recipe" > "$log" 2>&1; then cat "$log"; echo "FAILED: gate part $recipe"; exit 1; fi
      count=$(sed -n 's/^Ran \([0-9][0-9]*\) tests\{0,1\}.*/\1/p' "$log" | awk '{ total += $1 } END { if (NR) print total }')
      [ -n "$count" ] || count=$(grep -cE '^ *--- PASS' "$log" || true)
      echo "  $recipe: ${count:-0} tests"
      total=$((total + ${count:-0}))
    done
    echo "Ran $total tests"

# The guard hook's wall time with telemetry off and on (docs/design/observability.md section 8), with
# a binary built without telemetry beside it when JACKSTAY_LATENCY_BASELINE names one; not in the gate
# (timing is no pass/fail on a shared machine); its numbers belong with the change that moves them
guard-latency runs="500":
    JACKSTAY_LATENCY_RUNS={{runs}} go test -count=1 -tags latency -run TestGuardLatency -v ./internal/guard/latency/ | grep latency_test

# The release configuration checked by GoReleaser (pinned), as the release workflow runs it
release-check:
    go run github.com/goreleaser/goreleaser/v2@v2.20.2 check

# A release build of every platform into a new folder of its own (it says which), nothing published
# and no earlier build touched; nothing is signed unless the MACOS_* secrets are in the environment
# (docs/design/implementation.md section 6). A release is published only by
# .github/workflows/release.yml, when a version tag is pushed (W-075).
release-snapshot:
    folder="$(mktemp -d)" && \
    { cat .goreleaser.yaml; printf '\ndist: "%s"\n' "$folder/dist"; } > "$folder/goreleaser.yaml" && \
    JST_LICENCES="$folder/licenses" go run github.com/goreleaser/goreleaser/v2@v2.20.2 release --snapshot --skip=publish --config "$folder/goreleaser.yaml" && \
    echo "release-snapshot: the archives are in $folder/dist"
