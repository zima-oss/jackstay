#!/bin/sh
set -eu

runner=$1
jst=$2
fixtures=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT

export JACKSTAY_HOME="$work/jackstay" JACKSTAY_TELEMETRY=off
mkdir "$work/project"
cd "$work/project"
git init -q

expect_outcome() {
  expected_status=$1
  expected_text=$2
  shift 2
  status=0
  "$jst" test "$@" >"$work/result" 2>&1 || status=$?
  if [ "$status" -ne "$expected_status" ] || ! grep -F "$expected_text" "$work/result"; then
    cat "$work/result" >&2
    return 1
  fi
}

case $runner in
  jest)
    PATH="$(dirname "$3"):$PATH"
    export PATH
    cp "$fixtures/jest/"*.test.js .
    printf '%s\n' '{}' > jest.config.json
    cat > jackstay.json <<'JSON'
{"jackstay_schema":3,"project":"jest-producer","test":{"scopes":{
  "x-pass":{"judge":"jest-json","report":"jest.json","run":["jest","--runInBand","--no-watchman","--json","--outputFile={report}","--runTestsByPath","sample.test.js"]},
  "x-fail":{"judge":"jest-json","report":"jest.json","run":["jest","--runInBand","--no-watchman","--json","--outputFile={report}","--runTestsByPath","fail.test.js"]},
  "x-empty":{"judge":"jest-json","report":"jest.json","run":["jest","--runInBand","--no-watchman","--json","--outputFile={report}","--runTestsByPath","sample.test.js","--testNamePattern=absent"]},
  "x-discovery":{"judge":"jest-json","report":"jest.json","run":["jest","--runInBand","--no-watchman","--json","--outputFile={report}","--runTestsByPath","sample.test.js","--collectTests"]},
  "x-retry":{"judge":"jest-json","report":"jest.json","run":["jest","--runInBand","--no-watchman","--json","--outputFile={report}","--runTestsByPath","retry.test.js"]},
  "x-shard-one":{"judge":"jest-json","report":"jest.json","run":["jest","--runInBand","--no-watchman","--json","--outputFile={report}","--shard=1/2","--runTestsByPath","sample.test.js","shard.test.js"]},
  "x-pick":{"judge":"jest-json","report":"jest.json","selector":"jest-test","run":["jest","--runInBand","--no-watchman","--json","--outputFile={report}","{test_args}"]},
  "x-shard-two":{"judge":"jest-json","report":"jest.json","run":["jest","--runInBand","--no-watchman","--json","--outputFile={report}","--shard=2/2","--runTestsByPath","sample.test.js","shard.test.js"]}
}}}
JSON
    expect_outcome 0 'counted=1 skipped=2' x-pass
    expect_outcome 1 'outcome=failed' x-fail
    expect_outcome 76 'outcome=nothing-run' x-empty
    expect_outcome 77 'the Jest report is a discovery result' x-discovery
    expect_outcome 78 'outcome=passed-on-retry' x-retry
    expect_outcome 0 'counted=1 skipped=0' x-shard-one
    expect_outcome 0 'counted=1 skipped=2' x-shard-two
    expect_outcome 0 'outcome=passed' x-pick 'selection.test.js::group brackets [1]'
    expect_outcome 76 'outcome=nothing-run' x-pick 'selection.test.js::group absent'
    ;;
  trx)
    export DOTNET_CLI_HOME="$work/dotnet" DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_GENERATE_ASPNET_CERTIFICATE=false
    cp "$fixtures/trx/trx.csproj" "$fixtures/trx/Cases.cs" .
    dotnet restore trx.csproj --verbosity quiet
    cat > jackstay.json <<'JSON'
{"jackstay_schema":3,"project":"trx-producer","test":{"scopes":{
  "x-pass":{"judge":"trx","report":"run.trx","run":["dotnet","test","trx.csproj","--no-restore","--disable-build-servers","-p:UseSharedCompilation=false","--filter","Name!=Failure","--logger","trx;LogFileName={report}"]},
  "x-fail":{"judge":"trx","report":"run.trx","run":["dotnet","test","trx.csproj","--no-restore","--disable-build-servers","-p:UseSharedCompilation=false","--filter","Name=Failure","--logger","trx;LogFileName={report}"]},
  "x-empty":{"judge":"trx","report":"run.trx","run":["dotnet","test","trx.csproj","--no-restore","--disable-build-servers","-p:UseSharedCompilation=false","--filter","Name=Missing","--logger","trx;LogFileName={report}"]}
}}}
JSON
    expect_outcome 0 'counted=1 skipped=1' x-pass
    expect_outcome 1 'outcome=failed' x-fail
    expect_outcome 76 'outcome=nothing-run' x-empty
    ;;
  *) printf '%s\n' 'expected jest or trx' >&2; exit 2 ;;
esac
