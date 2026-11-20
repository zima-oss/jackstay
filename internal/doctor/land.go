package doctor

import (
	"context"
	"fmt"
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
)

// LandProject is what the land checks read of the project around the current folder.
type LandProject struct {
	// Config is the project's jackstay.json, which has a `land` section.
	Config config.Config
	// Workflows are the checkout's workflow files (.github/workflows/*.yml and *.yaml), by name.
	Workflows map[string]string
	// Connect is the project's host and its repository (owner/name), as `jst land` connects.
	Connect func(ctx context.Context) (hosting.Host, string, error)
}

// landTimeout bounds the land checks' questions to the host.
const landTimeout = 30 * time.Second

// realLandProject is the project around the current folder when its jackstay.json has a `land`
// section; a file that cannot be read is the adoption check's to report.
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
	if _, ok := parsed["land"]; !ok {
		return LandProject{}, false
	}
	return LandProject{Config: parsed, Workflows: readWorkflows(root), Connect: func(ctx context.Context) (hosting.Host, string, error) {
		repository, err := remote.RepositoryOf(ctx, gitcli.Git{Dir: root}, parsed.Section("github"))
		if err != nil {
			return nil, "", fmt.Errorf("which repository this is cannot be told: %w", err)
		}
		settings, _ := parsed.GitHub()
		host, _, err := gh.Connect(ctx, settings.Account, repository.String())
		return host, repository.String(), err
	}}, true
}

// checkLand reports a project's landing policy (docs/design/land.md section 5): the section valid,
// the account connecting, the base's rules readable and requiring checks, the method allowed, a
// required queue, and the guard refusing `gh pr merge`.
func checkLand(report *Report, inputs Inputs) {
	if inputs.Land == nil {
		return
	}
	project, ok := inputs.Land()
	if !ok {
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
	checkLandRules(report, facts, project, land, base, rules)
}

// checkLandRules judges the base's rules against the land section (section 4).
func checkLandRules(report *Report, facts map[string]any, project LandProject, land config.Land, base string, rules hosting.Rules) {
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
	checkLintRequired(report, base, rules, project.Workflows)
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

// checkLintRequired reports whether the base's rules require the check of the job that runs
// `jst lint pr`, which keeps a change of jackstay.json alone in its pull request (rule D0, section
// 3.3). The job is found in the workflows, not assumed: none is a problem; one whose check the
// readable rules do not require is a problem; several, one whose check's name cannot be told, or
// rules that cannot be read leave it unverified (a note).
func checkLintRequired(report *Report, base string, rules hosting.Rules, workflows map[string]string) {
	jobs := lintJobs(workflows)
	var named []string
	for _, job := range jobs {
		named = append(named, job.String())
	}
	switch {
	case len(jobs) == 0:
		report.problem("land-lint-missing", "no workflow in .github/workflows runs `jst lint pr`: rule D0 is not enforced, "+
			"and a change of jackstay.json can land with other files",
			"`jst adopt --apply` writes card-line.yml, which runs it; then require its check in the branch's rules")
	case !rules.ChecksRead:
		report.note("land-lint-unverified", "whether the rules of %s require the check of %s, which runs `jst lint pr`, cannot be read",
			base, strings.Join(named, ", "))
	case len(jobs) > 1:
		report.note("land-lint-unverified", "more than one job runs `jst lint pr` (%s): which check the rules of %s must require is not told",
			strings.Join(named, ", "), base)
	case jobs[0].Unknown != "":
		report.note("land-lint-unverified", "%s runs `jst lint pr`, and its check's name cannot be told (%s): whether the rules of %s require it is unverified",
			jobs[0], jobs[0].Unknown, base)
	case !slices.ContainsFunc(rules.Required, func(required hosting.RequiredCheck) bool { return required.Name == jobs[0].Check }):
		report.problem("land-lint-not-required", fmt.Sprintf("the rules of %s do not require `%s`, the check of %s, which runs `jst lint pr`: "+
			"a change of jackstay.json can land with other files, past rule D0", base, jobs[0].Check, jobs[0]),
			"require the `"+jobs[0].Check+"` check in the branch's rules on the host")
	}
}

// readWorkflows are the workflow files of the checkout at root, by name; none when there are none.
func readWorkflows(root string) map[string]string {
	folder := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	found := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		if data, err := os.ReadFile(filepath.Join(folder, name)); err == nil { // #nosec G304 -- the project's own workflow
			found[name] = string(data)
		}
	}
	return found
}
