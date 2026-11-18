package guard

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/cards"
)

var protectedBranches = []string{"main", "master"}

var pushSettings = regexp.MustCompile(`(?i)^(?:remote|push|branch|url|include|includeif)\.`)

func (job *gitJob) pushForce(found report) {
	opts := job.opts
	plus := false
	for _, one := range opts.operands {
		if text, ok := one.single(); ok && strings.HasPrefix(text, "+") && len(text) > 1 {
			plus = true
		}
	}
	if opts.on("--force") || opts.on("--mirror") || plus || opts.unknown {
		found("", "")
	}
}

func (job *gitJob) pushPrune(found report) {
	if job.opts.on("--prune") && !job.opts.on("--dry-run") {
		found("", "")
	}
}

func (job *gitJob) pushDeletesProtected(found report) {
	opts := job.opts
	var refs []string
	for index, one := range opts.operands {
		if index > 0 {
			refs = append(refs, one.value())
		}
	}
	var gone []string
	for _, ref := range refs {
		name := ref
		if !opts.on("--delete") {
			if !strings.HasPrefix(ref, ":") {
				continue
			}
			name = ref[1:]
		}
		if slices.Contains(protectedBranches, strings.TrimPrefix(name, "refs/heads/")) {
			gone = append(gone, name)
		}
	}
	if len(gone) > 0 {
		found("it deletes the branch "+strings.Join(gone, ", ")+" on the remote, for everyone", "")
	}
}

// cardPushRefused and cardPushUnreadable are the two rows of the Card push (docs/design/implementation.md section 4.1): the
// project's section broken is the second, everything else the first.
func (job *gitJob) cardPushRefused(found report) {
	job.cardPush(func(row, why, instead string) {
		if row == "cards-push" {
			found(why, instead)
		}
	})
}

func (job *gitJob) cardPushUnreadable(found report) {
	job.cardPush(func(row, why, instead string) {
		if row == "card-push-unreadable-config" {
			found(why, instead)
		}
	})
}

// cardsAt is the `cards` section of the project a folder is in ("" folder: the hook's project).
func (reader *reader) cardsAt(folder string) (map[string]any, bool) {
	if folder == "" || reader.settings.CardsAt == nil {
		return reader.settings.Cards, reader.settings.CardsBroken
	}
	return reader.settings.CardsAt(folder)
}

// cardPush: in a project with a `cards` section, a push that would move `cards.branch` is refused
// unless every commit it adds has valid `Card:` lines (docs/design/implementation.md section 4.1).
func (job *gitJob) cardPush(found func(row, why, instead string)) {
	if job.call.elsewhere || job.redirected {
		section, broken := job.reader.cardsAt("")
		if section != nil || broken {
			found("cards-push", "the repository is named by --git-dir, --work-tree or GIT_DIR, so the "+
				"guard cannot tell which project's Card rules this push must meet", "push from the checkout itself (`git -C <checkout> push …`)")
		}
		return
	}
	for _, where := range job.wheres {
		section, broken := job.reader.cardsAt(where)
		if section == nil && !broken {
			continue
		}
		job.cardPushIn(where, section, broken, found)
	}
}

func (job *gitJob) cardPushIn(where string, section map[string]any, broken bool, found func(row, why, instead string)) {
	var given []string
	for _, one := range job.call.rest {
		if text, ok := one.single(); ok && strings.HasPrefix(text, "-") {
			given = append(given, text)
		}
	}
	onlyThese := func(allowed ...string) bool {
		if len(given) == 0 {
			return false
		}
		for _, text := range given {
			if !slices.Contains(allowed, text) {
				return false
			}
		}
		return true
	}
	if onlyThese("--dry-run", "-n") || onlyThese("--delete", "-d") {
		return
	}
	if broken {
		found("card-push-unreadable-config", "", "")
		return
	}
	target := "main"
	if branch, ok := section["branch"].(string); ok && branch != "" {
		target = branch
	}
	overridden := false
	for _, entry := range job.call.configs {
		if entry.fromVar || pushSettings.MatchString(entry.key) {
			overridden = true
		}
	}
	for _, name := range []string{"GIT_CONFIG", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM"} {
		if _, ok := job.env[name]; ok {
			overridden = true
		}
	}
	for _, name := range job.opts.longs {
		if len(name) >= 5 && strings.HasPrefix("--repo", name) {
			overridden = true
		}
	}
	if overridden {
		found("cards-push", "where this push goes is set on the command itself (`-c`, `--config-env`, "+
			"`--repo`, or a GIT_CONFIG variable), which the guard cannot read, so it cannot tell whether it moves "+target,
			"push without it, naming the remote and the branch (`git push origin "+target+"`)")
		return
	}
	for _, one := range job.call.rest {
		if one.dynamic || !one.known() {
			found("cards-push", "the guard cannot tell whether this push moves "+target+
				" (part of it is only known when it runs)", "write the push out plainly (`git push origin "+target+
				"`); `jst lint range origin/"+target+"..HEAD` checks the Card lines first")
			return
		}
	}
	positionals := job.opts.operands
	remote := ""
	if len(positionals) > 0 {
		remote = positionals[0].value()
	}
	type pair struct{ source, destination string }
	var pairs []pair
	if job.opts.any("--all", "--mirror", "--branches") {
		pairs = append(pairs, pair{"refs/heads/" + target, target})
	}
	for _, one := range positionals[min(1, len(positionals)):] {
		spec := strings.TrimPrefix(one.value(), "+")
		if spec == ":" {
			pairs = append(pairs, pair{"refs/heads/" + target, target})
			continue
		}
		source, destination, hasColon := strings.Cut(spec, ":")
		if hasColon && source == "" {
			continue // a deletion
		}
		if !hasColon {
			destination = source
		}
		pairs = append(pairs, pair{source, destination})
	}
	tagsOnly := job.opts.on("--tags")
	if len(positionals) <= 1 && len(pairs) == 0 && !tagsOnly {
		pairs = append(pairs, pair{"HEAD", ""})
	}
	for _, each := range pairs {
		problems, ok := []string(nil), false
		if where != "" {
			problems, ok = job.reader.questions.cardsPush(where, CardPush{Remote: remote, Source: each.source,
				Target: each.destination, Branch: target, Section: section})
		}
		switch {
		case !ok:
			found("cards-push", "the guard cannot tell what this push would add to "+target+
				", so it cannot check the Card lines", "`jst lint range <remote>/"+target+"..<what you push>` shows what is wrong; "+
				"push from the checkout itself, to a remote by name")
		case len(problems) > 0:
			found("cards-push", "it adds commits to "+target+" without valid `Card:` lines: "+
				truncate(strings.Join(problems[:min(5, len(problems))], " | "), 600), "")
		}
	}
}

// CardRange answers the Card push question by asking git: where the push goes, the commits it adds
// to the cards branch, and their `Card:` lines against the card files of the pushed tree.
func CardRange(folder string, push CardPush, ask func(args ...string) (string, bool)) ([]string, bool) {
	text := func(args ...string) string {
		out, _ := ask(args...)
		return strings.TrimSpace(out)
	}
	root := text("rev-parse", "--show-toplevel")
	if root == "" {
		return nil, false
	}
	current := text("symbolic-ref", "--quiet", "--short", "HEAD")
	setting := func(key string) string { return text("config", "--get", key) }
	remote, source, target := push.Remote, push.Source, push.Target
	if remote == "" {
		remote = firstOf(func() string {
			if current != "" {
				return setting("branch." + current + ".pushRemote")
			}
			return ""
		}(), setting("remote.pushDefault"), func() string {
			if current != "" {
				return setting("branch." + current + ".remote")
			}
			return ""
		}(), "origin")
	}
	if slices.Contains([]string{"true", "yes", "on", "1"}, strings.ToLower(setting("remote."+remote+".mirror"))) {
		return nil, false
	}
	switch target {
	case "":
		if setting("remote."+remote+".push") != "" {
			return nil, false
		}
		mode := firstOf(setting("push.default"), "simple")
		switch {
		case mode == "nothing":
			return nil, true
		case mode == "matching":
			source, target = "refs/heads/"+push.Branch, push.Branch
		case current == "":
			return nil, true
		case mode == "current" || mode == "simple":
			target = current
		case mode == "upstream" || mode == "tracking":
			upstream := setting("branch." + current + ".merge")
			if remote != firstOf(setting("branch."+current+".remote"), remote) {
				target = current
			} else {
				target = firstOf(strings.TrimPrefix(upstream, "refs/heads/"), current)
			}
		default:
			return nil, false
		}
	case "HEAD":
		if current == "" {
			return nil, false
		}
		target = current
	}
	if strings.Contains(target, "*") {
		pattern := strings.TrimPrefix(strings.TrimPrefix(target, "refs/"), "heads/")
		if matched, _ := path.Match(pattern, push.Branch); !matched {
			return nil, true
		}
		star := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, "(.*)") + "$").FindStringSubmatch(push.Branch)
		filling := ""
		if star != nil {
			filling = star[1]
		}
		source = strings.Replace(source, "*", filling, 1)
		target = push.Branch
	}
	target = strings.TrimPrefix(strings.TrimPrefix(target, "refs/"), "heads/")
	if target != push.Branch {
		return nil, true
	}
	if !slices.Contains(strings.Fields(text("remote")), remote) {
		return nil, false
	}
	tip := text("rev-parse", "--verify", "--quiet", source+"^{commit}")
	if tip == "" {
		return nil, false
	}
	return cardProblems(root, push, tip, "refs/remotes/"+remote+"/"+target, ask)
}

func firstOf(texts ...string) string {
	for _, text := range texts {
		if text != "" {
			return text
		}
	}
	return ""
}

// cardProblems are the commits from the remote's copy of the branch to the tip whose `Card:` lines
// are wrong, judged against the card files of the tip's tree; commits before `cards.since` pass.
func cardProblems(root string, push CardPush, tip, base string, ask func(args ...string) (string, bool)) ([]string, bool) {
	rules, err := cards.FromSection(push.Section)
	if err != nil {
		return nil, false
	}
	listing, ok := ask("ls-tree", "-r", "--name-only", "-z", tip)
	if !ok {
		return nil, false
	}
	var names []string
	folderThere := false
	for _, name := range strings.Split(listing, "\x00") {
		if strings.HasPrefix(name, rules.Dir+"/") {
			folderThere = true
		}
		if name != "" {
			names = append(names, name)
		}
	}
	if !folderThere {
		return nil, false
	}
	known := rules.Known(names)
	revisions := []string{tip}
	if _, ok := ask("rev-parse", "--verify", "--quiet", base+"^{commit}"); ok {
		revisions = append(revisions, "--not", base)
	}
	log, ok := ask(append([]string{"log", "-z", "--format=%H%x1f%cI%x1f%s%x1f%B"}, revisions...)...)
	if !ok {
		return nil, false
	}
	since, _ := push.Section["since"].(string)
	var found []string
	for _, record := range strings.Split(log, "\x00") {
		record = strings.TrimLeft(record, "\n")
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\x1f", 4)
		if len(parts) != 4 {
			return nil, false
		}
		if since != "" && (len(parts[1]) < 10 || parts[1][:10] < since) {
			continue
		}
		problems := rules.Check(parts[3], known)
		if len(problems) == 0 {
			continue
		}
		var said []string
		for _, problem := range problems {
			said = append(said, problem.Rule)
		}
		found = append(found, fmt.Sprintf("%.10s %.80s: %s", parts[0], parts[2], strings.Join(said, "; ")))
	}
	_ = root
	return found, true
}
