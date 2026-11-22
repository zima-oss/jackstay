package doctor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gh"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/guard"
	"github.com/zima-oss/jackstay/internal/hosting"
	"github.com/zima-oss/jackstay/internal/remote"
	scopes "github.com/zima-oss/jackstay/internal/testing"
)

// LandProject is what the land checks read of the project around the current folder.
type LandProject struct {
	// Config is the project's jackstay.json, which has a `land` section or a scope whose `ci`
	// names a system.
	Config config.Config
	// LintMissing says the checkout has no .github/workflows/card-line.yml, adopt's workflow that
	// runs `jst lint pr`.
	LintMissing bool
	// Connect is the project's host and its repository (owner/name), as `jst land` connects.
	Connect func(ctx context.Context) (hosting.Host, string, error)
}

// lintCheck is the check of adopt's card-line workflow, whose job runs `jst lint pr`.
const lintCheck = "card-line"

// landTimeout bounds the land checks' questions to the host.
const landTimeout = 30 * time.Second

// realLandProject is the project around the current folder when its jackstay.json has a `land`
// section or a scope whose `ci` names a system; a file that cannot be read is the adoption check's
// to report.
func realLandProject() (LandProject, bool) {
	here, err := os.Getwd()
	if err != nil {
		return LandProject{}, false
	}
	root, _ := config.Find(here)
	if root == "" {
		return LandProject{}, false
	}
	data, err := os.ReadFile(filepath.Join(root, config.Name)) // #nosec G304 -- the project's jackstay.json
	if err != nil {
		return LandProject{}, false
	}
	parsed, err := config.Parse(data)
	if err != nil {
		return LandProject{}, false
	}
	if _, ok := parsed["land"]; !ok && len(ciJobs(parsed)) == 0 {
		return LandProject{}, false
	}
	project := LandProject{Config: parsed, Connect: func(ctx context.Context) (hosting.Host, string, error) {
		repository, err := remote.RepositoryOf(ctx, gitcli.Git{Dir: root}, parsed.Section("github"))
		if err != nil {
			return nil, "", fmt.Errorf("which repository this is cannot be told: %w", err)
		}
		settings, _ := parsed.GitHub()
		host, _, err := gh.Connect(ctx, settings.Account, repository.String())
		return host, repository.String(), err
	}}
	_, err = os.Stat(filepath.Join(root, ".github", "workflows", "card-line.yml"))
	project.LintMissing = errors.Is(err, os.ErrNotExist)
	return project, true
}

// checkLand reports a project's landing policy (docs/design/land.md section 5): the section valid,
// the account connecting, the base's rules readable and requiring checks, the method allowed, a
// required queue, and the guard refusing `gh pr merge`. A project with no `land` section gets only
// the check of its CI jobs.
func checkLand(report *Report, inputs Inputs) {
	if inputs.Land == nil {
		return
	}
	project, ok := inputs.Land()
	if !ok {
		return
	}
	if _, hasLand := project.Config["land"]; !hasLand {
		checkCIJobsWithoutLand(report, project)
		return
	}
	facts := map[string]any{}
	report.Facts["land"] = facts
	problems := config.SectionProblems(project.Config)
	if found := append(problems.Of(""), problems.Of("land")...); len(found) > 0 {
		report.problem("land-invalid", "jackstay.json's `land` section cannot be used: "+strings.Join(found, "; "),
			"fix the section (`jst check` names each problem), in a pull request a person merges")
		return
	}
	land, _ := project.Config.Land()
	facts["autonomy"], facts["method"] = land.Autonomy, land.Method
	checkLandGuard(report, project.Config)
	// D0 is never reported as enforced: only what is definitely wrong, else a standing note.
	lintWrong := project.LintMissing
	if lintWrong {
		report.problem("land-lint-missing", "there is no .github/workflows/card-line.yml, which runs `jst lint pr`: rule D0 is not enforced, "+
			"and a change of jackstay.json can land with other files",
			"`jst adopt --apply` writes it; then require its `"+lintCheck+"` check in the branch's rules")
	}
	defer func() {
		if !lintWrong {
			report.note("land-lint-confirm", "D0 (jackstay.json changed alone, `jst lint pr`) is enforced only if a required check runs "+
				"`jst lint pr` on the base and fails with it. jackstay does not verify that: confirm it once in the host's branch rules. "+
				"Pull requests from forks skip the adopted job.")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), landTimeout)
	defer cancel()
	host, repository, err := project.Connect(ctx)
	if err != nil {
		report.problem("land-account", "the host cannot be asked as the project's account: "+err.Error(),
			"sign the account in (`gh auth login`), or set `github.account` and `github.repository` in jackstay.json")
		return
	}
	facts["repository"] = repository
	base, err := host.DefaultBranch(ctx)
	if err != nil {
		report.problem("land-account", fmt.Sprintf("the default branch of %s cannot be read: %v", repository, err),
			"give the account read access to "+repository+", then `jst doctor` again")
		return
	}
	facts["base"] = base
	rules, err := host.Rules(ctx, base)
	if err != nil {
		report.problem("land-rules", fmt.Sprintf("the rules of %s cannot be read: %v", base, err),
			"run `jst doctor` again; if it stays, `jst land --plan` shows what the host answers")
		return
	}
	lintWrong = checkLandRules(report, facts, land, base, rules) || lintWrong
	checkCIJobs(report, ciJobs(project.Config), base, rules)
}

// checkCIJobsWithoutLand reports, for a project with scopes CI covers and no `land` section, a
// system whose CI job the base's rules do not require. A host that cannot be asked is a note: the
// project declared no landing whose account doctor should insist on.
func checkCIJobsWithoutLand(report *Report, project LandProject) {
	jobs := ciJobs(project.Config)
	if len(jobs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), landTimeout)
	defer cancel()
	var base string
	var rules hosting.Rules
	host, _, err := project.Connect(ctx)
	if err == nil {
		base, err = host.DefaultBranch(ctx)
	}
	if err == nil {
		rules, err = host.Rules(ctx, base)
	}
	if err != nil {
		report.note("ci-jobs-unchecked", "whether the base's rules require %s cannot be read: %v", strings.Join(jobs, ", "), err)
		return
	}
	checkCIJobs(report, jobs, base, rules)
}

// ciJobs are the CI jobs of the scopes CI covers (docs/design/land.md section 7.4): one
// `jst test ci (SYSTEM)` for each system some scope's `ci` names, in order.
func ciJobs(project config.Config) []string {
	entries, _ := project.Section("test")["scopes"].(map[string]any)
	systems := map[string]bool{}
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		listed, _ := entry["ci"].([]any)
		for _, system := range listed {
			if text, ok := system.(string); ok {
				systems[text] = true
			}
		}
	}
	var jobs []string
	for _, system := range slices.Sorted(maps.Keys(systems)) {
		jobs = append(jobs, scopes.CIJobName(system))
	}
	return jobs
}

// checkCIJobs reports a system with covered scopes whose job the readable rules of the base do
// not require: CI is the gate for those scopes only when the rules hold a change until it passes.
func checkCIJobs(report *Report, jobs []string, base string, rules hosting.Rules) {
	if !rules.ChecksRead {
		return
	}
	for _, job := range jobs {
		if !slices.ContainsFunc(rules.Required, func(required hosting.RequiredCheck) bool { return required.Name == job }) {
			report.problem("land-ci-job-not-required", fmt.Sprintf("the rules of %s do not require the CI job `%s`, which runs the scopes CI covers "+
				"there: a change can merge without them", base, job),
				"name the CI job `"+job+"` (it runs `jst test ci`) and require it in the branch's rules on the host")
		}
	}
}

// checkLandRules judges the base's rules against the land section (section 4); lintWrong says the
// readable rules require no check named `card-line`, adopt's check that runs `jst lint pr`.
func checkLandRules(report *Report, facts map[string]any, land config.Land, base string, rules hosting.Rules) (lintWrong bool) {
	switch {
	case !rules.ChecksRead && len(land.Checks) == 0:
		report.problem("land-rules-unread", fmt.Sprintf("the rules of %s cannot be read (%s) and land.checks lists none: "+
			"`jst land` cannot tell which checks gate a merge and hands every change to a person", base, rules.Unread),
			"give the account permission to read the branch's rules, or list the required checks in land.checks")
	case !rules.ChecksRead:
		report.note("land-rules-unread", "the rules of %s cannot be read (%s): `jst land` requires land.checks (%s) and reports the others",
			base, rules.Unread, strings.Join(land.Checks, ", "))
	case len(rules.Required) == 0 && len(land.Checks) == 0:
		report.problem("land-no-required-checks", fmt.Sprintf("the rules of %s require no check and land.checks lists none: "+
			"CI is not a gate, and `jst land` hands every change to a person", base),
			"require CI's checks in the branch's rules on the host (or list them in land.checks)")
	case len(rules.Required) == 0:
		report.note("land-no-required-checks", "the rules of %s require no check: `jst land` requires land.checks (%s)",
			base, strings.Join(land.Checks, ", "))
	default:
		var names []string
		for _, required := range rules.Required {
			names = append(names, required.String())
		}
		facts["required_checks"] = names
	}
	if rules.ChecksRead && !slices.ContainsFunc(rules.Required, func(required hosting.RequiredCheck) bool { return required.Name == lintCheck }) {
		lintWrong = true
		report.problem("land-lint-not-required", fmt.Sprintf("the rules of %s require no check named `%s`, the adopted job that runs "+
			"`jst lint pr`: a change of jackstay.json can land with other files, past rule D0", base, lintCheck),
			"require the `"+lintCheck+"` check in the branch's rules on the host")
	}
	switch {
	case !rules.MethodsRead:
		report.note("land-method", "the merge methods the repository allows cannot be read: whether `%s` is allowed is known at the merge", land.Method)
	case !slices.Contains(rules.Methods, hosting.Method(land.Method)):
		var allowed []string
		for _, method := range rules.Methods {
			allowed = append(allowed, string(method))
		}
		report.problem("land-method", fmt.Sprintf("land.method is %s, and the repository allows %s", land.Method, strings.Join(allowed, ", ")),
			"set land.method to an allowed method, or allow "+land.Method+" in the repository's settings")
	}
	if rules.QueueRead && rules.Queue {
		report.note("land-queue", "the rules of %s require the merge queue: the agent hands every change to a person, who queues it", base)
	}
	return lintWrong
}

// checkLandGuard reports a guard that lets `gh pr merge` run (section 3.3).
func checkLandGuard(report *Report, project config.Config) {
	switch guard.Level("gh-pr-merge", project.Section("guard")) {
	case config.LevelOff:
		report.problem("land-guard", "guard.rules.gh-pr-merge is off: an agent can merge with `gh pr merge` past the landing policy",
			"remove the `gh-pr-merge` entry from guard.rules in jackstay.json, in a pull request a person merges")
	case config.LevelWarn:
		report.note("land-guard", "guard.rules.gh-pr-merge only warns: `gh pr merge` runs past the landing policy")
	}
}
