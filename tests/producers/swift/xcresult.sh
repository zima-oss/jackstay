#!/bin/sh
# Run one Xcode test plan and export the two reports an xcresult-json judge needs.
set -eu

: "${JACKSTAY_TEST_REPORT_DIR:?use this wrapper with a jst xcresult-json scope}"
for word do
  case "$word" in
    -resultBundlePath|-resultBundlePath=*)
      printf '%s\n' 'the wrapper supplies -resultBundlePath' >&2
      exit 2
      ;;
  esac
done

report="$JACKSTAY_TEST_REPORT_DIR"
bundle="$report/run.xcresult"
test_status=0
xcodebuild test -resultBundlePath "$bundle" "$@" || test_status=$?
if [ -d "$bundle" ]; then
  if ! xcrun xcresulttool get test-results summary --path "$bundle" --compact > "$report/summary.json"; then
    rm -f "$report/summary.json"
  fi
  if ! xcrun xcresulttool get test-results tests --path "$bundle" --compact > "$report/tests.json"; then
    rm -f "$report/tests.json"
  fi
fi
exit "$test_status"
