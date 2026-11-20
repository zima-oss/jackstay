#!/bin/sh
set -eu

jst=$1
fixtures=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT

export JACKSTAY_HOME="$work/jackstay" JACKSTAY_TELEMETRY=off
export DOTNET_CLI_HOME="$work/dotnet" DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_GENERATE_ASPNET_CERTIFICATE=false
mkdir "$work/project"
cd "$work/project"
git init -q
cp "$fixtures/trx.csproj" "$fixtures/Cases.cs" "$fixtures/RetryCases.cs" .
dotnet restore trx.csproj --verbosity quiet

cat > jackstay.json <<'JSON'
{"jackstay_schema":3,"project":"trx-partitions","test":{"scopes":{
  "x-retry":{"judge":"trx","report":"run.trx","run":["dotnet","test","trx.csproj","--no-restore","--disable-build-servers","-p:UseSharedCompilation=false","--filter","Name=PassesOnRetry","--logger","trx;LogFileName={report}"]},
  "x-success":{"judge":"trx","report":"run.trx","run":["dotnet","test","trx.csproj","--no-restore","--disable-build-servers","-p:UseSharedCompilation=false","--filter","Name=Success","--logger","trx;LogFileName={report}"]}
}}}
JSON

for scope in x-retry x-success; do
  "$jst" test "$scope" --no-reuse > "$work/$scope.out" 2>&1 || {
    cat "$work/$scope.out" >&2
    exit 1
  }
  if ! grep -F 'counted=1 skipped=0' "$work/$scope.out"; then
    cat "$work/$scope.out" >&2
    exit 1
  fi
done
