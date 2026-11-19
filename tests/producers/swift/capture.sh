#!/bin/sh
set -eu

fixtures=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
output=${1:-$(mktemp -d "${TMPDIR:-/tmp}/jackstay-swift-pilot.XXXXXX")}
mkdir -p "$output"
output=$(CDPATH='' cd -- "$output" && pwd)
if [ -n "$(ls -A "$output")" ]; then
  printf 'capture output must be empty: %s\n' "$output" >&2
  exit 2
fi
cp -R "$fixtures" "$output/project"
cd "$output/project"

swift --version > "$output/swift-version.txt"

capture_swift() {
  name=$1
  expected=$2
  shift 2
  actual=0
  swift test "$@" --xunit-output "$output/$name.xml" > "$output/$name.log" 2>&1 || actual=$?
  if [ "$actual" -ne "$expected" ]; then
    tail -n 40 "$output/$name.log" >&2
    printf '%s returned %s, expected %s\n' "$name" "$actual" "$expected" >&2
    exit 1
  fi
  printf '%s exit=%s\n' "$name" "$actual"
}

capture_swift pass 0 --event-stream-output-path "$output/events.jsonl" --event-stream-version 0
capture_swift modern-selected 0 --skip-build --filter 'ModernTests.modernPass()'
capture_swift legacy-selected 0 --skip-build --filter 'LegacyTests.LegacyTests/testPass'
capture_swift missing 0 --skip-build --filter NoSuchTest
JACKSTAY_SWIFT_FAIL=1 capture_swift failed 1 --skip-build
unset JACKSTAY_SWIFT_FAIL

if command -v xcodebuild >/dev/null 2>&1 && command -v xcrun >/dev/null 2>&1 &&
  xcodebuild -version > "$output/xcode-version.txt" 2>&1 &&
  xcrun --find xcresulttool >/dev/null 2>&1; then
  capture_xcode() {
    name=$1
    expected=$2
    actual=0
    xcodebuild test -scheme SwiftStructuredPilot-Package -destination 'platform=macOS' \
      -resultBundlePath "$output/$name.xcresult" -derivedDataPath "$output/DerivedData" \
      > "$output/$name.log" 2>&1 || actual=$?
    if [ "$expected" = fail ] && [ "$actual" -eq 0 ]; then
      tail -n 40 "$output/$name.log" >&2
      printf '%s unexpectedly passed\n' "$name" >&2
      exit 1
    fi
    if [ "$expected" = pass ] && [ "$actual" -ne 0 ]; then
      tail -n 40 "$output/$name.log" >&2
      printf '%s returned %s\n' "$name" "$actual" >&2
      exit 1
    fi
    xcrun xcresulttool get test-results summary --path "$output/$name.xcresult" --compact \
      > "$output/$name-summary.json"
    xcrun xcresulttool get test-results tests --path "$output/$name.xcresult" --compact \
      > "$output/$name-tests.json"
    printf '%s exit=%s\n' "$name" "$actual"
  }
  capture_xcode xcode-pass pass
  sed -i '' 's/XCTAssertEqual(2 + 2, 4)/XCTAssertEqual(2 + 2, 5)/' Tests/LegacyTests/LegacyTests.swift
  sed -i '' 's/#expect(2 + 2 == 4)/#expect(2 + 2 == 5)/' Tests/ModernTests/ModernTests.swift
  capture_xcode xcode-failed fail
else
  printf 'Xcode test-results unavailable; SwiftPM captures complete\n'
fi

printf 'captured=%s\n' "$output"
