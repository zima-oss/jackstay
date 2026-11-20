# Maven Surefire test reports

jackstay can judge JUnit XML reports from Maven Surefire. The verified producer is Maven 3.9.11,
Surefire 3.5.4 and TestNG 7.11.0. A single selected class writes a `TEST-<class>.xml` report.
jackstay requires every declared report file to be present and valid before it accepts a passing
run.

Configure Surefire in the project's `pom.xml` to write into jackstay's private report directory:

```xml
<properties>
  <jackstay.reportsDirectory>${project.build.directory}/surefire-reports</jackstay.reportsDirectory>
</properties>
<build>
  <plugins>
    <plugin>
      <groupId>org.apache.maven.plugins</groupId>
      <artifactId>maven-surefire-plugin</artifactId>
      <version>3.5.4</version>
      <configuration>
        <reportsDirectory>${jackstay.reportsDirectory}</reportsDirectory>
      </configuration>
    </plugin>
  </plugins>
</build>
```

Then declare a selected test class in `jackstay.json`:

```json
{
  "test": {
    "scopes": {
      "full": {
        "run": ["mvn", "-B", "-ntp", "-Djackstay.reportsDirectory={report_dir}",
                "-Dtest=FirstTest", "test"],
        "judge": "junit",
        "reports": ["TEST-example.FirstTest.xml"],
        "when": "before handing over"
      }
    }
  }
}
```

Replace the class and report name with those produced by your project. The exact file name must
be known before the run. `{report_dir}` gives Maven a fresh directory for each attempt, and
`reports` does not expand globs. Run `jst test full --no-reuse` to execute the scope.

With this pinned TestNG provider, `-Dtest=FirstTest,SecondTest` aggregates both classes into one
`TEST-TestSuite.xml` report. To keep two class reports, invoke Maven once per selected class with
different `-Dsurefire.reportNameSuffix` values. A wrapper can receive the directory as its first
argument or through `JACKSTAY_TEST_REPORT_DIR`:

```sh
#!/bin/sh
set -eu
report_dir=$1
mvn -B -ntp -Djackstay.reportsDirectory="$report_dir" -Dtest=FirstTest \
  -Dsurefire.reportNameSuffix=first test
mvn -B -ntp -Djackstay.reportsDirectory="$report_dir" -Dtest=SecondTest \
  -Dsurefire.reportNameSuffix=second test
```

```json
"full": {
  "run": ["./scripts/run-two-test-classes.sh", "{report_dir}"],
  "judge": "junit",
  "reports": ["TEST-example.FirstTest-first.xml", "TEST-example.SecondTest-second.xml"]
}
```

This wrapper is an example for fixed classes. A changing full suite needs a way to declare all
expected report names before the run. The checked-in producer integration test exercises the two
Maven invocations and verifies both XML files.

In the pinned producer, a passed case and a skipped case produce a passing report; an all-skipped
class is `nothing-run`; a failed case produces XML and Maven exits 1. When a `-Dtest` selection
matches no class, Surefire normally fails. With `-Dsurefire.failIfNoSpecifiedTests=false`, it
exits 0 but writes no XML, so jackstay marks the missing declared report `unjudged`. Do not use
an empty selection as evidence of a completed test run.

## Tests that pass on retry

With `-Dsurefire.rerunFailingTestsCount=N`, Surefire runs a failed test again up to N times. The
pinned producer test checks this with JUnit 5 (JUnit Jupiter 5.13.4) and these results:

- A test that fails and then passes keeps one test case. Its failed attempts appear as
  `flakyFailure` or `flakyError` records. jackstay counts the case and judges the run
  `passed-on-retry` (exit 78).
- A test that fails every rerun keeps its `failure` or `error`, followed by `rerunFailure` or
  `rerunError` records. The run is `failed`.

`passed-on-retry` is not a clean pass. jackstay stores no receipt for it, and a gate whose step
ends `passed-on-retry` fails. The last `jst test:` line says how many tests passed only on retry.
The report directory is removed when the run ends, but jackstay keeps Maven's output
(`jst test last` names the file). Surefire prints each failed attempt there with its stack trace,
and its summary lists the attempts of each flaky test (`Run 1: …`). To keep the XML as well,
copy it out of `{report_dir}` in a wrapper. Fix the test, then run the scope again.

When a suite has rerun records, Surefire writes its `tests`, `failures`, `errors` and `skipped`
totals from the last rerun only. jackstay therefore does not compare that suite's totals with its
cases. It still counts every case and checks the results inside each one.

TestNG's `retryAnalyzer` works differently. The pinned producer test also covers a TestNG retry that
fails once and then passes. Surefire 3.5.4 writes both attempts under the same test case identity,
with the first attempt skipped. jackstay returns `unjudged` because it cannot tell which attempt
gives the final result. Do not use TestNG retries in a scope that requires a counted pass.

## Separate scopes

Independently selected classes can run as separate scopes or as the two declared reports in the
wrapper above. Each declared file must come from its intended selection.

Multi-module aggregation and other Surefire versions need their own producer proof and report
inventory. Each module needs declared report paths and a completion rule before it can be treated
as one test scope. The [Surefire plugin reference](https://maven.apache.org/surefire/maven-surefire-plugin/)
documents its report files and [test goal options](https://maven.apache.org/surefire/maven-surefire-plugin/test-mojo.html),
including `reportsDirectory` and no-test selection behavior.
