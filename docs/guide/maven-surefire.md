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

The pinned producer test also exercises a TestNG retry that fails once and then passes. Surefire
3.5.4 writes both attempts under the same test case identity, with the first attempt skipped.
jackstay returns `unjudged` for that report because it cannot verify which attempt determines the
final case result. Keep retries out of a scope that requires a counted pass until its report
format has an explicit, verified retry contract. Independently selected classes can be run as
separate scopes, or as the two declared reports in the wrapper above. Each declared file must be
produced by its intended selection.

Multi-module aggregation and other Surefire versions need their own producer proof and report
inventory. Each module needs declared report paths and a completion rule before it can be treated
as one test scope. The [Surefire plugin reference](https://maven.apache.org/surefire/maven-surefire-plugin/)
documents its report files and [test goal options](https://maven.apache.org/surefire/maven-surefire-plugin/test-mojo.html),
including `reportsDirectory` and no-test selection behavior.
