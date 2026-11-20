// Package adopt is `jst adopt` (docs/design/adopt.md; W-058, W-059, W-060): it computes what the
// four places jackstay owns in a project must hold — jackstay.json, the AGENTS block, the guard hook, the
// workflows it writes — from the project's jackstay.json, this jackstay and an optional profile, and either
// prints the difference and the conflicts it must not decide (--plan) or writes the difference
// when no conflict is left (--apply). Every write is computed from the files' state, never from a
// record of an earlier run, so a second --apply writes nothing.
package adopt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"

	"github.com/zima-oss/jackstay/internal/adopt/block"
	"github.com/zima-oss/jackstay/internal/adopt/jsondoc"
	"github.com/zima-oss/jackstay/internal/adopt/profile"
	"github.com/zima-oss/jackstay/internal/adopt/templates"
	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/check"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/install"
	"github.com/zima-oss/jackstay/internal/layout"
	"github.com/zima-oss/jackstay/internal/release"
)

// Options are what a plan is made from besides the project's files.
type Options struct {
	// Dir is any folder of the project.
	Dir string
	// Profile is --profile as given ("": the one jackstay.json names); WorkingDir resolves it when relative.
	Profile       string
	ProfileSHA256 string
	WorkingDir    string
	// Accept are the conflicts --accept lets jackstay overwrite.
	Accept []string
	// Pin moves adopt.pin to this jackstay's version.
	Pin bool
	// SettingsOnly keeps the plan to jackstay.json: its changes and its conflicts (rule D0 lands a
	// change of jackstay.json in a pull request of its own; the other files follow it).
	SettingsOnly bool
	// Has says a command of that name is in this jackstay; Version is this jackstay's version.
	Has     func(command string) bool
	Version string
	// Home is the user's home, for a profile path written `~/…`.
	Home string
	// GuardCommand is the hook command a new guard hook gets; "" is GuardCommand(), the launcher of
	// the install this jst runs from.
	GuardCommand string
	// landNow is Make's own: the upgrade's second step is done, so this plan is its third.
	landNow bool
	// Autonomy is --autonomy: the `land.autonomy` a new `land` section gets ("": not given, so a
	// person is asked before --apply writes one; the plan shows Offered).
	Autonomy string
	// Offered is the answer offered first when nobody chose (the machine's, from `jst setup`;
	// "": merge-when-green, the recommended one).
	Offered string
}

// SplitsSettings says whether the plan's changes touch jackstay.json and another file: one pull
// request of them all is refused by rule D0 (settings alone).
func (plan *Plan) SplitsSettings() bool {
	settings, files := false, false
	for _, change := range plan.Changes {
		if change.Path == config.Name {
			settings = true
		} else {
			files = true
		}
	}
	return settings && files
}

// fileConflict identifies conflicts that belong to the files-only adoption stage.
func fileConflict(id string) bool {
	for _, prefix := range []string{"agents:", "file:", "hook:", "workflow:"} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

// Change is one write the plan makes.
type Change struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	What string `json:"what"`
}

// Conflict is a difference jst adopt must not decide (spec section 5).
type Conflict struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	What   string `json:"what"`
	Settle string `json:"settle"`
	// Lost is the text an --accept would replace.
	Lost string `json:"lost,omitempty"`

	acceptable bool
	exceptable bool
	// replacement is what --accept writes.
	replacement *write
	acceptWhat  string
}

// Exception is a conflict the project keeps on purpose (adopt.exceptions).
type Exception struct {
	ID  string `json:"id"`
	Why string `json:"why"`
}

// Plan is what jst adopt found.
type Plan struct {
	Project       string      `json:"project"`
	Root          string      `json:"root"`
	Profile       string      `json:"profile"`
	ProfileName   string      `json:"profile_name,omitempty"`
	ProfileSHA256 string      `json:"profile_sha256,omitempty"`
	Changes       []Change    `json:"changes"`
	Conflicts     []Conflict  `json:"conflicts"`
	Exceptions    []Exception `json:"exceptions"`
	Notes         []string    `json:"notes"`

	writes  []write
	inputs  map[string]fileInput
	profile *profile.Profile
	// asksAutonomy says the plan writes a new `land` section whose autonomy nobody chose.
	asksAutonomy bool
}

type fileInput struct {
	text   string
	exists bool
}

type write struct {
	path   string // relative to the root
	data   []byte
	remove bool
}

// UsageError is a mistake in what was asked (exit 2).
type UsageError struct{ Message string }

func (err *UsageError) Error() string { return err.Message }

// ErrConflicts says --apply wrote nothing because conflicts are left.
var ErrConflicts = errors.New("conflicts are left: nothing written")

const (
	agentsFile   = "AGENTS.md"
	claudeFile   = "CLAUDE.md"
	settingsFile = ".claude/settings.json"
	workflowsDir = ".github/workflows"
)

type planner struct {
	options Options
	root    string
	// base is the project's folder in the main checkout (root itself outside a linked worktree):
	// what a relative recorded path is relative to, so it means one file in every worktree.
	base     string
	plan     *Plan
	document *jsondoc.Object // the prospective jackstay.json
	plain    config.Config
	// existed says the project had a jackstay.json before this plan.
	existed bool
	// profileLand is the profile's `land` defaults for a project without a section (nil: none).
	profileLand *jsondoc.Object
	// landComing says the workflows are those of a project with a `land` section, which the
	// upgrade's third step writes.
	landComing bool
	// upgrading says the plan is the upgrade's second or third step, which Make tells apart.
	upgrading bool
	// retained are the workflows the project keeps: its own, and those kept by an exception.
	retained []string
}

// newPlanner is the planner of the project at root: the one place base is set, so every path
// that reads a recorded profile (Make, BlockOf) reads it from the main checkout.
func newPlanner(options Options, root string) *planner {
	return &planner{options: options, root: root, base: mainFolder(root),
		plan: &Plan{Root: root, Changes: []Change{}, Conflicts: []Conflict{}, Exceptions: []Exception{}, Notes: []string{}, inputs: map[string]fileInput{}}}
}

// Make computes the plan; an error is a usage or reading problem (exit 2). For a project upgrading
// from 0.5.x past the pin (docs/design/land.md section 5), the whole plan, its settlements and
// exceptions applied, says whether the workflows step is done; only then is the plan the third
// step, schema 3 with the `land` section.
func Make(options Options) (*Plan, error) {
	plan, planner, err := makePlan(options)
	if err != nil || !planner.upgrading {
		return plan, err
	}
	judged := planner
	if options.SettingsOnly {
		whole := options
		whole.SettingsOnly = false
		if _, judged, err = makePlan(whole); err != nil {
			plan.Notes = append(plan.Notes, "blocker: step 3 is not offered: the workflows cannot be planned: "+err.Error())
			planner.upgradeNotes(2)
			sort.Strings(plan.Notes)
			return plan, nil
		}
	}
	pending, blockers := judged.stepTwoLeft()
	if !pending && len(blockers) == 0 {
		third := options
		third.landNow = true
		plan, _, err = makePlan(third)
		return plan, err
	}
	planner.upgradeNotes(2)
	plan.Notes = append(plan.Notes, blockers...)
	sort.Strings(plan.Notes)
	return plan, nil
}

// makePlan is one plan and the planner that made it.
func makePlan(options Options) (*Plan, *planner, error) {
	root, err := projectRoot(options.Dir)
	if err != nil {
		return nil, nil, err
	}
	planner := newPlanner(options, root)
	original, existed, err := planner.readConfig()
	if err != nil {
		return nil, nil, err
	}
	planner.existed = existed
	exceptions := stringMap(original.Object("adopt"), "exceptions")
	chosen, err := planner.readProfile(original)
	if err != nil {
		return nil, nil, err
	}
	planner.plan.profile = chosen
	if err := planner.prospective(original, chosen); err != nil {
		return nil, nil, err
	}
	planner.configChange(original, existed)
	if chosen != nil {
		planner.profileRules(chosen)
	}
	if options.SettingsOnly {
		for _, id := range options.Accept {
			if fileConflict(id) {
				return nil, nil, &UsageError{fmt.Sprintf("--accept %s is for a managed file: give it to the apply after the settings merge", id)}
			}
		}
		for id := range exceptions {
			if fileConflict(id) {
				delete(exceptions, id)
			}
		}
	} else if err := planner.managedFiles(chosen); err != nil {
		return nil, nil, err
	}
	planner.except(exceptions)
	if err := planner.accept(options.Accept); err != nil {
		return nil, nil, err
	}
	planner.sort()
	return planner.plan, planner, nil
}

func (planner *planner) managedFiles(chosen *profile.Profile) error {
	rules, leaveOut := []string(nil), []string(nil)
	if chosen != nil {
		planner.profileFiles(chosen)
		rules, leaveOut = chosen.Rules, chosen.LeaveOut
	}
	body := block.Render(block.Inputs{Config: planner.plain, Has: planner.options.Has, Rules: rules, LeaveOut: leaveOut})
	if err := planner.agents(body); err != nil {
		return err
	}
	if err := planner.hook(); err != nil {
		return err
	}
	return planner.workflows(chosen)
}

// projectRoot is the folder of the nearest jackstay.json, else the git work tree's top.
func projectRoot(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", &UsageError{err.Error()}
	}
	if root, _ := config.Find(absolute); root != "" {
		return root, nil
	}
	output, err := exec.Command("git", "-C", absolute, "rev-parse", "--show-toplevel").Output() // #nosec G204 -- git with a folder the user names
	if err != nil {
		return "", &UsageError{fmt.Sprintf("%s is in no project: no %s at or above it and no git work tree", absolute, config.Name)}
	}
	top := strings.TrimSpace(string(output))
	if real, err := filepath.EvalSymlinks(top); err == nil {
		top = real
	}
	return top, nil
}

// readConfig is the project's jackstay.json as an ordered object (a new one when there is none).
func (planner *planner) readConfig() (*jsondoc.Object, bool, error) {
	path := filepath.Join(planner.root, config.Name)
	text, exists, err := planner.read(config.Name)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		fresh := jsondoc.NewObject()
		if install.Compare(planner.options.Version, config.Schema3Release) >= 0 {
			fresh.Set("$schema", config.EditorSchemaURL(planner.options.Version))
			fresh.Set("jackstay_schema", json.Number(fmt.Sprint(config.Schema)))
		} else {
			fresh.Set("jackstay_schema", json.Number("2"))
		}
		fresh.Set("project", filepath.Base(planner.root))
		planner.plan.Project = filepath.Base(planner.root)
		return fresh, false, nil
	}
	data := []byte(text)
	parsed, err := config.Parse(data)
	if err != nil {
		return nil, false, &UsageError{fmt.Sprintf("%s: %v", path, err)}
	}
	if problems := check.ConfigProblems(parsed); len(problems) > 0 {
		return nil, false, &UsageError{fmt.Sprintf("%s is not usable (jst check): %s", path, strings.Join(problems, "; "))}
	}
	value, err := jsondoc.Parse(data)
	if err != nil {
		return nil, false, &UsageError{fmt.Sprintf("%s: %v", path, err)}
	}
	object, _ := value.(*jsondoc.Object)
	planner.plan.Project, _ = parsed["project"].(string)
	if planner.plan.Project == "" {
		planner.plan.Project = filepath.Base(planner.root)
	}
	return object, true, nil
}

// readProfile loads --profile, else the profile jackstay.json names.
func (planner *planner) readProfile(original *jsondoc.Object) (*profile.Profile, error) {
	recorded := ""
	recordedDigest := ""
	if adoptSection := original.Object("adopt"); adoptSection != nil {
		recorded, _ = getString(adoptSection, "profile")
		recordedDigest, _ = getString(adoptSection, "profile_sha256")
	}
	given := planner.options.Profile
	digest := planner.options.ProfileSHA256
	if digest == "" {
		digest = recordedDigest
	}
	if given == "" && recorded == "" {
		if digest != "" {
			return nil, &UsageError{"profile SHA-256 requires a profile path"}
		}
		return nil, nil
	}
	var path string
	if given != "" {
		var err error
		path, recorded, err = planner.resolveGiven(given)
		if err != nil {
			return nil, err
		}
	} else {
		path = planner.resolveRecorded(recorded)
	}
	planner.plan.Profile = recorded
	planner.plan.ProfileSHA256 = digest
	var loaded *profile.Profile
	var err error
	if digest != "" {
		loaded, err = profile.LoadPinned(path, digest)
	} else {
		loaded, err = profile.Load(path)
	}
	if err != nil {
		return nil, &UsageError{"the profile: " + err.Error()}
	}
	planner.plan.ProfileName = loaded.Name
	return loaded, nil
}

// resolveGiven is --profile's file and the form jackstay.json records: `~/…` and absolute paths as
// given, anything else relative to the project's folder in the main checkout (planner.base).
func (planner *planner) resolveGiven(given string) (string, string, error) {
	switch {
	case strings.HasPrefix(given, "~/"):
		return filepath.Join(planner.options.Home, given[2:]), given, nil
	case filepath.IsAbs(given):
		return given, given, nil
	}
	working := planner.options.WorkingDir
	if real, err := filepath.EvalSymlinks(working); err == nil {
		working = real
	}
	absolute := filepath.Join(working, given)
	relative, err := filepath.Rel(planner.base, absolute)
	if err != nil {
		return "", "", &UsageError{err.Error()}
	}
	return absolute, filepath.ToSlash(relative), nil
}

func (planner *planner) resolveRecorded(recorded string) string {
	switch {
	case strings.HasPrefix(recorded, "~/"):
		return filepath.Join(planner.options.Home, recorded[2:])
	case filepath.IsAbs(recorded):
		return recorded
	}
	return filepath.Join(planner.base, filepath.FromSlash(recorded))
}

// mainFolder is the project folder's place in the repository's main checkout: a recorded relative
// path (adopt.profile) is read from there, so it means one file in the main checkout and in every
// linked worktree. Outside a repository it is the folder itself.
// gitTimeout bounds the questions to git about where the main checkout is.
const gitTimeout = 30 * time.Second

func mainFolder(root string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitcli.Git{Dir: root}.SameFolderInMain(ctx, root)
}

// prospective is the jackstay.json the apply would write (spec section 4): defaults merged, the
// profile and pin recorded, the schema moved; plain is the same for the checks and the block. Keys
// that need "jackstay_schema": 3, when this apply adds them, are refused while the project is pinned
// to a jackstay before 0.6.0, which would refuse the file; with them, the schema becomes 3.
func (planner *planner) prospective(original *jsondoc.Object, chosen *profile.Profile) error {
	document := jsondoc.Clone(original).(*jsondoc.Object)
	if chosen != nil {
		mergeDefaults(document, chosen.Defaults)
		if planner.options.Profile != "" {
			setAdopt(document, "profile", planner.plan.Profile)
		}
		if planner.options.ProfileSHA256 != "" {
			setAdopt(document, "profile_sha256", strings.ToLower(planner.plan.ProfileSHA256))
		}
	}
	// A profile's `land` defaults never make the first policy by themselves: landSection decides
	// it from the project's own file, and the profile fills only its other keys.
	if original.Object("land") == nil {
		planner.profileLand = document.Object("land")
		document = withoutKey(document, "land")
	}
	planner.document = document
	if err := planner.settle(original, chosen); err != nil {
		return err
	}
	changed, err := planner.landSection(original, chosen)
	if err != nil || !changed {
		return err
	}
	return planner.settle(original, chosen)
}

// withoutKey is the object without one key, the others in their order.
func withoutKey(object *jsondoc.Object, key string) *jsondoc.Object {
	kept := jsondoc.NewObject()
	for _, name := range object.Keys() {
		if name != key {
			value, _ := object.Get(name)
			kept.Set(name, value)
		}
	}
	return kept
}

// templatePlain is the file the wanted workflows are chosen by: with a `land` section while the
// upgrade's second step writes the workflows the coming policy needs.
func (planner *planner) templatePlain() map[string]any {
	if !planner.landComing {
		return planner.plain
	}
	return withLand(planner.plain)
}

// withLand is a copy of plain with a `land` section when it has none.
func withLand(plain map[string]any) map[string]any {
	if _, present := plain["land"]; present {
		return plain
	}
	copied := make(map[string]any, len(plain)+1)
	for key, value := range plain {
		copied[key] = value
	}
	copied["land"] = map[string]any{}
	return copied
}

// settle writes the pin a workflow needs and moves the schema the prospective file's keys need,
// then sets plain from the document.
func (planner *planner) settle(original *jsondoc.Object, chosen *profile.Profile) error {
	document := planner.document
	planner.plain = config.Config(jsondoc.ToPlain(document).(map[string]any))
	// --pin moves a pin the file has even when no workflow uses it now: a later one may.
	if pin, _ := getString(document.Object("adopt"), "pin"); planner.usesVersion(chosen) || pin != "" && planner.options.Pin {
		if pin == "" || planner.options.Pin {
			pin = "v" + planner.options.Version
		}
		setAdopt(document, "pin", pin)
	}
	if schema, _ := document.Get("jackstay_schema"); fmt.Sprint(schema) == "1" {
		for _, key := range config.Stage1Sections {
			if _, present := document.Get(key); present {
				document.Set("jackstay_schema", json.Number("2"))
				break
			}
		}
	}
	if newer := config.Schema3Keys(planner.plain); len(newer) > 0 {
		before := config.Schema3Keys(config.Config(jsondoc.ToPlain(original).(map[string]any)))
		var added []string
		for _, key := range newer {
			if !slices.Contains(before, key) {
				added = append(added, key)
			}
		}
		// W-090: a pinned project is read by its pinned jackstay too (its workflows download it).
		if pin, _ := getString(document.Object("adopt"), "pin"); len(added) > 0 && pin != "" &&
			install.Compare(strings.TrimPrefix(pin, "v"), config.Schema3Release) < 0 {
			return &UsageError{fmt.Sprintf("`%s` need jackstay %s or later, and this project is pinned to %s, which would refuse %s: "+
				"leave them out until the pin is %s or later (`jst adopt --apply --pin` with a newer jackstay)",
				strings.Join(added, "`, `"), config.Schema3Release, pin, config.Name, config.Schema3Release)}
		}
		if schema, _ := document.Get("jackstay_schema"); fmt.Sprint(schema) != fmt.Sprint(config.Schema) {
			document.Set("jackstay_schema", json.Number(fmt.Sprint(config.Schema)))
		}
	}
	planner.plain = config.Config(jsondoc.ToPlain(document).(map[string]any))
	return nil
}

// workflowValues are the values a managed workflow is rendered with, for the pin given.
func (planner *planner) workflowValues(chosen *profile.Profile, pin string) map[string]string {
	values := map[string]string{
		"jackstay_repository": templates.Repository,
		"release_base":        release.BaseOf(planner.plain),
	}
	if pin != "" {
		values["jackstay_version"] = pin
	}
	if chosen != nil {
		for name, value := range chosen.Values {
			values[name] = value
		}
	}
	return values
}

func (planner *planner) usesVersion(chosen *profile.Profile) bool {
	for _, template := range wantedTemplates(chosen, planner.templatePlain()) {
		if slices.Contains(templates.Placeholders(template.Text), "jackstay_version") {
			return true
		}
	}
	return false
}

// mergeDefaults adds what the project lacks: a missing key whole, inside an object only missing
// keys; a value the project has is never replaced. New keys go in lexical order.
func mergeDefaults(document *jsondoc.Object, defaults map[string]any) {
	keys := make([]string, 0, len(defaults))
	for key := range defaults {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		existing, present := document.Get(key)
		if !present {
			document.Set(key, jsondoc.FromPlain(defaults[key]))
			continue
		}
		inner, isObject := existing.(*jsondoc.Object)
		wanted, wantedObject := defaults[key].(map[string]any)
		if isObject && wantedObject {
			mergeDefaults(inner, wanted)
		}
	}
}

func setAdopt(document *jsondoc.Object, key, value string) {
	section := document.Object("adopt")
	if section == nil {
		section = jsondoc.NewObject()
		document.Set("adopt", section)
	}
	if current, _ := getString(section, key); current != value {
		section.Set(key, value)
	}
}

func getString(object *jsondoc.Object, key string) (string, bool) {
	if object == nil {
		return "", false
	}
	value, present := object.Get(key)
	text, _ := value.(string)
	return text, present
}

func stringMap(object *jsondoc.Object, key string) map[string]string {
	out := map[string]string{}
	if object == nil {
		return out
	}
	inner := object.Object(key)
	if inner == nil {
		return out
	}
	for _, name := range inner.Keys() {
		value, _ := inner.Get(name)
		out[name], _ = value.(string)
	}
	return out
}

// configChange judges the prospective jackstay.json and plans its write.
func (planner *planner) configChange(original *jsondoc.Object, existed bool) {
	if problems := check.ConfigProblems(planner.plain); len(problems) > 0 {
		planner.conflict(Conflict{ID: "jackstay.json:invalid", Path: config.Name,
			What:   "with what jst adopt adds, jackstay.json would not be usable (jst check): " + strings.Join(problems, "; "),
			Settle: "set the value in jackstay.json yourself (a value the project sets is never replaced); this cannot be excepted"})
		return
	}
	if existed && jsondoc.Equal(original, planner.document) {
		return
	}
	what := "new: " + strings.Join(planner.document.Keys(), ", ")
	if existed {
		what = strings.Join(differences(original, planner.document, ""), "; ")
	}
	planner.change(Change{ID: "jackstay.json", Path: config.Name, What: what}, write{path: config.Name, data: jsondoc.Format(planner.document)})
}

// differences say what changed between two objects, key by key.
func differences(before, after *jsondoc.Object, prefix string) []string {
	var found []string
	for _, key := range after.Keys() {
		newValue, _ := after.Get(key)
		oldValue, present := before.Get(key)
		where := prefix + key
		switch {
		case !present:
			found = append(found, "add "+where)
		case jsondoc.Equal(oldValue, newValue):
		default:
			oldObject, oldIsObject := oldValue.(*jsondoc.Object)
			newObject, newIsObject := newValue.(*jsondoc.Object)
			if oldIsObject && newIsObject {
				found = append(found, differences(oldObject, newObject, where+".")...)
			} else {
				found = append(found, fmt.Sprintf("%s %s -> %s", where, short(oldValue), short(newValue)))
			}
		}
	}
	return found
}

func short(value any) string {
	text := strings.TrimSpace(string(jsondoc.Format(value)))
	if len(text) > 40 {
		return text[:37] + "..."
	}
	return text
}

// profileFiles judges the profile's required and forbidden files.
func (planner *planner) profileFiles(chosen *profile.Profile) {
	for _, rule := range chosen.Required {
		if _, err := os.Lstat(filepath.Join(planner.root, filepath.FromSlash(rule.Path))); errors.Is(err, os.ErrNotExist) {
			planner.conflict(Conflict{ID: "file:" + rule.Path + ":missing", Path: rule.Path, exceptable: true,
				What:   "the profile requires " + rule.Path + ": " + rule.Why,
				Settle: "create it, or keep it out with an exception in jackstay.json (adopt.exceptions) and the reason"})
		}
	}
	for _, rule := range chosen.Forbidden {
		if _, err := os.Lstat(filepath.Join(planner.root, filepath.FromSlash(rule.Path))); err == nil {
			planner.conflict(Conflict{ID: "file:" + rule.Path + ":present", Path: rule.Path, exceptable: true,
				What:   "the profile forbids " + rule.Path + ": " + rule.Why,
				Settle: "instead: " + rule.Instead + " (a migration of the project's own), or an exception with the reason and its card"})
		}
	}
}

// profileRules judges the profile's requirements for scopes and settings.
func (planner *planner) profileRules(chosen *profile.Profile) {
	for _, key := range chosen.RequiredKeys {
		if !hasKey(planner.plain, key) {
			planner.conflict(Conflict{ID: "jackstay.json:" + key + ":missing", Path: config.Name, exceptable: true,
				What:   "the profile requires `" + key + "` in jackstay.json",
				Settle: "add it (only the project knows its value), or an exception with the reason"})
		}
	}
	test := planner.plain.Section("test")
	scopes, _ := test["scopes"].(map[string]any)
	for _, name := range chosen.RequiredScopes {
		if _, declared := scopes[name]; !declared {
			planner.conflict(Conflict{ID: "scope:" + name + ":missing", Path: config.Name, exceptable: true,
				What:   "the profile requires the test scope `" + name + "`",
				Settle: "declare it in jackstay.json (test.scopes), or an exception with the reason"})
		}
	}
	if len(chosen.RunWith) > 0 {
		for _, name := range sortedKeys(scopes) {
			for _, program := range programs(test, name, nil) {
				if !slices.Contains(chosen.RunWith, program) {
					planner.conflict(Conflict{ID: "scope:" + name + ":runner", Path: config.Name, exceptable: true,
						What:   fmt.Sprintf("the scope `%s` runs %s; the profile runs scopes with %s", name, program, strings.Join(chosen.RunWith, ", ")),
						Settle: "run it through one of those, or an exception with the reason"})
					break
				}
			}
		}
	}
}

// programs are the first words of what a scope runs, through its steps.
func programs(test map[string]any, name string, seen []string) []string {
	if slices.Contains(seen, name) {
		return nil
	}
	seen = append(seen, name)
	scopes, _ := test["scopes"].(map[string]any)
	steps, _ := test["steps"].(map[string]any)
	entry, _ := steps[name].(map[string]any)
	if entry == nil {
		entry, _ = scopes[name].(map[string]any)
	}
	if words, ok := entry["run"].([]any); ok && len(words) > 0 {
		first, _ := words[0].(string)
		return []string{first}
	}
	var found []string
	listed, _ := entry["steps"].([]any)
	for _, item := range listed {
		stepName, _ := item.(string)
		found = append(found, programs(test, stepName, seen)...)
	}
	return found
}

func hasKey(plain map[string]any, dotted string) bool {
	var current any = plain
	for _, part := range strings.Split(dotted, ".") {
		members, ok := current.(map[string]any)
		if !ok {
			return false
		}
		if current, ok = members[part]; !ok {
			return false
		}
	}
	return true
}

func sortedKeys(members map[string]any) []string {
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func readInput(root, relative string) (fileInput, error) {
	target, err := changes.InRepo(root, relative)
	if err != nil {
		return fileInput{}, err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return fileInput{}, nil
	}
	if err != nil {
		return fileInput{}, err
	}
	if !info.Mode().IsRegular() {
		return fileInput{}, fmt.Errorf("%s is not a regular file", relative)
	}
	data, err := os.ReadFile(target) // #nosec G304 -- a validated project file
	if err != nil {
		return fileInput{}, err
	}
	return fileInput{text: string(data), exists: true}, nil
}

func (planner *planner) read(relative string) (string, bool, error) {
	input, err := readInput(planner.root, relative)
	if err != nil {
		return "", false, &UsageError{err.Error()}
	}
	planner.plan.inputs[relative] = input
	return input.text, input.exists, nil
}

// agents plans the block in AGENTS.md and CLAUDE.md's reference to it (spec section 3.2).
func (planner *planner) agents(body string) error {
	content, existed, err := planner.read(agentsFile)
	if err != nil {
		return err
	}
	lines := strings.Split(content, "\n")
	found := block.Locate(lines)
	switch found.Kind {
	case block.None:
		text := block.Marked(body)
		if content != "" {
			text = strings.TrimRight(content, "\n") + "\n\n" + text
		}
		what := "append the block"
		if !existed {
			what = "new, with the block"
		}
		planner.change(Change{ID: "agents-block", Path: agentsFile, What: what}, write{path: agentsFile, data: []byte(text)})
	case block.Current:
		replaced := replaceLines(lines, found.Start, found.Stop+1, block.Marked(body))
		if !found.Intact {
			planner.conflict(Conflict{ID: "agents:edited", Path: agentsFile, acceptable: true, exceptable: true, Lost: found.Body,
				What:        fmt.Sprintf("the block in AGENTS.md (lines %d-%d) was edited by hand", found.Start+1, found.Stop+1),
				Settle:      "move the edit out (to the project's own text, or the profile), then --accept agents:edited",
				replacement: &write{path: agentsFile, data: []byte(replaced)}, acceptWhat: "rewrite the block edited by hand"})
		} else if found.Body != body {
			planner.change(Change{ID: "agents-block", Path: agentsFile, What: "rewrite the block"}, write{path: agentsFile, data: []byte(replaced)})
		}
	case block.Malformed:
		planner.conflict(Conflict{ID: "agents:markers", Path: agentsFile,
			What:   "AGENTS.md's jackstay-adopt markers do not make one block (two begins, an end before its begin, or a begin with no end)",
			Settle: "fix the markers by hand"})
	}
	claude, claudeExists, err := planner.read(claudeFile)
	if err != nil {
		return err
	}
	switch {
	case !claudeExists:
		planner.change(Change{ID: "claude-md", Path: claudeFile, What: "new: @AGENTS.md (Claude Code reads CLAUDE.md)"},
			write{path: claudeFile, data: []byte("@AGENTS.md\n")})
	case !readsAgents.MatchString(claude) && !strings.Contains(claude, "jackstay-adopt: begin agents-block"):
		planner.note("CLAUDE.md does not read AGENTS.md: add `@AGENTS.md` to it so Claude Code sees the block")
	}
	return nil
}

// readsAgents is Claude Code's import of AGENTS.md (`@AGENTS.md` or `@./AGENTS.md`).
var readsAgents = regexp.MustCompile(`(?m)^@(\./)?AGENTS\.md\s*$`)

// replaceLines puts text in place of lines[start:stop]; text ends in a newline.
func replaceLines(lines []string, start, stop int, text string) string {
	var out strings.Builder
	if start > 0 {
		out.WriteString(strings.Join(lines[:start], "\n"))
		out.WriteString("\n")
	}
	out.WriteString(text)
	if stop < len(lines) {
		out.WriteString(strings.Join(lines[stop:], "\n"))
	}
	return out.String()
}

// hook plans the guard hook in .claude/settings.json (spec section 3.3).
func (planner *planner) hook() error {
	content, existed, err := planner.read(settingsFile)
	if err != nil {
		return err
	}
	unreadable := func(why string) {
		planner.conflict(Conflict{ID: "hook:settings-unreadable", Path: settingsFile,
			What: settingsFile + " " + why, Settle: "fix the file by hand"})
	}
	document := jsondoc.NewObject()
	if existed {
		value, err := jsondoc.Parse([]byte(content))
		object, isObject := value.(*jsondoc.Object)
		if err != nil || !isObject {
			unreadable("is not a JSON object")
			return nil
		}
		document = object
	}
	hooks := jsondoc.NewObject()
	if value, present := document.Get("hooks"); present {
		object, isObject := value.(*jsondoc.Object)
		if !isObject {
			unreadable("has `hooks` that is not an object")
			return nil
		}
		hooks = object
	}
	var entries []any
	if value, present := hooks.Get("PreToolUse"); present {
		list, isList := value.([]any)
		if !isList {
			unreadable("has `hooks.PreToolUse` that is not a list")
			return nil
		}
		entries = list
	}
	if hasGuard(entries) {
		return nil
	}
	command := jsondoc.NewObject()
	command.Set("type", "command")
	guard := planner.options.GuardCommand
	if guard == "" {
		launcher, installed := guardLauncher()
		if installed && !isExecutableFile(launcher) {
			planner.conflict(Conflict{ID: "hook:no-guard-launcher", Path: settingsFile,
				What: "the install this jst runs from has no executable " + launcher + " (an install that stopped part way)",
				Settle: "run the original install command again with the same options, its prefix " +
					`"` + shellQuoted(filepath.Dir(filepath.Dir(launcher))) + `"` + " included (`--prefix`), then `jst adopt --apply`"})
			return nil
		}
		guard = guardCommandFor(launcher, homedir.AccountHome())
	}
	command.Set("command", guard)
	command.Set("timeout", json.Number("10"))
	entry := jsondoc.NewObject()
	entry.Set("matcher", "Bash")
	entry.Set("hooks", []any{command})
	hooks.Set("PreToolUse", append(entries, entry))
	document.Set("hooks", hooks)
	planner.change(Change{ID: "hook:guard", Path: settingsFile, What: "add the guard hook (PreToolUse, Bash, jst-guard)"},
		write{path: settingsFile, data: jsondoc.Format(document)})
	return nil
}

// GuardCommand is the hook command for the guard launcher of the install this jst runs from, else
// of the account's ~/.local: written `"$HOME/…"` when the launcher is under the account's home, so
// the settings file names no account, and quoted for a home with a space in it.
func GuardCommand() string {
	launcher, _ := guardLauncher()
	return guardCommandFor(launcher, homedir.AccountHome())
}

// guardLauncher is the guard launcher of the install this jst runs from (installed true), else of
// the account's ~/.local, which is not checked: a checkout or CI may have nothing installed.
func guardLauncher() (launcher string, installed bool) {
	prefix, installed := runningInstall()
	if !installed {
		prefix = filepath.Join(homedir.AccountHome(), ".local")
	}
	return layout.GuardLauncher(prefix), installed
}

// runningInstall is layout.RunningPrefix (a test's own install in tests).
var runningInstall = layout.RunningPrefix

// isExecutableFile says whether path is a file (links followed) someone may run.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// guardCommandFor writes a launcher's path as the hook command, relative to home when it is under it.
func guardCommandFor(launcher, home string) string {
	if home != "" {
		if relative, err := filepath.Rel(home, launcher); err == nil && relative != "." && !strings.HasPrefix(relative, "..") {
			return `"$HOME/` + shellQuoted(filepath.ToSlash(relative)) + `"`
		}
	}
	return `"` + shellQuoted(launcher) + `"`
}

// shellQuoted escapes what a shell reads specially inside double quotes.
func shellQuoted(text string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(text)
}

// guardCommands are the commands of the Bash guard hooks in a settings document: those whose first
// shell word, as the shell reads it, ends in jst-guard.
func guardCommands(entries []any) []string {
	var found []string
	for _, value := range entries {
		entry, _ := value.(*jsondoc.Object)
		if entry == nil {
			continue
		}
		if matcher, _ := getString(entry, "matcher"); matcher != "Bash" {
			continue
		}
		list, _ := entry.Get("hooks")
		commands, _ := list.([]any)
		for _, item := range commands {
			command, _ := getString(asObject(item), "command")
			if program, err := firstWord(command, "/"); err == nil && filepath.Base(program) == layout.Guard {
				found = append(found, command)
			}
		}
	}
	return found
}

// guardLauncherOf is the file a guard hook command runs, its first word expanded as the shell
// expands it with HOME set to home: quotes, $HOME and ${HOME}, and ~ only where unquoted.
func guardLauncherOf(command, home string) (string, error) { return firstWord(command, home) }

// plainProgramWord accepts the program word of a hook command only when it is made of what a path
// needs: plain text, single or double quotes, $HOME or ${HOME}, and a leading ~. Anything else (a
// command or process substitution, another variable, arithmetic, an unquoted pattern) is refused before it
// is expanded: jackstay does not run code to learn where a hook points.
func plainProgramWord(word *syntax.Word) error {
	for _, part := range word.Parts {
		// An unquoted pattern character makes the shell match files: the program depends on the disk.
		if literal, isLiteral := part.(*syntax.Lit); isLiteral && strings.ContainsAny(literal.Value, "*?[") {
			return errors.New("the program is a pattern")
		}
	}
	var refused error
	syntax.Walk(word, func(node syntax.Node) bool {
		if refused != nil {
			return false
		}
		switch part := node.(type) {
		case nil: // the walk's end of a node
			return true
		case *syntax.Word, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted:
			return true
		case *syntax.ParamExp:
			if part.Param == nil || part.Param.Value != "HOME" || part.Excl || part.Length || part.Width ||
				part.Index != nil || part.Slice != nil || part.Repl != nil || part.Exp != nil || part.Names != 0 {
				refused = errors.New("the program names more than $HOME")
			}
			return false
		default:
			refused = fmt.Errorf("the program is not a plain path (%T)", node)
			return false
		}
	})
	return refused
}

// firstWord is the first word of a shell command line after the shell's expansions, with HOME set
// to home and nothing else in the environment; an error when the line is not one simple command or
// its first word needs more than that (a command substitution, another variable).
func firstWord(command, home string) (string, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return "", err
	}
	if len(file.Stmts) != 1 {
		return "", fmt.Errorf("not one command: %q", command)
	}
	call, isCall := file.Stmts[0].Cmd.(*syntax.CallExpr)
	if !isCall || len(call.Args) == 0 {
		return "", fmt.Errorf("not a simple command: %q", command)
	}
	if err := plainProgramWord(call.Args[0]); err != nil {
		return "", fmt.Errorf("%w: %q", err, command)
	}
	config := &expand.Config{Env: expand.ListEnviron("HOME=" + home), NoUnset: true}
	fields, err := expand.Fields(config, call.Args[0])
	if err != nil {
		return "", err
	}
	if len(fields) != 1 {
		return "", fmt.Errorf("the program is not one word: %q", command)
	}
	return fields[0], nil
}

func hasGuard(entries []any) bool { return len(guardCommands(entries)) > 0 }

// HookProblems are the project's guard hooks whose command does not run an executable jst-guard
// (an install moved, or one under another prefix): Claude Code would then run every command
// unguarded. Nothing when the settings file is absent or holds no guard hook; that is the plan's.
func HookProblems(root, home string) []string {
	content, err := os.ReadFile(filepath.Join(root, settingsFile))
	if err != nil {
		return nil
	}
	value, err := jsondoc.Parse(content)
	document, isObject := value.(*jsondoc.Object)
	if err != nil || !isObject {
		return nil
	}
	hooksValue, _ := document.Get("hooks")
	hooks, _ := hooksValue.(*jsondoc.Object)
	if hooks == nil {
		return nil
	}
	entriesValue, _ := hooks.Get("PreToolUse")
	entries, _ := entriesValue.([]any)
	var problems []string
	for _, command := range guardCommands(entries) {
		launcher, err := guardLauncherOf(command, home)
		if err != nil {
			problems = append(problems, fmt.Sprintf("the guard hook in %s (%s) cannot be read as one program to run (%v): Claude Code may run every command unguarded; point it at %s",
				settingsFile, command, err, GuardCommand()))
			continue
		}
		if !isExecutableFile(launcher) {
			problems = append(problems, fmt.Sprintf("the guard hook in %s runs %s, which is not an executable %s: Claude Code runs every command unguarded; point it at %s",
				settingsFile, launcher, layout.Guard, GuardCommand()))
		}
	}
	return problems
}

func asObject(value any) *jsondoc.Object {
	object, _ := value.(*jsondoc.Object)
	return object
}

// wantedTemplates are the templates whose condition holds: jackstay's, replaced by the profile's of the
// same name, sorted by name.
func wantedTemplates(chosen *profile.Profile, plain map[string]any) []templates.Template {
	byName := map[string]templates.Template{}
	for _, template := range templates.Embedded() {
		byName[template.Name] = template
	}
	if chosen != nil {
		for _, template := range chosen.Workflows {
			byName[template.Name] = template
		}
	}
	var wanted []templates.Template
	for _, template := range byName {
		enabled := len(template.When) == 0
		for _, section := range template.When {
			if _, present := plain[section]; present {
				enabled = true
			}
		}
		if enabled {
			wanted = append(wanted, template)
		}
	}
	sort.Slice(wanted, func(left, right int) bool { return wanted[left].Name < wanted[right].Name })
	return wanted
}

// workflows plans the workflows jackstay writes and the inventory of the marked ones (spec 3.4, 7).
func (planner *planner) workflows(chosen *profile.Profile) error {
	pin, _ := planner.plain.Section("adopt")["pin"].(string)
	values := planner.workflowValues(chosen, pin)
	existing, err := planner.existingWorkflows()
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, template := range wantedTemplates(chosen, planner.templatePlain()) {
		wanted[template.Name] = true
		body, err := templates.Render(template.Text, values)
		if err != nil {
			return &UsageError{fmt.Sprintf("the template %s: %v", template.Name, err)}
		}
		path := workflowsDir + "/" + template.Name + ".yml"
		text := templates.Marked(template.Name, body)
		current, present := existing[template.Name+".yml"]
		marked, headerName, intact := templates.Header(current)
		switch {
		case !present:
			planner.change(Change{ID: "workflow:" + template.Name, Path: path, What: "new"}, write{path: path, data: []byte(text)})
		case marked && intact && headerName == template.Name:
			if current != text {
				planner.change(Change{ID: "workflow:" + template.Name, Path: path, What: "rewrite"}, write{path: path, data: []byte(text)})
			}
		case marked:
			planner.conflict(Conflict{ID: "workflow:" + template.Name + ":edited", Path: path, acceptable: true, exceptable: true, Lost: current,
				What:        path + " was written by jst adopt and edited by hand since",
				Settle:      "move the edit into the profile, then --accept; or keep the file with an exception and the reason",
				replacement: &write{path: path, data: []byte(text)}, acceptWhat: "rewrite the workflow edited by hand"})
		default:
			planner.conflict(Conflict{ID: "workflow:" + template.Name + ":unmarked", Path: path, acceptable: true, exceptable: true, Lost: current,
				What:        path + " is the project's own, and jackstay's template " + template.Name + " goes there",
				Settle:      "--accept to let jackstay's template replace it; or keep the project's with an exception and the reason",
				replacement: &write{path: path, data: []byte(text)}, acceptWhat: "jackstay's template in place of the project's file"})
		}
	}
	for _, file := range sortedFiles(existing) {
		name := strings.TrimSuffix(strings.TrimSuffix(file, ".yml"), ".yaml")
		path := workflowsDir + "/" + file
		marked, _, intact := templates.Header(existing[file])
		// Wanted is exactly <name>.yml; a marked file under any other name or extension is in the
		// inventory too (spec section 3.4).
		isWanted := wanted[name] && file == name+".yml"
		switch {
		case marked && isWanted:
		case marked && intact:
			planner.change(Change{ID: "workflow:" + name, Path: path, What: "remove: no template wants it now"}, write{path: path, remove: true})
		case marked:
			planner.conflict(Conflict{ID: "workflow:" + name + ":edited", Path: path, acceptable: true, exceptable: true, Lost: existing[file],
				What:        path + " was written by jst adopt, no template wants it now, and it was edited by hand since",
				Settle:      "--accept to remove it; or keep it with an exception and the reason",
				replacement: &write{path: path, remove: true}, acceptWhat: "remove the workflow edited by hand"})
		case isWanted:
		default:
			planner.retained = append(planner.retained, path)
			planner.note(path + " is the project's own; jackstay leaves it alone: keep it, or remove from it what jackstay's workflows do")
		}
	}
	return nil
}

func (planner *planner) existingWorkflows() (map[string]string, error) {
	folder := filepath.Join(planner.root, filepath.FromSlash(workflowsDir))
	entries, err := os.ReadDir(folder)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, &UsageError{err.Error()}
	}
	found := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		text, exists, err := planner.read(workflowsDir + "/" + name)
		if err != nil {
			return nil, err
		}
		if exists {
			found[name] = text
		}
	}
	return found, nil
}

func sortedFiles(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// except turns the conflicts the project keeps into exceptions; an exception that matches no
// conflict is a note.
func (planner *planner) except(exceptions map[string]string) {
	used := map[string]bool{}
	kept := make([]Conflict, 0, len(planner.plan.Conflicts))
	for _, conflict := range planner.plan.Conflicts {
		why, excepted := exceptions[conflict.ID]
		switch {
		case excepted && conflict.exceptable:
			used[conflict.ID] = true
			planner.plan.Exceptions = append(planner.plan.Exceptions, Exception{ID: conflict.ID, Why: why})
			if strings.HasPrefix(conflict.ID, "workflow:") {
				planner.retained = append(planner.retained, conflict.Path)
				planner.note(conflict.Path + " is kept by an exception: it is the project's own; keep it, or remove from it what jackstay's workflows do")
			}
		case excepted:
			used[conflict.ID] = true
			planner.note("the exception for " + conflict.ID + " is ignored: that conflict cannot be excepted")
			kept = append(kept, conflict)
		default:
			kept = append(kept, conflict)
		}
	}
	planner.plan.Conflicts = kept
	for _, id := range sortedKeysOf(exceptions) {
		if !used[id] {
			planner.note("the exception for " + id + " matches no conflict now (stale): it may be removed from jackstay.json")
		}
	}
}

func sortedKeysOf(members map[string]string) []string {
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// accept turns the accepted conflicts into changes.
func (planner *planner) accept(ids []string) error {
	for _, id := range ids {
		index := slices.IndexFunc(planner.plan.Conflicts, func(conflict Conflict) bool { return conflict.ID == id })
		if index < 0 {
			return &UsageError{fmt.Sprintf("--accept %s: there is no such conflict now (the plan lists them)", id)}
		}
		conflict := planner.plan.Conflicts[index]
		if !conflict.acceptable || conflict.replacement == nil {
			return &UsageError{fmt.Sprintf("--accept %s: this conflict is settled in the project, not by --accept (%s)", id, conflict.Settle)}
		}
		planner.plan.Conflicts = slices.Delete(planner.plan.Conflicts, index, index+1)
		planner.change(Change{ID: id, Path: conflict.Path, What: "accepted: " + conflict.acceptWhat}, *conflict.replacement)
	}
	return nil
}

func (planner *planner) change(change Change, pending write) {
	planner.plan.Changes = append(planner.plan.Changes, change)
	planner.plan.writes = append(planner.plan.writes, pending)
}

func (planner *planner) conflict(conflict Conflict) {
	planner.plan.Conflicts = append(planner.plan.Conflicts, conflict)
}

func (planner *planner) note(text string) {
	planner.plan.Notes = append(planner.plan.Notes, text)
}

func (planner *planner) sort() {
	plan := planner.plan
	sort.SliceStable(plan.Changes, func(left, right int) bool { return plan.Changes[left].ID < plan.Changes[right].ID })
	sort.SliceStable(plan.Conflicts, func(left, right int) bool { return plan.Conflicts[left].ID < plan.Conflicts[right].ID })
	sort.SliceStable(plan.Exceptions, func(left, right int) bool { return plan.Exceptions[left].ID < plan.Exceptions[right].ID })
	sort.Strings(plan.Notes)
	sort.SliceStable(plan.writes, func(left, right int) bool { return plan.writes[left].path < plan.writes[right].path })
}

func (plan *Plan) checkInput(relative string, expected fileInput) error {
	actual, err := readInput(plan.Root, relative)
	if err != nil {
		return fmt.Errorf("checking %s before apply: %w", relative, err)
	}
	if actual != expected {
		return fmt.Errorf("%s changed after planning; rerun jst adopt to review the new state", relative)
	}
	return nil
}
